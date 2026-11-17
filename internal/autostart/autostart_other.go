//go:build !darwin && !windows && !linux

package autostart

import (
	"errors"
	"os/exec"
)

// Enable is unsupported on this platform.
func Enable(_ string) error {
	return errors.New("auto-start is not supported on this platform")
}

// Disable is unsupported on this platform.
func Disable() error {
	return errors.New("auto-start is not supported on this platform")
}

// Ensure is a no-op where auto-start is unsupported.
func Ensure(string) (bool, error) { return false, nil }

// HideChild is a no-op where auto-start is unsupported.
func HideChild(*exec.Cmd) {}
