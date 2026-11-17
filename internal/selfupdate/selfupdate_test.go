package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serve(t *testing.T, files map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestDownloadVerifiedOK(t *testing.T) {
	bin := "binary-bytes"
	base := serve(t, map[string]string{
		"avora-agent.exe": bin,
		"SHA256SUMS":      sum("other") + "  avora-agent-macos-arm64\n" + sum(bin) + "  avora-agent.exe\n",
	})
	dest := filepath.Join(t.TempDir(), "new")
	if err := downloadVerified(base, "avora-agent.exe", dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != bin {
		t.Fatal("wrong content")
	}
}

func TestDownloadVerifiedMismatch(t *testing.T) {
	base := serve(t, map[string]string{
		"avora-agent.exe": "tampered",
		"SHA256SUMS":      sum("original") + "  avora-agent.exe\n",
	})
	if err := downloadVerified(base, "avora-agent.exe", filepath.Join(t.TempDir(), "new")); err == nil {
		t.Fatal("mismatch must be refused")
	}
}

func TestDownloadVerifiedMissingChecksums(t *testing.T) {
	base := serve(t, map[string]string{"avora-agent.exe": "x"})
	if err := downloadVerified(base, "avora-agent.exe", filepath.Join(t.TempDir(), "new")); err == nil {
		t.Fatal("a release without SHA256SUMS must be refused")
	}
	base = serve(t, map[string]string{
		"avora-agent.exe": "x",
		"SHA256SUMS":      sum("x") + "  some-other-asset\n",
	})
	if err := downloadVerified(base, "avora-agent.exe", filepath.Join(t.TempDir(), "new")); err == nil {
		t.Fatal("a SHA256SUMS without this asset must be refused")
	}
}
