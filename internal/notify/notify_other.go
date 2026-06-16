//go:build !darwin && !windows

package notify

// Ping is a no-op on this platform (macOS and Windows are supported).
func Ping(_ string) {}
