//go:build darwin

// Package autostart registers the agent to run automatically at login.
package autostart

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const label = "com.avora.agent"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// Enable installs a LaunchAgent that runs `<execPath> run` at login (and starts
// it now via launchctl load).
func Enable(execPath string) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(plistTemplate, label, execPath)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	_ = launchctl("unload", path) // ignore "not loaded"
	return launchctl("load", "-w", path)
}

// Disable unloads and removes the LaunchAgent.
func Disable() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	_ = launchctl("unload", path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// launchctlTimeout bounds a launchctl call. It normally answers instantly, but
// it talks to launchd over IPC and can block when launchd is busy — and with no
// bound that hung the installer with nothing on screen to explain it.
const launchctlTimeout = 30 * time.Second

func launchctl(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), launchctlTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "launchctl", args...)
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict>
</plist>
`
