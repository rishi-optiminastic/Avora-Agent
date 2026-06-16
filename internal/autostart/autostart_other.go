//go:build !darwin && !windows && !linux

package autostart

import "errors"

// Enable is unsupported on this platform.
func Enable(_ string) error {
	return errors.New("auto-start is not supported on this platform")
}

// Disable is unsupported on this platform.
func Disable() error {
	return errors.New("auto-start is not supported on this platform")
}
