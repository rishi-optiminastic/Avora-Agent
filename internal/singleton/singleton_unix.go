//go:build !windows

package singleton

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// tryAcquire takes an exclusive, non-blocking flock on ~/.avora/<name>.lock.
// The kernel drops it when the process exits, however it exits, so a crash can
// never leave a stale lock behind.
func tryAcquire(name string) (func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".avora")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
