//go:build windows

package notify

import (
	"syscall"
	"unsafe"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
	procMessageBeep = user32.NewProc("MessageBeep")
)

const mbIconInformation = 0x00000040

// Ping plays the default system sound and shows a message box (non-blocking).
func Ping(message string) {
	text := message
	if text == "" {
		text = "Your manager pinged you on Avora."
	}
	go func() {
		procMessageBeep.Call(uintptr(0xFFFFFFFF))
		title, _ := syscall.UTF16PtrFromString("Avora")
		body, _ := syscall.UTF16PtrFromString(text)
		procMessageBoxW.Call(
			0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), mbIconInformation,
		)
	}()
}
