//go:build windows

package autostart

import (
	"os/exec"
	"syscall"
)

const (
	runKey    = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "AvoraAgent"
	// DETACHED_PROCESS — the started agent has no console and survives the
	// terminal that launched it closing.
	detachedProcess = 0x00000008
)

// Enable registers the agent to launch at logon via the per-user Run key (no
// admin required, unlike a Scheduled Task) and starts it now, detached.
func Enable(execPath string) error {
	if err := exec.Command(
		"reg", "add", runKey, "/v", valueName, "/t", "REG_SZ",
		"/d", `"`+execPath+`" run`, "/f",
	).Run(); err != nil {
		return err
	}
	cmd := exec.Command(execPath, "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess}
	return cmd.Start()
}

// Disable removes the Run-key entry.
func Disable() error {
	return exec.Command("reg", "delete", runKey, "/v", valueName, "/f").Run()
}
