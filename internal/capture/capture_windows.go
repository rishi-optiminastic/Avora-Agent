//go:build windows

package capture

import (
	"bytes"
	"image"
	_ "image/jpeg" // register the JPEG decoder for DecodeConfig
	"os"
	"os/exec"
	"syscall"
)

// createNoWindow (CREATE_NO_WINDOW) stops Windows from spawning a console window
// for the PowerShell child. Without it a cmd window flashes on every capture —
// and gets caught in the screenshot itself.
const createNoWindow = 0x08000000

// Capture grabs the virtual screen via PowerShell + System.Drawing and returns
// the JPEG bytes. (Shelling out mirrors the macOS approach and avoids hand-rolled
// GDI syscalls.) Dimensions are read back from the encoded image.
func Capture() (Shot, error) {
	f, err := os.CreateTemp("", "avora-shot-*.jpg")
	if err != nil {
		return Shot{}, err
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	ps := `Add-Type -AssemblyName System.Windows.Forms,System.Drawing; ` +
		`$b=[System.Windows.Forms.SystemInformation]::VirtualScreen; ` +
		`$bmp=New-Object System.Drawing.Bitmap($b.Width,$b.Height); ` +
		`$g=[System.Drawing.Graphics]::FromImage($bmp); ` +
		`$g.CopyFromScreen($b.Location,[System.Drawing.Point]::Empty,$b.Size); ` +
		`$bmp.Save('` + path + `',[System.Drawing.Imaging.ImageFormat]::Jpeg); ` +
		`$g.Dispose(); $bmp.Dispose()`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Run(); err != nil {
		return Shot{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Shot{}, err
	}
	shot := Shot{JPEG: data}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		shot.Width, shot.Height = cfg.Width, cfg.Height
	}
	return shot, nil
}
