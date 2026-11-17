// Package selfupdate keeps the agent current without anyone touching the
// employee's machine. It checks the latest published version (a tiny VERSION
// file on the GitHub release), and if newer downloads the matching binary, swaps
// it in place, and restarts. Everything is best-effort: any failure logs and
// the agent keeps running the version it has — an update never bricks it. The
// device token in ~/.avora is untouched, so updating never requires re-enrolling.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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
	// Integrity first: the asset must match the SHA256SUMS published in the
	// same release, or it is never even executed.
	if err := downloadVerified(base, asset, newPath); err != nil {
		fmt.Println("  warn: self-update refused, keeping current version: " + err.Error())
		_ = os.Remove(newPath)
		return
	}
	_ = os.Chmod(newPath, 0o755)
	// Prove the download actually runs on THIS machine before it replaces the
	// agent. apply() renames the new file over the live one, so a binary that
	// cannot start (truncated upload, wrong architecture, a release asset built
	// wrong, an antivirus that mangles it on write) used to be installed anyway
	// — and from then on auto-start relaunched a broken exe every few minutes,
	// forever, with no rollback. That is a permanently dead machine, shipped by
	// an update. A binary that cannot even print its own status is not one we
	// are willing to swap in.
	if err := verify(newPath); err != nil {
		fmt.Println("  warn: downloaded agent failed its smoke test, keeping current version: " + err.Error())
		_ = os.Remove(newPath)
		return
	}
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

// verify runs the freshly downloaded binary once and requires a clean exit.
// `status` only reads the config and prints, so it is side-effect free and
// exercises the one thing we need to know: that this file starts and runs here.
func verify(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, "status").Run()
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

// checksumsFile is the release asset listing "<sha256-hex>  <asset>" per line,
// written by build-agent.sh.
const (
	checksumsFile     = "SHA256SUMS"
	maxChecksumsBytes = 64 << 10
)

// downloadVerified fetches SHA256SUMS and the asset from base and writes the
// asset to dest only if its SHA-256 matches. A missing checksum file, a
// missing line for the asset, or a mismatch are all refusals.
func downloadVerified(base, asset, dest string) error {
	sums, err := fetchLimited(base+"/"+checksumsFile, maxChecksumsBytes)
	if err != nil {
		return fmt.Errorf("checksum file unavailable: %w", err)
	}
	want, err := expectedSum(sums, asset)
	if err != nil {
		return err
	}
	got, err := download(base+"/"+asset, dest)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", asset, got, want)
	}
	return nil
}

// expectedSum finds asset's hash in a sha256sum-format file.
func expectedSum(sums []byte, asset string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*") // binary-mode marker
		if name == asset && len(fields[0]) == sha256.Size*2 {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in %s", asset, checksumsFile)
}

// download writes url to dest and returns the hex SHA-256 of what it wrote.
func download(url, dest string) (string, error) {
	body, err := get(url, 5*time.Minute)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), body); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fetchLimited(url string, limit int64) ([]byte, error) {
	body, err := get(url, 20*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(io.LimitReader(body, limit))
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
