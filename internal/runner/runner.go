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
)

const (
	baseInterval         = 15 * time.Second
	activityEverySteps   = 4  // ~60s
	screenshotEverySteps = 20 // ~5min
)

// Run loops until interrupted, or until the device token is rejected (revoked),
// in which case it de-enrolls and exits cleanly.
func Run(cfg *config.Config) error {
	client := &http.Client{Timeout: 20 * time.Second}
	fmt.Println("Avora agent running — Ctrl-C to stop.")
	for step := 0; ; step++ {
		if err := handlePings(client, cfg); revoked(err) {
			return deauthorize(cfg)
		}
		if step%activityEverySteps == 0 {
			if err := tick(client, cfg); revoked(err) {
				return deauthorize(cfg)
			} else if err != nil {
				fmt.Println("  warn: " + err.Error())
			}
		}
		if step%screenshotEverySteps == 0 {
			if err := shotTick(client, cfg); revoked(err) {
				return deauthorize(cfg)
			} else if err != nil {
				fmt.Println("  warn (screenshot): " + err.Error())
			}
		}
		time.Sleep(baseInterval)
	}
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

// handlePings delivers any inbound pings — sound + on-screen message.
func handlePings(client *http.Client, cfg *config.Config) error {
	pings, err := ingest.FetchPings(client, cfg)
	if err != nil {
		return err // caller checks for a revoked token; otherwise transient
	}
	for _, p := range pings {
		notify.Ping(p.Message)
		fmt.Printf("  🔔 ping received: %s\n", p.Message)
	}
	return nil
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
