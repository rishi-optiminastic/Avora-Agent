//go:build darwin

package capture

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// maxDimension caps the longest side so uploads stay small (~100–250 KB JPEG).
const maxDimension = "1280"

// Capture grabs the main display via `screencapture`, downscales it with `sips`,
// and returns JPEG bytes + dimensions. Needs macOS Screen Recording permission.
func Capture() (Shot, error) {
	f, err := os.CreateTemp("", "avora-shot-*.jpg")
	if err != nil {
		return Shot{}, err
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	// -x: silent (no shutter sound), -t jpg: JPEG, main display.
	if err := exec.Command("screencapture", "-x", "-t", "jpg", path).Run(); err != nil {
		return Shot{}, err
	}
	_ = exec.Command("sips", "-Z", maxDimension, path).Run() // downscale in place

	data, err := os.ReadFile(path)
	if err != nil {
		return Shot{}, err
	}
	w, h := dimensions(path)
	return Shot{JPEG: data, Width: w, Height: h}, nil
}

func dimensions(path string) (int, int) {
	out, err := exec.Command("sips", "-g", "pixelWidth", "-g", "pixelHeight", path).Output()
	if err != nil {
		return 0, 0
	}
	var w, h int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "pixelWidth":
			w, _ = strconv.Atoi(fields[1])
		case "pixelHeight":
			h, _ = strconv.Atoi(fields[1])
		}
	}
	return w, h
}
