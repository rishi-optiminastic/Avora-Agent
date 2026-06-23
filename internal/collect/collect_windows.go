//go:build windows

package collect

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

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

const (
	processQueryLimitedInformation = 0x1000
	createNoWindow                 = 0x08000000
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// browserNames maps a foreground process exe (lower-case, no extension) to its
// display name. When the foreground app is one of these we read the active tab.
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

// Collect reads the foreground app, idle time, and — when the foreground app is a
// browser — the active tab URL + page title (via UI Automation).
func Collect() (Sample, error) {
	hwnd := foregroundWindow()
	app := processName(hwnd)
	s := Sample{ActiveWindow: app, IdleSeconds: idleSeconds()}
	if browser, ok := browserNames[strings.ToLower(app)]; ok && hwnd != 0 {
		s.Browser = browser
		s.PageTitle = cleanTitle(windowText(hwnd), browser)
		s.URL = browserURL(hwnd)
	}
	return s, nil
}

func foregroundWindow() uintptr {
	hwnd, _, _ := procGetForegroundWindow.Call()
	return hwnd
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

// cleanTitle strips the trailing " - <Browser>" the browser appends to its
// window title, leaving (roughly) the page title.
func cleanTitle(title, browser string) string {
	for _, suffix := range []string{" - " + browser, " — " + browser, " - " + browser + " "} {
		if i := strings.LastIndex(title, suffix); i >= 0 {
			return strings.TrimSpace(title[:i])
		}
	}
	return strings.TrimSpace(title)
}

// browserURL reads the address bar of the given browser window via UI Automation
// (PowerShell, since UIAutomationClient has no clean pure-Go binding). Best
// effort: returns "" on any failure, denial, or timeout, so it never blocks or
// regresses the rest of the sample.
func browserURL(hwnd uintptr) string {
	script := `$ErrorActionPreference='SilentlyContinue';` +
		`Add-Type -AssemblyName UIAutomationClient,UIAutomationTypes;` +
		`$h=[IntPtr]` + uitoa(hwnd) + `;` +
		`$root=[System.Windows.Automation.AutomationElement]::FromHandle($h);` +
		`if($root){` +
		`$cond=New-Object System.Windows.Automation.PropertyCondition(` +
		`[System.Windows.Automation.AutomationElement]::ControlTypeProperty,` +
		`[System.Windows.Automation.ControlType]::Edit);` +
		`$edits=$root.FindAll([System.Windows.Automation.TreeScope]::Descendants,$cond);` +
		`foreach($e in $edits){` +
		`$vp=$null;try{$vp=$e.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern)}catch{};` +
		`if($vp){$v=$vp.Current.Value;` +
		`if($v -and $v.Length -gt 0 -and $v -notmatch '\s' -and $v -match '\.'){Write-Output $v;break}}}}`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(
		ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// uitoa renders a window handle as a base-10 string for the PS script.
func uitoa(v uintptr) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
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
