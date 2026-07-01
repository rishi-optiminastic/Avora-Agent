//go:build windows

package capture

import (
	"bytes"
	"image"
	"image/jpeg"
	"sync"
	"syscall"
	"unsafe"
)

const (
	// maxDimension caps the longest side of the COMBINED virtual desktop. Kept
	// high so a multi-monitor desktop keeps enough per-screen resolution for OCR
	// to read small code/UI text (the worker crops + upscales each monitor).
	maxDimension = 3200
	jpegQuality  = 90
)

// Win32 / GDI bindings. We capture the screen entirely in-process with BitBlt —
// no PowerShell child, so there is NO console window that could flash or get
// caught inside the screenshot (the bug the shell-out approach kept hitting).
var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBM  = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procBitBlt              = gdi32.NewProc("BitBlt")
	procGetDIBits           = gdi32.NewProc("GetDIBits")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
	smCXScreen        = 0
	smCYScreen        = 1

	srcCopy    = 0x00CC0020
	captureBlt = 0x40000000
	biRGB      = 0
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type winRect struct {
	Left, Top, Right, Bottom int32
}

func metric(i int) int {
	v, _, _ := procGetSystemMetrics.Call(uintptr(i))
	return int(int32(v))
}

// The EnumDisplayMonitors callback is allocated ONCE for the process lifetime —
// syscall.NewCallback registrations are never freed and are capped, so a fresh
// one per capture (every few minutes) would eventually exhaust the limit. The
// agent captures serially, so a package-level slice guarded by a mutex is safe.
var (
	enumMu       sync.Mutex
	enumCollect  []winRect
	enumCallback = syscall.NewCallback(func(_, _, lprc, _ uintptr) uintptr {
		enumCollect = append(enumCollect, *(*winRect)(unsafe.Pointer(lprc)))
		return 1 // continue enumeration
	})
)

// enumMonitors returns each physical monitor's rectangle in virtual-desktop
// coordinates (best-effort; empty on failure → the whole image is one screen).
func enumMonitors() []winRect {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumCollect = nil
	procEnumDisplayMonitors.Call(0, 0, enumCallback, 0)
	out := make([]winRect, len(enumCollect))
	copy(out, enumCollect)
	return out
}

// Capture grabs the whole virtual screen via GDI BitBlt, converts the BGRA
// device bits to RGBA, downscales (area averaging) to maxDimension, and returns
// a JPEG plus per-monitor rectangles within it.
func Capture() (Shot, error) {
	x := metric(smXVirtualScreen)
	y := metric(smYVirtualScreen)
	w := metric(smCXVirtualScreen)
	h := metric(smCYVirtualScreen)
	if w <= 0 || h <= 0 { // fall back to the primary monitor
		x, y = 0, 0
		w, h = metric(smCXScreen), metric(smCYScreen)
	}
	if w <= 0 || h <= 0 {
		return Shot{}, syscall.EINVAL
	}

	// Monitor rects relative to the captured origin (the virtual-screen top-left),
	// so they index into the combined image. Negative/odd offsets become 0-based.
	rects := make([]Rect, 0, 4)
	for _, m := range enumMonitors() {
		rects = append(rects, Rect{
			X: int(m.Left) - x,
			Y: int(m.Top) - y,
			W: int(m.Right - m.Left),
			H: int(m.Bottom - m.Top),
		})
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return Shot{}, syscall.EINVAL
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return Shot{}, syscall.EINVAL
	}
	defer procDeleteDC.Call(memDC)

	bmp, _, _ := procCreateCompatibleBM.Call(screenDC, uintptr(w), uintptr(h))
	if bmp == 0 {
		return Shot{}, syscall.EINVAL
	}
	defer procDeleteObject.Call(bmp)

	old, _, _ := procSelectObject.Call(memDC, bmp)
	ret, _, _ := procBitBlt.Call(
		memDC, 0, 0, uintptr(w), uintptr(h), screenDC, uintptr(x), uintptr(y), srcCopy|captureBlt,
	)
	procSelectObject.Call(memDC, old) // deselect before GetDIBits
	if ret == 0 {
		return Shot{}, syscall.EINVAL
	}

	bi := bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      int32(-h), // negative → top-down rows (matches image.RGBA)
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}
	buf := make([]byte, w*h*4)
	got, _, _ := procGetDIBits.Call(
		memDC, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0,
	)
	if got == 0 {
		return Shot{}, syscall.EINVAL
	}

	// Device bits are BGRA; rewrite in place to RGBA with opaque alpha.
	for i := 0; i+3 < len(buf); i += 4 {
		buf[i], buf[i+2] = buf[i+2], buf[i]
		buf[i+3] = 0xff
	}
	src := &image.RGBA{Pix: buf, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	out, scale := downscaleArea(src, maxDimension)
	rects = scaleRects(rects, scale)

	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, out, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return Shot{}, err
	}
	return Shot{
		JPEG:     jpg.Bytes(),
		Width:    out.Rect.Dx(),
		Height:   out.Rect.Dy(),
		Monitors: rects,
	}, nil
}
