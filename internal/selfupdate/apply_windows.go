//go:build windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// apply swaps the new binary in for the running one. A running .exe can't be
// overwritten on Windows, but it CAN be renamed — so move the current one aside
// (.old, cleaned up on the next start) and rename the new one into its place.
func apply(current, newPath string) error {
	old := current + ".old"
	_ = os.Remove(old)
	if err := os.Rename(current, old); err != nil {
		return err
	}
	if err := os.Rename(newPath, current); err != nil {
		_ = os.Rename(old, current) // roll back so we don't lose the agent
		return err
	}
	return nil
}

// restart launches the freshly-swapped binary (windowless) and exits, so the new
// version takes over. The autostart task still points at the same path. If the
// launch fails it returns the error WITHOUT exiting, so the caller can keep the
// current process alive instead of leaving the machine with no agent running.
func restart(exe string) error {
	cmd := exec.Command(exe, "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil // unreachable
}
