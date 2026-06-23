//go:build windows

package capture

import (
	"bytes"
	"image"
	"image/jpeg"
	"syscall"
	"unsafe"
)

// maxDimension caps the longest side. Kept high (not 1600) so OCR can read small
// code/UI text — a multi-monitor desktop squished to 1600px is unreadable to
// Tesseract, which starves the EOD context. ~2560px JPEG ≈ 0.4–1.5 MB, well under
// the 15 MB ingest cap.
const maxDimension = 2560

// Win32 / GDI bindings. We capture the screen entirely in-process with BitBlt —
// no PowerShell child, so there is NO console window that could flash or get
// caught inside the screenshot (the bug the shell-out approach kept hitting).
var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBM = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procGetDIBits          = gdi32.NewProc("GetDIBits")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
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

func metric(i int) int {
	v, _, _ := procGetSystemMetrics.Call(uintptr(i))
	return int(int32(v))
}

// Capture grabs the whole virtual screen via GDI BitBlt, converts the BGRA
// device bits to RGBA, downscales to maxDimension, and returns a JPEG.
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
	out := downscale(src, maxDimension)

	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, out, &jpeg.Options{Quality: 78}); err != nil {
		return Shot{}, err
	}
	return Shot{JPEG: jpg.Bytes(), Width: out.Rect.Dx(), Height: out.Rect.Dy()}, nil
}

// downscale shrinks src so its longest side is <= max (nearest-neighbor, no
// deps). Returns src unchanged when it already fits.
func downscale(src *image.RGBA, max int) *image.RGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	longest := w
	if h > w {
		longest = h
	}
	if longest <= max {
		return src
	}
	scale := float64(max) / float64(longest)
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for yy := 0; yy < nh; yy++ {
		sy := int(float64(yy) / scale)
		if sy >= h {
			sy = h - 1
		}
		for xx := 0; xx < nw; xx++ {
			sx := int(float64(xx) / scale)
			if sx >= w {
				sx = w - 1
			}
			di := dst.PixOffset(xx, yy)
			si := src.PixOffset(sx, sy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}
