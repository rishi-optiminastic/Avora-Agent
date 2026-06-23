//go:build windows

package collect

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// All of this is pure Win32 syscall — NO subprocess is ever spawned, so the
// agent loop can't pop a cmd/powershell window. (Browser-URL capture used to
// shell out to PowerShell for UI Automation; that was removed because the hidden
// spawn still flashed a window. Restoring browsing on Windows needs in-process
// COM UI Automation, not a subprocess.)
var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procGetLastInputInfo           = user32.NewProc("GetLastInputInfo")
	procGetTickCount               = kernel32.NewProc("GetTickCount")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
)

const processQueryLimitedInformation = 0x1000

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// Collect reads the foreground app and idle time via the Win32 API.
func Collect() (Sample, error) {
	return Sample{ActiveWindow: frontmostApp(), IdleSeconds: idleSeconds()}, nil
}

// frontmostApp returns the foreground process's executable name (no extension).
func frontmostApp() string {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return ""
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}
	handle, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return ""
	}
	defer procCloseHandle.Call(handle)

	buf := make([]uint16, 260)
	size := uint32(len(buf))
	ret, _, _ := procQueryFullProcessImageNameW.Call(
		handle, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
	)
	if ret == 0 {
		return ""
	}
	base := filepath.Base(syscall.UTF16ToString(buf[:size]))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// idleSeconds derives idle time from the last input event (both DWORDs wrap at
// ~49 days, so the uint32 subtraction stays correct across a wrap).
func idleSeconds() int {
	info := lastInputInfo{}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if ret, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info))); ret == 0 {
		return 0
	}
	tick, _, _ := procGetTickCount.Call()
	return int((uint32(tick) - info.dwTime) / 1000)
}
