//go:build windows

package collect

import (
	"runtime"
	"strings"
	"syscall"
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

// method returns the function pointer for vtable slot `index` of a COM object.
func method(this uintptr, index int) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(this)) //nolint:govet // COM object (foreign memory)
	return *(*uintptr)(unsafe.Pointer(vtbl + uintptr(index)*ptrSize))
}

func comRelease(this uintptr) {
	if this != 0 {
		syscall.SyscallN(method(this, idxRelease), this)
	}
}

// callOut invokes a vtable method whose LAST parameter is an out **pointer and
// returns that pointer (0 on failure). The out cell is heap-allocated so its
// address stays valid across the call.
func callOut(this uintptr, index int, args ...uintptr) uintptr {
	out := new(uintptr)
	full := append([]uintptr{this}, args...)
	full = append(full, uintptr(unsafe.Pointer(out)))
	r, _, _ := syscall.SyscallN(method(this, index), full...)
	runtime.KeepAlive(out)
	if r != 0 {
		return 0
	}
	return *out
}

// browserURL returns the active tab URL of the foreground browser window, or ""
// on any failure. Pure in-process COM — no child process, no window.
func browserURL(hwnd uintptr) (result string) {
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

	uiaCell := new(uintptr)
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(uiaCell)),
	)
	runtime.KeepAlive(uiaCell)
	uia := *uiaCell
	if r != 0 || uia == 0 {
		return ""
	}
	defer comRelease(uia)

	root := callOut(uia, idxElementFromHandle, hwnd)
	if root == 0 {
		return ""
	}
	defer comRelease(root)

	// Condition: ControlType == Edit (the address bar is an editable text field).
	cond := new(comVariant)
	cond.vt = vtI4
	cond.val = uiaEditControlType
	condition := callOut(uia, idxCreatePropertyCondition, uiaControlTypeProperty, uintptr(unsafe.Pointer(cond)))
	runtime.KeepAlive(cond)
	if condition == 0 {
		return ""
	}
	defer comRelease(condition)

	edit := callOut(root, idxFindFirst, treeScopeDescendants, condition)
	if edit == 0 {
		return ""
	}
	defer comRelease(edit)

	value := new(comVariant)
	rv, _, _ := syscall.SyscallN(
		method(edit, idxGetCurrentPropertyValue),
		edit, uiaValueValueProperty, uintptr(unsafe.Pointer(value)),
	)
	runtime.KeepAlive(value)
	if rv != 0 || value.vt != vtBSTR || value.val == 0 {
		return ""
	}
	url := bstrToString(value.val)
	procSysFreeString.Call(value.val)
	return strings.TrimSpace(url)
}

// bstrToString reads a null-terminated UTF-16 BSTR (COM memory) into a string.
func bstrToString(p uintptr) string {
	if p == 0 {
		return ""
	}
	buf := make([]uint16, 0, 256)
	for i := 0; i < 8192; i++ {
		c := *(*uint16)(unsafe.Pointer(p + uintptr(i)*2)) //nolint:govet // BSTR (foreign memory)
		if c == 0 {
			break
		}
		buf = append(buf, c)
	}
	return string(utf16.Decode(buf))
}
