//go:build windows

package singleton

import (
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	procCloseHandle  = kernel32.NewProc("CloseHandle")
)

const errorAlreadyExists = 183

// tryAcquire creates a named mutex in the user's session. Windows closes the
// handle when the process exits, however it exits, so it can never go stale.
func tryAcquire(name string) (func(), error) {
	mutexName, err := syscall.UTF16PtrFromString(`Local\AvoraAgent-` + name)
	if err != nil {
		return nil, err
	}
	handle, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(mutexName)))
	if handle == 0 {
		return nil, callErr
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == errorAlreadyExists {
		_, _, _ = procCloseHandle.Call(handle)
		return nil, ErrHeld
	}
	return func() { _, _, _ = procCloseHandle.Call(handle) }, nil
}
