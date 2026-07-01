//go:build darwin

package capture

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
)

const (
	// maxDimension caps the longest side of the COMBINED (all-monitors) image.
	// Kept high so each monitor keeps enough resolution for OCR to read small
	// code/UI text; the per-monitor OCR crop is upscaled again server-side.
	maxDimension = 3200
	jpegQuality  = 90
	// Capture up to this many displays. `screencapture` writes one file per
	// display in order; extra paths for absent displays are left empty.
	maxDisplays = 4
)

// Capture grabs EVERY display via `screencapture` (one file per screen),
// composes them side by side into one image, downscales with area averaging,
// and returns JPEG bytes + per-monitor rectangles. Needs macOS Screen Recording
// permission. Previously only the main display was captured, so work on a second
// monitor was invisible — this fixes that.
func Capture() (Shot, error) {
	paths := make([]string, 0, maxDisplays)
	for i := 0; i < maxDisplays; i++ {
		f, err := os.CreateTemp("", "avora-shot-*.jpg")
		if err != nil {
			return Shot{}, err
		}
		name := f.Name()
		_ = f.Close()
		defer func(p string) { _ = os.Remove(p) }(name)
		paths = append(paths, name)
	}

	// -x: silent (no shutter sound), -t jpg: JPEG, one file per display.
	args := append([]string{"-x", "-t", "jpg"}, paths...)
	if err := exec.Command("screencapture", args...).Run(); err != nil {
		return Shot{}, err
	}

	var imgs []image.Image
	for _, p := range paths {
		info, err := os.Stat(p) // absent displays leave the temp file empty
		if err != nil || info.Size() == 0 {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		im, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		imgs = append(imgs, im)
	}
	if len(imgs) == 0 {
		return Shot{}, errors.New("screencapture produced no images")
	}

	canvas, rects := composeHorizontal(imgs)
	out, scale := downscaleArea(canvas, maxDimension)
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
