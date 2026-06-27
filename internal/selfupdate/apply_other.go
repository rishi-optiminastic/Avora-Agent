//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

// apply replaces the running binary. On Unix a file can be renamed over even
// while it's executing (the running process keeps the old inode), so this is a
// single atomic rename.
func apply(current, newPath string) error {
	return os.Rename(newPath, current)
}

// restart re-execs the process image with the new binary — a clean in-place
// restart that keeps the same PID. On success it does not return. If exec fails
// it returns the error so the caller keeps the current process running rather
// than exiting and relying on a supervisor that may not exist (e.g. Linux XDG
// autostart has no KeepAlive).
func restart(exe string) error {
	if err := syscall.Exec(exe, []string{exe, "run"}, os.Environ()); err != nil {
		return err
	}
	return nil // unreachable on success
}
