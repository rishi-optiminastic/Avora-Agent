//go:build windows

package autostart

import "os/exec"

const taskName = "AvoraAgent"

// Enable registers a logon-triggered Scheduled Task and starts it now.
func Enable(execPath string) error {
	err := exec.Command(
		"schtasks", "/Create", "/TN", taskName,
		"/TR", `"`+execPath+`" run`, "/SC", "ONLOGON", "/F",
	).Run()
	if err != nil {
		return err
	}
	_ = exec.Command("schtasks", "/Run", "/TN", taskName).Run() // start immediately
	return nil
}

// Disable removes the Scheduled Task.
func Disable() error {
	return exec.Command("schtasks", "/Delete", "/TN", taskName, "/F").Run()
}
