//go:build windows

package notify

import (
	"syscall"
	"unsafe"
)

const (
	mbIconError = 0x00000010
	mbOK        = 0x00000000
)

// Dialog shows a modal message box and BLOCKS until it is dismissed.
//
// Blocking is the point: the agent is a GUI-subsystem binary, so after a
// double-click there is no console to print to and no shell to hold the process
// open. A non-blocking box would vanish the moment main returned, which is
// exactly how "double-click does nothing" looks to the person doing it.
func Dialog(title, message string, isError bool) {
	icon := uintptr(mbIconInformation)
	if isError {
		icon = mbIconError
	}
	head, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	body, err := syscall.UTF16PtrFromString(message)
	if err != nil {
		return
	}
	procMessageBoxW.Call(
		0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(head)), icon|mbOK,
	)
}
