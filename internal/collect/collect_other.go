//go:build !darwin && !windows

package collect

import "errors"

// Collect is unimplemented on this platform (macOS and Windows are supported).
func Collect() (Sample, error) {
	return Sample{}, errors.New("activity collection is only implemented on macOS and Windows")
}
