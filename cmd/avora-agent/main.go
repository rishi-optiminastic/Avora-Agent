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
	"avora-agent/internal/runner"
)

func main() {
	// Built with -H windowsgui (no console window, so the login Run-key
	// auto-start shows nothing). When a user runs a command from a terminal,
	// this re-attaches stdout so output is still visible. No-op off Windows.
	attachParentConsole()

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cfg, err := config.Load()
	check(err)

	switch os.Args[1] {
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
		fmt.Printf("Avora is connected. Start it manually with:\n  %q run\n", target)
		return
	}
	fmt.Println("Avora is set up — it's running now and will start automatically at login.")
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
	if err := os.WriteFile(target, data, 0o755); err != nil {
		return "", err
	}
	return target, nil
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

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}
