// Package runner is the agent's main loop. A short base tick drives three
// cadences: poll for pings every tick (responsive), sample activity every
// minute, and capture a screenshot every few minutes. The monotonic sequence
// is persisted after each accepted activity sample so a restart resumes cleanly.
package runner

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"avora-agent/internal/autostart"
	"avora-agent/internal/capture"
	"avora-agent/internal/collect"
	"avora-agent/internal/config"
	"avora-agent/internal/ingest"
	"avora-agent/internal/notify"
	"avora-agent/internal/selfupdate"
)

const (
	baseInterval         = 15 * time.Second
	activityEverySteps   = 2   // ~30s
	screenshotEverySteps = 20  // ~5min
	updateEverySteps     = 960 // ~4h — check for a newer release + self-update
	// Debounce event-triggered screenshots so rapid app-switching can't spam.
	eventShotMinInterval = 90 * time.Second
)

// Run loops until interrupted, or until the device token is rejected (revoked),
// in which case it de-enrolls and exits cleanly.
func Run(cfg *config.Config) error {
	client := &http.Client{Timeout: 20 * time.Second}
	// Clear any stale personal-mode pause persisted by an older build, so a
	// previously-paused machine resumes (the toggle was removed; capture is
	// always on) and never sits looking offline.
	if cfg.PersonalMode {
		cfg.PersonalMode = false
		_ = cfg.Save()
	}
	fmt.Println("Avora agent running — Ctrl-C to stop.")
	var lastApp string
	var lastEventShot time.Time
	for step := 0; ; step++ {
		// Each iteration runs inside a recover so a panic deep in a platform
		// syscall (collect/capture) can't take the whole agent down — without
		// this, one bad sample turns a Windows machine offline until the next
		// login. A revoked token is the only intentional exit.
		if runStep(client, cfg, step, &lastApp, &lastEventShot) {
			return deauthorize(cfg)
		}
		// The sleep is outside the recovered step so a persistent panic backs off
		// (one attempt per tick) instead of spinning the CPU.
		time.Sleep(baseInterval)
	}
}

// runStep performs one loop iteration and reports whether the device was revoked
// (the caller then de-enrolls and exits). Any panic is recovered and logged so
// the loop survives and tries again on the next tick.
func runStep(client *http.Client, cfg *config.Config, step int, lastApp *string, lastEventShot *time.Time) (revokedNow bool) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("  warn: recovered from panic, continuing: %v\n", r)
		}
	}()

	// Self-update on startup (step 0) and every few hours: if a newer release
	// is published, this swaps the binary and restarts — so a fix ships to
	// every machine on its own, no reinstall. Best-effort; never blocks work.
	if step%updateEverySteps == 0 {
		selfupdate.CheckAndApply(config.Version(), config.UpdateRepo())
	}
	// Poll inbound commands (capture/ping/update) first.
	if err := handlePings(client, cfg); revoked(err) {
		return true
	}
	// Capture is always on (work mode) — the personal-mode pause was removed, so a
	// machine can never get stuck paused-and-looking-offline.
	if step%activityEverySteps == 0 {
		app, err := tick(client, cfg)
		if revoked(err) {
			return true
		} else if err != nil {
			fmt.Println("  warn: " + err.Error())
		}
		// Event-triggered screenshot: the foreground app changed.
		if app != "" && app != *lastApp {
			*lastApp = app
			if time.Since(*lastEventShot) > eventShotMinInterval {
				if err := shotTick(client, cfg); err == nil {
					*lastEventShot = time.Now()
				}
			}
		}
	}
	if step%screenshotEverySteps == 0 {
		if err := shotTick(client, cfg); revoked(err) {
			return true
		} else if err != nil {
			fmt.Println("  warn (screenshot): " + err.Error())
		}
		*lastEventShot = time.Now()
	}
	return false
}

func revoked(err error) bool { return errors.Is(err, ingest.ErrUnauthorized) }

// deauthorize stops the agent cleanly when the device is revoked: remove
// auto-start (so it won't relaunch at login), clear the dead token, and exit.
// Reconnecting is then a single `avora-agent install`.
func deauthorize(cfg *config.Config) error {
	fmt.Println("This device was revoked in Avora — stopping. Run `avora-agent install` to reconnect.")
	_ = autostart.Disable()
	cfg.DeviceToken = ""
	cfg.Sequence = 0
	_ = cfg.Save()
	return nil
}

// handlePings delivers inbound commands: a "capture" takes a screenshot now; a
// "ping" plays a sound + shows the message.
func handlePings(client *http.Client, cfg *config.Config) error {
	pings, err := ingest.FetchPings(client, cfg)
	if err != nil {
		return err // caller checks for a revoked token; otherwise transient
	}
	for _, p := range pings {
		switch p.Kind {
		case "capture":
			fmt.Println("  📸 capture requested")
			if err := shotTick(client, cfg); err != nil {
				fmt.Println("  warn (capture): " + err.Error())
			}
		case "mode_personal":
			setMode(cfg, true)
		case "mode_work":
			setMode(cfg, false)
		case "update":
			fmt.Println("  ⬆️  update requested")
			selfupdate.CheckAndApply(config.Version(), config.UpdateRepo())
		default:
			notify.Ping(p.Message)
			fmt.Printf("  🔔 ping received: %s\n", p.Message)
		}
	}
	return nil
}

// setMode flips personal/work capture and persists it so it survives a restart.
func setMode(cfg *config.Config, personal bool) {
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

// tick collects + sends one activity sample and returns the foreground app it
// observed (so the loop can trigger an event screenshot when it changes).
func tick(client *http.Client, cfg *config.Config) (string, error) {
	sample, err := collect.Collect()
	if err != nil {
		return "", err
	}

	cfg.Sequence++
	err = ingest.Send(client, cfg, cfg.Sequence, sample)
	// A replay means our local high-water mark drifted behind the server's;
	// nudge it forward once and retry so we self-heal instead of wedging.
	if err == ingest.ErrReplay {
		cfg.Sequence++
		err = ingest.Send(client, cfg, cfg.Sequence, sample)
	}
	if err != nil {
		return "", err
	}

	app := sample.ActiveWindow
	shown := app
	if shown == "" {
		shown = "(unknown)"
	}
	fmt.Printf("  ✓ seq %d — %s · idle %ds\n", cfg.Sequence, shown, sample.IdleSeconds)
	return app, cfg.Save()
}

func shotTick(client *http.Client, cfg *config.Config) error {
	shot, err := capture.Capture()
	if err != nil {
		return err
	}
	if err := ingest.SendScreenshot(client, cfg, shot); err != nil {
		return err
	}
	fmt.Printf("  ✓ screenshot %dx%d (%d KB)\n", shot.Width, shot.Height, len(shot.JPEG)/1024)
	return nil
}
