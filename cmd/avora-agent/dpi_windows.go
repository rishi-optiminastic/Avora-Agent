//go:build windows

package main

import "syscall"

// Windows reports SM_CXVIRTUALSCREEN and friends in LOGICAL pixels to a process
// that has not declared DPI awareness — scaled down by the display's scale
// factor. BitBlt, meanwhile, always copies PHYSICAL pixels. On a 1920x1080
// laptop at the Windows default 150% scaling the agent therefore asked for a
// 1280x720 rectangle and got the top-left 1280x720 of the real screen: a
// screenshot silently cropped to two-thirds of the desktop, on exactly the
// machines that use scaling and nowhere else.
//
// Declaring awareness makes those metrics physical, which fixes both the capture
// size and the per-monitor rectangles the OCR worker crops with.
//
// Per-monitor rather than system-aware on purpose: a laptop with an external
// display routinely runs two different scale factors, and system awareness would
// fix the built-in screen while leaving the external one wrong.
var (
	dpiUser32  = syscall.NewLazyDLL("user32.dll")
	dpiShcore  = syscall.NewLazyDLL("shcore.dll")
	procSetCtx = dpiUser32.NewProc("SetProcessDpiAwarenessContext")
	procSetOld = dpiUser32.NewProc("SetProcessDPIAware")
	procSetSh  = dpiShcore.NewProc("SetProcessDpiAwareness")
)

// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is the handle value -4.
var perMonitorAwareV2 = ^uintptr(3)

// processPerMonitorDPIAware is PROCESS_PER_MONITOR_DPI_AWARE for shcore.
const processPerMonitorDPIAware = 2

// setDPIAware declares this process DPI-aware, newest API first.
//
// Must run before anything creates a window or grabs a device context —
// awareness is fixed for the process at first use — so main calls it first.
// Every call is best-effort: on a Windows too old for one entry point the DLL
// proc simply is not found, and we fall through to the next. Failing all three
// leaves the old cropped-capture behaviour rather than stopping the agent.
func setDPIAware() {
	if procSetCtx.Find() == nil {
		if r, _, _ := procSetCtx.Call(perMonitorAwareV2); r != 0 {
			return // Windows 10 1703+
		}
	}
	if procSetSh.Find() == nil {
		if r, _, _ := procSetSh.Call(uintptr(processPerMonitorDPIAware)); r == 0 {
			return // Windows 8.1+ — S_OK is 0 here, unlike the BOOL calls
		}
	}
	if procSetOld.Find() == nil {
		procSetOld.Call() // Vista+: system-aware only, but better than cropped
	}
}
