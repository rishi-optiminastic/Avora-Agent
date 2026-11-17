package main

import (
	"os"
	"path/filepath"
	"testing"

	"avora-agent/internal/config"
)

// A machine reconnected after a long run of refused calls must come back as a
// clean enrollment. Leaving UnauthorizedSince behind sent `run` straight into
// dormancy — warning the employee that Avora had lost access, and capturing
// nothing for the first dormant interval — on a token the server had just
// issued.
func TestAdoptTokenClearsThePreviousEnrollment(t *testing.T) {
	withHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DeviceToken = "old-token"
	cfg.Sequence = 4210
	cfg.UnauthorizedSince = "2026-01-01T00:00:00Z"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	adoptToken(cfg, "fresh-token")

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DeviceToken != "fresh-token" {
		t.Fatalf("token = %q; want the new one persisted", reloaded.DeviceToken)
	}
	if reloaded.Sequence != 0 {
		t.Fatalf("sequence = %d; want 0 for a new enrollment", reloaded.Sequence)
	}
	if reloaded.UnauthorizedSince != "" {
		t.Fatalf("UnauthorizedSince = %q; a new token must not start out refused",
			reloaded.UnauthorizedSince)
	}
}

// A double-click on an already-installed agent re-runs install, so the swap has
// to cope with a target that already exists — on Windows, one that is locked by
// the running process. The rename dance is what makes that work; these pin it.
func TestReplaceBinaryCreatesTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "avora-agent")
	if err := replaceBinary(target, []byte("v1")); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "v1" {
		t.Fatalf("target = %q, %v; want \"v1\"", got, err)
	}
}

func TestReplaceBinaryOverwritesAndLeavesNoLitter(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "avora-agent")
	if err := replaceBinary(target, []byte("v1")); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := replaceBinary(target, []byte("v2-longer")); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "v2-longer" {
		t.Fatalf("target = %q; want the new bytes", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "avora-agent" {
			t.Fatalf("left %q behind; the swap must clean up after itself", e.Name())
		}
	}
}

func TestReplaceBinaryIsExecutable(t *testing.T) {
	target := filepath.Join(t.TempDir(), "avora-agent")
	if err := replaceBinary(target, []byte("#!/bin/sh\n")); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	// Auto-start execs this path directly; a non-executable copy installs
	// "successfully" and then never runs.
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("mode = %v; want the execute bit set", info.Mode().Perm())
	}
}

// A stale .old from an interrupted swap must not block the next install.
func TestReplaceBinaryClearsAStaleOldFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "avora-agent")
	if err := os.WriteFile(target+".old", []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(target, []byte("v1")); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatal("stale .old survived the swap")
	}
}
