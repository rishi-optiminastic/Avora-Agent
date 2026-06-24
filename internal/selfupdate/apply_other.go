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
// restart that keeps the same PID. Falls through to exit if exec fails.
func restart(exe string) {
	_ = syscall.Exec(exe, []string{exe, "run"}, os.Environ())
	os.Exit(0)
}
