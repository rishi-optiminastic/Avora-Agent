// Command avora-agent is the Avora desktop activity agent.
//
//	avora-agent install    one-step setup: enroll + run automatically at login
//	avora-agent uninstall  remove auto-start
//	avora-agent enroll     link this machine to your Avora account (browser)
//	avora-agent run        start sampling + posting activity
//	avora-agent status     show enrollment / config
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"avora-agent/internal/autostart"
	"avora-agent/internal/config"
	"avora-agent/internal/enroll"
	"avora-agent/internal/notify"
	"avora-agent/internal/runner"
)

// hasConsole is false when nothing can see stdout — a Windows double-click from
// Explorer, or the login task. Set once in main; `say` and `check` read it to
// decide between printing and showing a dialog.
var hasConsole = true

func main() {
	// Before anything else: DPI awareness is fixed for a process at the first
	// window or device context it touches, and the screen capture depends on it
	// (see dpi_windows.go). No-op off Windows.
	setDPIAware()

	// Built with -H windowsgui (no console window, so the login auto-start shows
	// nothing). When a user runs a command from a terminal, this re-attaches
	// stdout so output is still visible. Always true off Windows.
	hasConsole = attachParentConsole()

	// No arguments means someone double-clicked the download. That used to print
	// usage into a console that does not exist and exit 2 — so the agent looked
	// broken, and the only working path was pasting a command into PowerShell.
	// Installing is the one thing a double-click can reasonably mean, and it is
	// idempotent, so running it twice repairs rather than breaks.
	command := "install"
	if len(os.Args) >= 2 {
		command = os.Args[1]
	}

	cfg, err := config.Load()
	check(err)

	switch command {
	case "install":
		cmdInstall(cfg)
	case "uninstall":
		cmdUninstall()
	case "enroll":
		cmdEnroll(cfg)
	case "run":
		cmdRun(cfg)
	case "status":
		cmdStatus(cfg)
	default:
		usage()
		os.Exit(2)
	}
}

// cmdInstall is the one-step path for employees: copy the binary somewhere
// stable, enroll (browser) if needed, then register auto-start at login.
func cmdInstall(cfg *config.Config) {
	target, err := installBinary()
	check(err)
	if !cfg.Enrolled() {
		token, err := enroll.Run(cfg, hostname(), osName())
		check(err)
		cfg.DeviceToken = token
		cfg.Sequence = 0
		check(cfg.Save())
	}
	// Auto-start is best-effort: if registering it fails, the device is still
	// enrolled — tell the user how to start it rather than aborting the install.
	if err := autostart.Enable(target); err != nil {
		fmt.Fprintln(os.Stderr, "warn: could not enable auto-start: "+err.Error())
		say(fmt.Sprintf(
			"Avora is connected, but it could not be set to start automatically.\n\n%s\n\n"+
				"Start it manually with:\n%s run", err.Error(), target))
		return
	}
	say("Avora is set up — it's running now and will start automatically at login.")
}

func cmdUninstall() {
	if err := autostart.Disable(); err != nil {
		fmt.Fprintln(os.Stderr, "warn: "+err.Error())
	}
	fmt.Println("Avora auto-start removed.")
}

// installBinary copies the running executable into a stable per-user location so
// auto-start doesn't break if the download is moved or deleted.
func installBinary() (string, error) {
	dir, err := installDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := "avora-agent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)

	src, err := os.Executable()
	if err != nil {
		return "", err
	}
	if abs, _ := filepath.Abs(src); abs == target {
		return target, nil // already running from the install location
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	if err := replaceBinary(target, data); err != nil {
		return "", err
	}
	return target, nil
}

// replaceBinary writes `data` to `target`, even when `target` is a copy of this
// agent that is currently RUNNING.
//
// Windows refuses to overwrite or delete a running .exe, so double-clicking the
// download to repair an install would fail with a sharing violation — the most
// likely way anyone re-runs it. A running image can still be RENAMED, so move
// the old one aside and rename the new one into place (the same trick
// selfupdate uses). The .old file is cleaned up here when it is free, and
// otherwise on the next install.
func replaceBinary(target string, data []byte) error {
	staged := target + ".new"
	if err := os.WriteFile(staged, data, 0o755); err != nil {
		return err
	}
	old := target + ".old"
	_ = os.Remove(old) // a leftover from a previous swap, now unlocked
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, old); err != nil {
			_ = os.Remove(staged)
			return err
		}
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Rename(old, target) // roll back rather than leave no agent at all
		return err
	}
	_ = os.Remove(old)
	return nil
}

func installDir() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = os.Getenv("APPDATA")
		}
		return filepath.Join(base, "Avora"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Avora"), nil
	}
	return filepath.Join(home, ".local", "share", "avora"), nil
}

func cmdEnroll(cfg *config.Config) {
	token, err := enroll.Run(cfg, hostname(), osName())
	check(err)
	cfg.DeviceToken = token
	cfg.Sequence = 0
	check(cfg.Save())
	fmt.Println("Device connected. Run `avora-agent run` to start tracking.")
}

func cmdRun(cfg *config.Config) {
	if !cfg.Enrolled() {
		fmt.Println("Not enrolled yet. Run `avora-agent enroll` first.")
		os.Exit(1)
	}
	check(runner.Run(cfg))
}

func cmdStatus(cfg *config.Config) {
	fmt.Printf("Version:   %s\n", config.Version())
	fmt.Printf("Enrolled:  %t\n", cfg.Enrolled())
	fmt.Printf("Sequence:  %d\n", cfg.Sequence)
	fmt.Printf("Frontend:  %s\n", cfg.FEBaseURL)
	fmt.Printf("Backend:   %s\n", cfg.APIBaseURL)
	if dir, err := config.Dir(); err == nil {
		fmt.Printf("Config:    %s/agent.json\n", dir)
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "Unknown host"
	}
	return h
}

// osName returns a friendly OS label, e.g. "macOS 14.5".
func osName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			return "macOS " + strings.TrimSpace(string(out))
		}
		return "macOS"
	}
	return runtime.GOOS
}

func usage() {
	fmt.Println("Avora agent")
	fmt.Println("Usage:")
	fmt.Println("  avora-agent install    set up + run automatically at login (recommended)")
	fmt.Println("  avora-agent uninstall  remove auto-start")
	fmt.Println("  avora-agent enroll     link this machine to your Avora account")
	fmt.Println("  avora-agent run        start sampling + posting activity")
	fmt.Println("  avora-agent status     show enrollment / config")
}

// say reports success to whoever is watching: the terminal when there is one,
// otherwise a dialog. Without this a double-click install finishes in silence.
func say(message string) {
	if hasConsole {
		fmt.Println(message)
		return
	}
	notify.Dialog("Avora", message, false)
}

func check(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "error: "+err.Error())
	if !hasConsole {
		notify.Dialog("Avora — setup failed", err.Error(), true)
	}
	os.Exit(1)
}
