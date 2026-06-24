//go:build windows

package notify

import (
	"syscall"
	"unsafe"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	winmm           = syscall.NewLazyDLL("winmm.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
	procMessageBeep = user32.NewProc("MessageBeep")
	procPlaySoundW  = winmm.NewProc("PlaySoundW")
)

const (
	mbIconInformation = 0x00000040
	sndAsync          = 0x0001     // SND_ASYNC — return immediately
	sndAlias          = 0x00010000 // SND_ALIAS — name is a system-sound alias
)

// playSound plays an audible notification through the audio device. The old
// MessageBeep(0xFFFFFFFF) hits the PC speaker, which is silent on most modern
// laptops — PlaySound("SystemAsterisk") goes through the real sound device.
// Falls back to MessageBeep if winmm/PlaySound isn't available.
func playSound() {
	if alias, err := syscall.UTF16PtrFromString("SystemAsterisk"); err == nil {
		if r, _, _ := procPlaySoundW.Call(
			uintptr(unsafe.Pointer(alias)), 0, uintptr(sndAlias|sndAsync),
		); r != 0 {
			return
		}
	}
	procMessageBeep.Call(uintptr(mbIconInformation))
}

// Ping plays an audible alert and shows a message box (non-blocking).
func Ping(message string) {
	text := message
	if text == "" {
		text = "Your manager pinged you on Avora."
	}
	go func() {
		playSound()
		title, _ := syscall.UTF16PtrFromString("Avora")
		body, _ := syscall.UTF16PtrFromString(text)
		procMessageBoxW.Call(
			0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), mbIconInformation,
		)
	}()
}
