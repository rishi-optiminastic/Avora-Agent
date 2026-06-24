//go:build windows

package collect

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Pure Win32 syscall — NO subprocess is ever spawned, so the agent loop can't pop
// a cmd/powershell window. Browser-URL capture is done in-process via UI
// Automation COM (see browser_windows.go), not by shelling out.
var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW       = user32.NewProc("GetWindowTextLengthW")
	procGetLastInputInfo           = user32.NewProc("GetLastInputInfo")
	procGetTickCount               = kernel32.NewProc("GetTickCount")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
)

const processQueryLimitedInformation = 0x1000

// browserNames maps a foreground exe (lower-case, no extension) → display name.
var browserNames = map[string]string{
	"chrome":   "Google Chrome",
	"msedge":   "Microsoft Edge",
	"brave":    "Brave",
	"vivaldi":  "Vivaldi",
	"opera":    "Opera",
	"arc":      "Arc",
	"chromium": "Chromium",
	"firefox":  "Firefox",
}

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// Collect reads the foreground app + idle time, and — for a browser — the active
// tab URL + page title (all in-process; no subprocess).
func Collect() (Sample, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	app := processName(hwnd)
	s := Sample{ActiveWindow: app, IdleSeconds: idleSeconds()}
	if browser, ok := browserNames[strings.ToLower(app)]; ok && hwnd != 0 {
		s.Browser = browser
		s.PageTitle = cleanTitle(windowText(hwnd), browser)
		s.URL = browserURL(hwnd)
	}
	return s, nil
}

// processName returns the foreground window's executable name (no extension).
func processName(hwnd uintptr) string {
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

// windowText returns the foreground window's title bar text.
func windowText(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	got, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:got])
}

// cleanTitle strips the trailing " - <Browser>" a browser appends to its window
// title, leaving roughly the page title.
func cleanTitle(title, browser string) string {
	for _, suffix := range []string{" - " + browser, " — " + browser} {
		if i := strings.LastIndex(title, suffix); i >= 0 {
			return strings.TrimSpace(title[:i])
		}
	}
	return strings.TrimSpace(title)
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
