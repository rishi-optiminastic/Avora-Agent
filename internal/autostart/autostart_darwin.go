//go:build darwin

// Package autostart registers the agent to run automatically at login.
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	_ = exec.Command("launchctl", "unload", path).Run() // ignore "not loaded"
	return exec.Command("launchctl", "load", "-w", path).Run()
}

// Disable unloads and removes the LaunchAgent.
func Disable() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
