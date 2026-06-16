// Package runner is the agent's main loop. A short base tick drives three
// cadences: poll for pings every tick (responsive), sample activity every
// minute, and capture a screenshot every few minutes. The monotonic sequence
// is persisted after each accepted activity sample so a restart resumes cleanly.
package runner

import (
	"fmt"
	"net/http"
	"time"

	"avora-agent/internal/capture"
	"avora-agent/internal/collect"
	"avora-agent/internal/config"
	"avora-agent/internal/ingest"
	"avora-agent/internal/notify"
)

const (
	baseInterval         = 15 * time.Second
	activityEverySteps   = 4  // ~60s
	screenshotEverySteps = 20 // ~5min
)

// Run loops until the process is interrupted.
func Run(cfg *config.Config) error {
	client := &http.Client{Timeout: 20 * time.Second}
	fmt.Println("Avora agent running — Ctrl-C to stop.")
	for step := 0; ; step++ {
		handlePings(client, cfg)
		if step%activityEverySteps == 0 {
			if err := tick(client, cfg); err != nil {
				fmt.Println("  warn: " + err.Error())
			}
		}
		if step%screenshotEverySteps == 0 {
			if err := shotTick(client, cfg); err != nil {
				fmt.Println("  warn (screenshot): " + err.Error())
			}
		}
		time.Sleep(baseInterval)
	}
}

// handlePings delivers any inbound pings — sound + on-screen message.
func handlePings(client *http.Client, cfg *config.Config) {
	pings, err := ingest.FetchPings(client, cfg)
	if err != nil {
		return // transient; try again next tick
	}
	for _, p := range pings {
		notify.Ping(p.Message)
		fmt.Printf("  🔔 ping received: %s\n", p.Message)
	}
}

func tick(client *http.Client, cfg *config.Config) error {
	sample, err := collect.Collect()
	if err != nil {
		return err
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
		return err
	}

	app := sample.ActiveWindow
	if app == "" {
		app = "(unknown)"
	}
	fmt.Printf("  ✓ seq %d — %s · idle %ds\n", cfg.Sequence, app, sample.IdleSeconds)
	return cfg.Save()
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
