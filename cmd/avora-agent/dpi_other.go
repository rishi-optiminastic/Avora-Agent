//go:build !windows

package main

// setDPIAware is Windows-only. macOS hands back the full backing-store
// resolution already, and the Linux collectors are stubs.
func setDPIAware() {}
