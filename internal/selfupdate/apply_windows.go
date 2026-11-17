//go:build windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	createNoWindow   = 0x08000000
	detachedProcess  = 0x00000008
	newProcessGroup  = 0x00000200
	breakawayFromJob = 0x01000000
)

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
//
// The child must BREAK AWAY FROM THE JOB. Auto-start runs the agent as a
// Scheduled Task, and Task Scheduler puts the task's process in a job object
// that it tears down when the task instance ends — so a plain child inherited
// the job and was killed moments later by the parent's own os.Exit, leaving the
// machine with no agent until the task's next repetition. Not every job permits
// breakaway, so fall back to a plain detached start, and only exit once a child
// has actually started. Mirrors autostart.startDetached.
func restart(exe string) error {
	flagSets := []uint32{
		detachedProcess | newProcessGroup | breakawayFromJob,
		detachedProcess | newProcessGroup,
		createNoWindow,
	}
	var err error
	for _, flags := range flagSets {
		cmd := exec.Command(exe, "run")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if err = cmd.Start(); err == nil {
			os.Exit(0)
		}
	}
	return err
}
