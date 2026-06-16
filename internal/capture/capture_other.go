//go:build !darwin && !windows

package capture

import "errors"

// Capture is unimplemented on this platform (macOS and Windows are supported).
func Capture() (Shot, error) {
	return Shot{}, errors.New("screenshot capture is only implemented on macOS and Windows")
}
