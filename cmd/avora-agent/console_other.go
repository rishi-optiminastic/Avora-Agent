//go:build !windows

package main

// attachParentConsole is Windows-only; elsewhere the process already has its
// stdio wired to the launching terminal, so there is always somewhere to print.
func attachParentConsole() bool { return true }
