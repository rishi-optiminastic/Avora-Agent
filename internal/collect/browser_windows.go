//go:build windows

package collect

import (
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// In-process UI Automation (COM) reader for the foreground browser's address-bar
// URL. No subprocess — so no console/popup ever (the PowerShell approach flashed
// a window). Out-params are heap-allocated (Go's heap is non-moving) and kept
// alive across each call, and the whole thing is wrapped in recover() + nil
// checks, so a COM hiccup degrades to "" instead of crashing the agent.

var (
	ole32                = syscall.NewLazyDLL("ole32.dll")
	oleaut32             = syscall.NewLazyDLL("oleaut32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procSysFreeString    = oleaut32.NewProc("SysFreeString")
)

const ptrSize = unsafe.Sizeof(uintptr(0))

type comGUID struct {
	a uint32
	b uint16
	c uint16
	d [8]byte
}

// VARIANT (24 bytes on amd64): the data union starts at offset 8.
type comVariant struct {
	vt  uint16
	_   uint16
	_   uint16
	_   uint16
	val uintptr
	_   uintptr
}

// CLSID_CUIAutomation {FF48DBA4-60EF-4201-AA87-54103EEF594E}
var clsidCUIAutomation = comGUID{
	0xff48dba4, 0x60ef, 0x4201, [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e},
}

// IID_IUIAutomation {30CBE57D-D9D0-452A-AB13-7AC5AC4825EE}
var iidIUIAutomation = comGUID{
	0x30cbe57d, 0xd9d0, 0x452a, [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee},
}

const (
	coinitMultithreaded  = 0x0
	clsctxInprocServer   = 0x1
	treeScopeDescendants = 0x4
	vtI4                 = 3
	vtBSTR               = 8

	uiaControlTypeProperty = 30003
	uiaValueValueProperty  = 30045
	uiaEditControlType     = 50004

	// Vtable indices (IUnknown occupies slots 0,1,2).
	idxRelease                 = 2
	idxElementFromHandle       = 6  // IUIAutomation
	idxCreatePropertyCondition = 23 // IUIAutomation
	idxFindFirst               = 5  // IUIAutomationElement
	idxGetCurrentPropertyValue = 10 // IUIAutomationElement
)

// COM interface pointers are held as unsafe.Pointer from the moment COM hands
// them over, never as uintptr. Converting a uintptr back into a pointer is the
// pattern `go vet` (rightly) cannot prove safe; keeping them typed as pointers
// means the only conversions left are pointer -> uintptr at the syscall
// boundary, which is always valid. They point at COM (non-Go) memory, so the
// garbage collector ignores them.

// method returns the function pointer for vtable slot `index` of a COM object.
func method(this unsafe.Pointer, index int) uintptr {
	vtbl := *(*unsafe.Pointer)(this)
	return *(*uintptr)(unsafe.Add(vtbl, uintptr(index)*ptrSize))
}

func comRelease(this unsafe.Pointer) {
	if this != nil {
		syscall.SyscallN(method(this, idxRelease), uintptr(this))
	}
}

// callOut invokes a vtable method whose LAST parameter is an out **pointer and
// returns that pointer (nil on failure). The out cell is heap-allocated so its
// address stays valid across the call.
func callOut(this unsafe.Pointer, index int, args ...uintptr) unsafe.Pointer {
	out := new(unsafe.Pointer)
	full := append([]uintptr{uintptr(this)}, args...)
	full = append(full, uintptr(unsafe.Pointer(out)))
	r, _, _ := syscall.SyscallN(method(this, index), full...)
	runtime.KeepAlive(out)
	if r != 0 {
		return nil
	}
	return *out
}

// uiaTimeout bounds one address-bar read. A healthy FindFirst answers in
// milliseconds; seconds means UI Automation is struggling and we would rather
// lose the URL than the agent.
const uiaTimeout = 5 * time.Second

// uiaBusy is a single-flight guard: at most one UIA probe may be outstanding.
var uiaBusy atomic.Bool

// browserURL reads the foreground browser's address bar, giving up after
// uiaTimeout.
//
// The read itself cannot be cancelled, so it runs on its own goroutine and we
// walk away from it. That matters because it used to run INLINE on the agent's
// main loop: FindFirst(TreeScope_Descendants) walks a Chromium window's whole
// accessibility tree, which is a documented place for UI Automation to block
// indefinitely, and when it did the entire agent froze — no samples, no
// screenshots, no pings — while the process sat there looking perfectly
// healthy. Neither Task Scheduler nor launchd restarts a process that has not
// exited, so that machine was off the fleet until someone rebooted it. Heavy
// browser users hit it; light ones never did.
//
// The single-flight guard is the other half: without it a wedged probe would be
// joined by a new one every 30s, each holding its own locked OS thread.
// Abandoning at most ONE thread is the price of keeping the agent alive. If the
// probe is still stuck we simply report no URL; activity and screenshots carry
// on as normal, and the next process start is clean.
func browserURL(hwnd uintptr) string {
	if hwnd == 0 {
		return ""
	}
	if !uiaBusy.CompareAndSwap(false, true) {
		return "" // an earlier probe has not come back; do not pile on
	}
	done := make(chan string, 1) // buffered: a late finisher must never block
	go func() {
		defer uiaBusy.Store(false)
		done <- readAddressBar(hwnd)
	}()
	select {
	case url := <-done:
		return url
	case <-time.After(uiaTimeout):
		return ""
	}
}

// readAddressBar is the blocking COM read. Only ever called from browserURL's
// goroutine, which owns the timeout.
func readAddressBar(hwnd uintptr) (result string) {
	if hwnd == 0 {
		return ""
	}
	defer func() { _ = recover() }()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded)
	if hr == 0 || hr == 1 { // S_OK / S_FALSE — we initialized it, so we balance it
		defer procCoUninitialize.Call()
	}

	uiaCell := new(unsafe.Pointer)
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(uiaCell)),
	)
	runtime.KeepAlive(uiaCell)
	uia := *uiaCell
	if r != 0 || uia == nil {
		return ""
	}
	defer comRelease(uia)

	root := callOut(uia, idxElementFromHandle, hwnd)
	if root == nil {
		return ""
	}
	defer comRelease(root)

	// Condition: ControlType == Edit (the address bar is an editable text field).
	cond := new(comVariant)
	cond.vt = vtI4
	cond.val = uiaEditControlType
	condition := callOut(uia, idxCreatePropertyCondition, uiaControlTypeProperty, uintptr(unsafe.Pointer(cond)))
	runtime.KeepAlive(cond)
	if condition == nil {
		return ""
	}
	defer comRelease(condition)

	edit := callOut(root, idxFindFirst, treeScopeDescendants, uintptr(condition))
	if edit == nil {
		return ""
	}
	defer comRelease(edit)

	value := new(comVariant)
	rv, _, _ := syscall.SyscallN(
		method(edit, idxGetCurrentPropertyValue),
		uintptr(edit), uiaValueValueProperty, uintptr(unsafe.Pointer(value)),
	)
	runtime.KeepAlive(value)
	if rv != 0 || value.vt != vtBSTR || value.val == 0 {
		return ""
	}
	// For VT_BSTR the union holds a pointer. Read the field's memory AS a
	// pointer (a *uintptr -> *unsafe.Pointer reinterpretation) rather than
	// converting the integer value back into one.
	bstr := *(*unsafe.Pointer)(unsafe.Pointer(&value.val))
	url := bstrToString(bstr)
	procSysFreeString.Call(uintptr(bstr))
	return strings.TrimSpace(url)
}

// bstrToString reads a null-terminated UTF-16 BSTR (COM memory) into a string.
func bstrToString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	buf := make([]uint16, 0, 256)
	for i := 0; i < 8192; i++ {
		c := *(*uint16)(unsafe.Add(p, uintptr(i)*2))
		if c == 0 {
			break
		}
		buf = append(buf, c)
	}
	return string(utf16.Decode(buf))
}
