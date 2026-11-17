package config

import "testing"

func TestAReleaseBuiltWithoutARepoStillUpdates(t *testing.T) {
	saved := updateRepo
	t.Cleanup(func() { updateRepo = saved })

	updateRepo = ""
	if got := UpdateRepo(); got != defaultUpdateRepo {
		t.Fatalf("missing repo must fall back to %q, got %q", defaultUpdateRepo, got)
	}
	updateRepo = "none"
	if got := UpdateRepo(); got != "" {
		t.Fatalf("an explicit none must disable updates, got %q", got)
	}
	updateRepo = "acme/agent"
	if got := UpdateRepo(); got != "acme/agent" {
		t.Fatalf("a baked repo must win, got %q", got)
	}
}
