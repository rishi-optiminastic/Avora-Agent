// Package capture grabs a downscaled screenshot of the screen. The actual
// capture is platform-specific (see capture_darwin.go / capture_windows.go).
//
// Multi-monitor desktops are captured into ONE image (monitors laid side by
// side). `Shot.Monitors` records each monitor's rectangle within that image so
// the backend OCR worker can crop and OCR each screen separately — far more
// accurate than running OCR over two unrelated layouts at once.
package capture

// Rect is one monitor's rectangle within the combined capture, in the stored
// image's pixels (after any downscale).
type Rect struct {
	X, Y, W, H int
}

// Shot is a captured screen image plus its pixel dimensions and per-monitor
// rectangles (one entry per physical display; empty when unknown).
type Shot struct {
	JPEG     []byte
	Width    int
	Height   int
	Monitors []Rect
}
