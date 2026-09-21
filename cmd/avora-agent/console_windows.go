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
// is one, and reports whether it found one. The agent ships as a GUI-subsystem
// binary (-H windowsgui) so it never owns a console window — that's what kept a
// black cmd window open at login. When run from a terminal (install/enroll/
// status) we still want output, so we attach to the parent console.
//
// A false return means nobody can see stdout: launched from Explorer by a
// double-click, or started by the login task. The caller talks in dialogs
// instead, because printing into the void is how a double-click came to look
// like nothing happened at all.
func attachParentConsole() bool {
	r, _, _ := procAttachConsole.Call(uintptr(attachParentProcess))
	if r == 0 {
		return false
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
	}
	return true
}
