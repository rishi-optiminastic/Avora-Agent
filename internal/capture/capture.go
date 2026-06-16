// Package capture grabs a downscaled screenshot of the screen. The actual
// capture is platform-specific (see capture_darwin.go).
package capture

// Shot is a captured screen image plus its pixel dimensions.
type Shot struct {
	JPEG   []byte
	Width  int
	Height int
}
