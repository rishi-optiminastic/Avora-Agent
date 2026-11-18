// Package runner is the agent's main loop. A short base tick drives the
// cadences: poll for pings every tick (responsive), sample activity every ~30s,
// capture a screenshot every few minutes. Everything that talks to the network
// slowly - uploads, the offline backlog, health reports, self-update - runs on
// its own goroutine, so a slow server or a dead wifi never stops capture. See
// delivery.go for the wiring.
package runner

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"avora-agent/internal/capture"
	"avora-agent/internal/collect"
	"avora-agent/internal/config"
	"avora-agent/internal/ingest"
	"avora-agent/internal/notify"
	"avora-agent/internal/queue"
	"avora-agent/internal/selfupdate"
)

const (
	baseInterval         = 15 * time.Second
	activityEverySteps   = 2   // ~30s
	screenshotEverySteps = 20  // ~5min
	updateEverySteps     = 960 // ~4h — check for a newer release + self-update
	pingTimeout          = 8 * time.Second
	// Debounce event-triggered screenshots so rapid app-switching can't spam.
	eventShotMinInterval = 90 * time.Second

	// A 401 is the server's CLAIM that this device is revoked, not proof of it.
	// Treat it as transient until it has persisted this long without a single
	// accepted call in between.
	//
	// One 401 used to be enough: the agent deleted its own auto-start, wiped its
	// token and exited. So a momentary backend problem — a redeploy that changed
	// AGENT_TOKEN_PEPPER, a proxy answering 401, a token lookup that briefly
	// found nothing — took laptops off the fleet PERMANENTLY, with reinstalling
	// by hand on each machine as the only cure. A genuine revocation still takes
	// effect; it just has to mean it.
	revokedAfter = 24 * time.Hour
	// How often to re-test the token once dormant. Cheap, and it is what lets a
	// machine come back without anyone touching it.
	dormantInterval = 10 * time.Minute

	// If the main loop has not completed an iteration in this long it is wedged,
	// and the watchdog exits so auto-start can replace the process. MUST stay
	// comfortably above dormantInterval, which is the longest legitimate gap
	// between iterations.
	loopStallTimeout = 20 * time.Minute
	watchdogPoll     = time.Minute
)

// Run loops until interrupted. It never exits on its own and never removes its
// own auto-start: a refused token puts the agent to sleep and it keeps probing,
// so whatever broke can be fixed on the server and every machine comes back by
// itself.
func Run(cfg *config.Config) error {
	client := &http.Client{Timeout: 20 * time.Second}
	// Commands are polled from the capture loop, so that call stays short:
	// a slow server costs at most a few seconds of a tick, never a sample.
	pingClient := &http.Client{Timeout: pingTimeout}
	// Clear any stale personal-mode pause persisted by an older build, so a
	// previously-paused machine resumes (the toggle was removed; capture is
	// always on) and never sits looking offline.
	if cfg.PersonalMode {
		cfg.PersonalMode = false
		_ = cfg.Save()
	}
	fmt.Println("Avora agent running — Ctrl-C to stop.")
	a := newAgent(client, cfg)
	a.pingClient = pingClient
	a.startCourier()
	warnedDormant := false

	// The supervisors can only replace a process that EXITED; nothing on either
	// platform notices one that is alive but stuck. The watchdog turns the
	// second case into the first.
	progress := &atomic.Int64{}
	progress.Store(time.Now().Unix())
	go watchdog(progress)

	for step := 0; ; step++ {
		progress.Store(time.Now().Unix())
		if rejectedFor(cfg) > revokedAfter {
			warnedDormant = dormantTick(client, cfg, warnedDormant)
			time.Sleep(dormantInterval)
			continue
		}
		warnedDormant = false
		// Each iteration runs inside a recover so a panic deep in a platform
		// syscall (collect/capture) can't take the whole agent down — without
		// this, one bad sample turns a Windows machine offline until the next
		// login.
		a.runStep(step)
		// The sleep is outside the recovered step so a persistent panic backs off
		// (one attempt per tick) instead of spinning the CPU.
		time.Sleep(baseInterval)
	}
}

// watchdog ends the process when the main loop stops making progress.
//
// Task Scheduler's IgnoreNew and launchd's KeepAlive only restart a process
// that has exited. A capture or collect call blocked inside a platform syscall
// therefore froze the agent indefinitely: the machine read as offline while a
// healthy-looking process sat there doing nothing, and only a reboot or a
// manual reinstall cleared it. Exiting hands the machine back to the
// supervisor, which starts a clean process within a few minutes.
//
// It runs on its own goroutine precisely because the loop it is watching may be
// the thing that is stuck.
func watchdog(progress *atomic.Int64) {
	for {
		time.Sleep(watchdogPoll)
		stalled := time.Since(time.Unix(progress.Load(), 0))
		if stalled > loopStallTimeout {
			fmt.Printf(
				"  watchdog: no loop progress for %s — exiting so auto-start replaces this process\n",
				stalled.Round(time.Second),
			)
			os.Exit(1)
		}
	}
}

// dormantTick runs one slow probe while the token is being refused. It captures
// nothing and only asks the server whether the token works again, so a device
// that is re-enrolled — or a backend whose pepper is restored — resumes on its
// own. The agent stays installed and auto-starting throughout.
func dormantTick(client *http.Client, cfg *config.Config, warned bool) bool {
	if !warned {
		fmt.Println("  Avora: token refused for over a day — capture paused, still retrying.")
		notify.Ping("Avora has lost access on this machine. Ask IT to reconnect it. " +
			"The agent is still installed and will resume on its own once it is.")
	}
	_, err := ingest.FetchPings(client, cfg)
	recordAuth(cfg, err)
	return true
}

// ---- token-rejection state ------------------------------------------------ #

// stateMu guards the agent's saved state (agent.json): the capture loop and the
// delivery goroutine both update it - token status from any call, the sequence
// counter from sends - and two concurrent Saves would interleave the file.
var stateMu sync.Mutex

// recordAuth folds one call's outcome into the current run of rejections.
//
// Only a reply that actually reached the backend says anything about the token:
// a network error means the laptop is on a train, not that it was revoked, so it
// neither starts nor clears a run. A replay (409) is proof the token was
// ACCEPTED — authentication runs before the route — so it clears one.
func recordAuth(cfg *config.Config, err error) {
	stateMu.Lock()
	defer stateMu.Unlock()
	switch {
	case errors.Is(err, ingest.ErrUnauthorized):
		noteAuthFailure(cfg)
	case err == nil, errors.Is(err, ingest.ErrReplay):
		noteAuthOK(cfg)
	}
}

func noteAuthFailure(cfg *config.Config) {
	if cfg.UnauthorizedSince != "" {
		return // already counting; keep the ORIGINAL timestamp
	}
	cfg.UnauthorizedSince = time.Now().UTC().Format(time.RFC3339)
	_ = cfg.Save()
	fmt.Println("  warn: server refused this device's token — retrying, staying installed.")
}

func noteAuthOK(cfg *config.Config) {
	if cfg.UnauthorizedSince == "" {
		return
	}
	cfg.UnauthorizedSince = ""
	_ = cfg.Save()
	fmt.Println("  ✓ token accepted again — resuming.")
}

// rejectedFor is how long the current unbroken run of 401s has lasted, or 0
// when the last call that reached the server was accepted.
func rejectedFor(cfg *config.Config) time.Duration {
	stateMu.Lock()
	defer stateMu.Unlock()
	if cfg.UnauthorizedSince == "" {
		return 0
	}
	since, err := time.Parse(time.RFC3339, cfg.UnauthorizedSince)
	if err != nil {
		noteAuthOK(cfg) // unreadable stamp: start over rather than sulk forever
		return 0
	}
	// A clock moved backwards (or a stamp written in the future) would otherwise
	// read as a negative age and keep the agent out of dormancy indefinitely.
	if d := time.Since(since); d > 0 {
		return d
	}
	return 0
}

// runStep performs one loop iteration. Any panic is recovered and logged so the
// loop survives and tries again on the next tick. Nothing it sees can end the
// agent: a refused token is recorded and retried, never acted on here.
func (a *agent) runStep(step int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("  warn: recovered from panic, continuing: %v\n", r)
		}
	}()

	// Self-update on startup (step 0) and every few hours: if a newer release
	// is published, this swaps the binary and restarts — so a fix ships to
	// every machine on its own, no reinstall. Best-effort; never blocks work.
	if step%updateEverySteps == 0 {
		checkForUpdate()
	}
	// Poll inbound commands (capture/ping/update) first.
	recordAuth(a.cfg, a.handlePings())
	// Capture is always on (work mode) — the personal-mode pause was removed, so a
	// machine can never get stuck paused-and-looking-offline.
	if step%activityEverySteps == 0 {
		app := a.tick()
		// Event-triggered screenshot: the foreground app changed.
		if app != "" && app != a.lastApp {
			a.lastApp = app
			if time.Since(a.lastEventShot) > eventShotMinInterval {
				if a.shotTick() {
					a.lastEventShot = time.Now()
				}
			}
		}
	}
	if step%screenshotEverySteps == 0 {
		a.shotTick()
		a.lastEventShot = time.Now()
	}
	// Uploads, the offline backlog and health reports are the courier's job
	// (delivery.go), so nothing here waits on the network.
}

// handlePings delivers inbound commands: a "capture" takes a screenshot now; a
// "ping" plays a sound + shows the message.
func (a *agent) handlePings() error {
	pings, err := ingest.FetchPings(a.pingClient, a.cfg)
	if err != nil {
		return err // caller checks for a revoked token; otherwise transient
	}
	for _, p := range pings {
		switch p.Kind {
		case "capture":
			fmt.Println("  📸 capture requested")
			a.shotTick()
		case "mode_personal":
			setMode(a.cfg, true)
		case "mode_work":
			setMode(a.cfg, false)
		case "update":
			fmt.Println("  ⬆️  update requested")
			checkForUpdate()
		default:
			notify.Ping(p.Message)
			fmt.Printf("  🔔 ping received: %s\n", p.Message)
		}
	}
	return nil
}

// setMode flips personal/work capture and persists it so it survives a restart.
func setMode(cfg *config.Config, personal bool) {
	stateMu.Lock()
	defer stateMu.Unlock()
	if cfg.PersonalMode == personal {
		return
	}
	cfg.PersonalMode = personal
	if personal {
		fmt.Println("  ⏸️  personal mode — capture paused")
	} else {
		fmt.Println("  ▶️  work mode — capture resumed")
	}
	if err := cfg.Save(); err != nil {
		fmt.Println("  warn (mode save): " + err.Error())
	}
}

// tick collects one activity sample and hands it to the outbox, returning the
// foreground app it observed (so the loop can trigger an event screenshot when
// it changes). The sequence number is drawn when the sample is actually sent,
// which may be now or, after an outage, from the queue later.
func (a *agent) tick() string {
	sample, err := collect.Collect()
	a.health.ObserveSample(sample, err)
	if err != nil {
		fmt.Println("  warn: " + err.Error())
		return ""
	}
	a.submit(&queue.Entry{
		Kind:       queue.KindActivity,
		CapturedAt: time.Now(),
		Sample:     &sample,
	})
	return sample.ActiveWindow
}

// shotTick captures one screenshot and hands it to the outbox. It reports
// whether the capture itself succeeded: a screenshot that was captured but
// queued for later still counts, so the event debounce is not defeated by an
// outage.
func (a *agent) shotTick() bool {
	shot, err := capture.Capture()
	a.health.ObserveCapture(err)
	if err != nil {
		fmt.Println("  warn (screenshot): " + err.Error())
		return false
	}
	a.submit(&queue.Entry{
		Kind:       queue.KindScreenshot,
		CapturedAt: time.Now(),
		Shot:       &shot,
	})
	return true
}

// updating keeps self-update checks from piling up: a check downloads a
// release, which can take minutes on slow wifi.
var updating atomic.Bool

// checkForUpdate looks for a newer release on its own goroutine, so a slow
// download never holds up capture. A check already in flight wins.
func checkForUpdate() {
	if !updating.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer updating.Store(false)
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("  warn: self-update recovered from panic: %v\n", r)
			}
		}()
		selfupdate.CheckAndApply(config.Version(), config.UpdateRepo())
	}()
}
