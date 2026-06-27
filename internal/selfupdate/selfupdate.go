// Package selfupdate keeps the agent current without anyone touching the
// employee's machine. It checks the latest published version (a tiny VERSION
// file on the GitHub release), and if newer downloads the matching binary, swaps
// it in place, and restarts. Everything is best-effort: any failure logs and
// the agent keeps running the version it has — an update never bricks it. The
// device token in ~/.avora is untouched, so updating never requires re-enrolling.
package selfupdate

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// assetName is the release asset for this platform — must match build-agent.sh.
func assetName() string {
	switch runtime.GOOS {
	case "windows":
		return "avora-agent.exe"
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "avora-agent-macos-arm64"
		}
		return "avora-agent-macos-intel"
	}
	return ""
}

// CheckAndApply updates the running agent to the latest release if there is one.
// On a successful swap it restarts (this call does not return). On anything else
// it returns and the caller carries on with the current binary.
func CheckAndApply(current, repo string) {
	asset := assetName()
	if repo == "" || current == "" || current == "dev" || asset == "" {
		return // self-update disabled (local/unconfigured build or unknown OS)
	}

	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	_ = os.Remove(exe + ".old") // leftover from a previous Windows swap

	base := "https://github.com/" + repo + "/releases/latest/download"
	latest, err := fetchText(base + "/VERSION")
	if err != nil || latest == "" || latest == current {
		return // up to date, or couldn't reach the release
	}

	newPath := exe + ".new"
	if err := download(base+"/"+asset, newPath); err != nil {
		_ = os.Remove(newPath)
		return
	}
	_ = os.Chmod(newPath, 0o755)
	if err := apply(exe, newPath); err != nil {
		_ = os.Remove(newPath)
		return
	}
	fmt.Printf("  ⬆️  updated %s → %s, restarting\n", current, latest)
	// On success restart() does not return (it replaces/relaunches the process).
	// If the relaunch fails, do NOT exit — keep the current process running so the
	// machine doesn't go dark. The new binary is already in place and will be
	// picked up on the next normal start (login or the autostart supervisor).
	if err := restart(exe); err != nil {
		fmt.Println("  warn: self-update relaunch failed, staying on current version: " + err.Error())
	}
}

func fetchText(url string) (string, error) {
	body, err := get(url, 20*time.Second)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, 256))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func download(url, dest string) error {
	body, err := get(url, 5*time.Minute)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(f, body); err != nil {
		return err
	}
	return f.Close()
}

// get does a GET that follows redirects (GitHub release downloads redirect to a
// CDN) and returns the body on a 2xx.
func get(url string, timeout time.Duration) (io.ReadCloser, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url) //nolint:gosec // URL is built from a baked-in repo
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}
