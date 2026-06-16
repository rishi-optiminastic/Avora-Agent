//go:build linux

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

func desktopPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "autostart", "avora-agent.desktop"), nil
}

// Enable writes an XDG autostart entry that launches `<execPath> run` at login.
func Enable(execPath string) error {
	path, err := desktopPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf(desktopTemplate, execPath)), 0o644)
}

// Disable removes the autostart entry.
func Disable() error {
	path, err := desktopPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

const desktopTemplate = `[Desktop Entry]
Type=Application
Name=Avora Agent
Exec=%s run
X-GNOME-Autostart-enabled=true
`
