//go:build darwin

package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"strings"
	"time"
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
	// captureTimeout bounds the screencapture child. Four displays take a couple
	// of seconds; anything near this means it is stuck.
	//
	// It had no bound at all, on the agent's main loop. screencapture can block
	// indefinitely when Screen Recording permission has not been granted, when
	// the display is asleep or locked, or behind a secure-input window — and a
	// blocked capture froze the whole agent, which neither launchd nor Task
	// Scheduler can detect because the process never exits.
	captureTimeout = 20 * time.Second
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
	ctx, cancel := context.WithTimeout(context.Background(), captureTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "screencapture", args...)
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		// Without Screen Recording permission screencapture fails with "could
		// not create image from display".
		if strings.Contains(strings.ToLower(string(out)), "could not create image") {
			return Shot{}, fmt.Errorf("%w: %v", ErrPermissionDenied, err)
		}
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
