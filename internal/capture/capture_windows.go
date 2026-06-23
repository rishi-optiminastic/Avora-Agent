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

// Capture grabs the virtual screen via PowerShell + System.Drawing, downscales
// the longest side to ~1600px (HighQualityBicubic) and saves JPEG at quality 65.
// Downscaling matters: a raw multi-monitor capture can be many megabytes and get
// rejected by the ingest size cap (which is why captures were silently failing).
// Dimensions are read back from the encoded image.
func Capture() (Shot, error) {
	f, err := os.CreateTemp("", "avora-shot-*.jpg")
	if err != nil {
		return Shot{}, err
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	ps := `$ErrorActionPreference='Stop';` +
		`Add-Type -AssemblyName System.Windows.Forms,System.Drawing;` +
		`$vs=[System.Windows.Forms.SystemInformation]::VirtualScreen;` +
		`$src=New-Object System.Drawing.Bitmap($vs.Width,$vs.Height);` +
		`$g=[System.Drawing.Graphics]::FromImage($src);` +
		`$g.CopyFromScreen($vs.Location,[System.Drawing.Point]::Empty,$vs.Size);` +
		`$g.Dispose();` +
		`$max=1600.0;` +
		`$scale=[Math]::Min(1.0,$max/[Math]::Max($vs.Width,$vs.Height));` +
		`$nw=[Math]::Max(1,[int]($vs.Width*$scale));` +
		`$nh=[Math]::Max(1,[int]($vs.Height*$scale));` +
		`$dst=New-Object System.Drawing.Bitmap($nw,$nh);` +
		`$dg=[System.Drawing.Graphics]::FromImage($dst);` +
		`$dg.InterpolationMode=[System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic;` +
		`$dg.DrawImage($src,0,0,$nw,$nh);` +
		`$dg.Dispose();$src.Dispose();` +
		`$enc=[System.Drawing.Imaging.ImageCodecInfo]::GetImageEncoders()|Where-Object{$_.MimeType -eq 'image/jpeg'};` +
		`$ep=New-Object System.Drawing.Imaging.EncoderParameters(1);` +
		`$ep.Param[0]=New-Object System.Drawing.Imaging.EncoderParameter([System.Drawing.Imaging.Encoder]::Quality,[int64]65);` +
		`$dst.Save('` + path + `',$enc,$ep);` +
		`$dst.Dispose()`
	cmd := exec.Command(
		"powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps,
	)
	// CREATE_NO_WINDOW alone is unreliable for powershell.exe — pair it with
	// HideWindow (STARTF_USESHOWWINDOW + SW_HIDE) so no console flashes (and so it
	// never lands inside the screenshot).
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
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
