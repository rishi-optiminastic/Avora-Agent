package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withHome points Dir()/path() at a throwaway home for the duration of the test.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)          // darwin/linux
	t.Setenv("USERPROFILE", home)   // windows
	return home
}

func TestLoadCorruptFileSelfHeals(t *testing.T) {
	home := withHome(t)
	dir := filepath.Join(home, ".avora")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A crash left the file full of null bytes — the exact failure we saw.
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte{0, 0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("corrupt config must not error, got %v", err)
	}
	if cfg.Enrolled() {
		t.Fatal("corrupt config should reset to unenrolled")
	}
}

func TestSaveLoadRoundTripIsAtomic(t *testing.T) {
	withHome(t)
	cfg := &Config{DeviceToken: "tok-123", Sequence: 7}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	// No leftover temp file after an atomic write.
	dir, _ := Dir()
	if _, err := os.Stat(filepath.Join(dir, "agent.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp file should not survive a successful save")
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceToken != "tok-123" || got.Sequence != 7 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
