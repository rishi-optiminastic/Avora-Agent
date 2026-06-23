//go:build windows

package main

import (
	"os"
	"syscall"
)

var (
	modkernel32       = syscall.NewLazyDLL("kernel32.dll")
	procAttachConsole = modkernel32.NewProc("AttachConsole")
)

// attachParentProcess (ATTACH_PARENT_PROCESS) — attach to the console of the
// process that launched us, if any. It's the DWORD (uint32) value -1.
const attachParentProcess = ^uint32(0)

// attachParentConsole wires stdout/stderr to the launching terminal when there
// is one. The agent ships as a GUI-subsystem binary (-H windowsgui) so it never
// owns a console window — that's what kept a black cmd window open at login.
// When run interactively (install/enroll/status) we still want output, so we
// attach to the parent console; auto-start at login has no parent console, so
// AttachConsole fails and the agent stays silently windowless.
func attachParentConsole() {
	r, _, _ := procAttachConsole.Call(uintptr(attachParentProcess))
	if r == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
	}
}
