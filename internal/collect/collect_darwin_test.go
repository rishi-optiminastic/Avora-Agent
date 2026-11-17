//go:build darwin

package collect

import (
	"testing"
	"time"
)

// The regression this file's timeout exists for: a helper that never answers
// must not hold the agent's main loop. `sleep` stands in for an AppleScript
// waiting on a beachballing browser, which is what actually froze machines.
func TestRunGivesUpOnAHangingHelper(t *testing.T) {
	start := time.Now()
	got := run("sleep", "30")
	elapsed := time.Since(start)

	if got != "" {
		t.Fatalf("a timed-out helper must yield no data, got %q", got)
	}
	// scriptTimeout plus WaitDelay, with slack for a loaded machine.
	if limit := scriptTimeout + 3*time.Second; elapsed > limit {
		t.Fatalf("run blocked for %s, must give up within %s", elapsed, limit)
	}
}

func TestRunReturnsOutputOfAHealthyHelper(t *testing.T) {
	if got := run("echo", "  hello  "); got != "hello" {
		t.Fatalf("want trimmed %q, got %q", "hello", got)
	}
}

func TestRunSwallowsAFailingHelper(t *testing.T) {
	if got := run("false"); got != "" {
		t.Fatalf("a failing helper must yield %q, got %q", "", got)
	}
}

// Non-browsers must not cost an Apple Event at all.
func TestBrowserTabSkipsNonBrowsers(t *testing.T) {
	url, title, browser, denied := browserTab("Terminal")
	if url != "" || title != "" || browser != "" || denied {
		t.Fatalf("non-browser should yield empty fields, got %q/%q/%q", url, title, browser)
	}
}

// A refused Automation / Accessibility permission must be recognisable, so the
// health report can say "permission_denied" instead of a vague "empty".
func TestPermissionDeniedRecognisesTCCErrors(t *testing.T) {
	_, err := runDetailed("sh", "-c", "echo 'execution error: Not authorized to send Apple events to Google Chrome. (-1743)' >&2; exit 1")
	if !permissionDenied(err) {
		t.Fatal("a -1743 failure must read as permission denied")
	}
	_, err = runDetailed("sh", "-c", "echo 'some other failure' >&2; exit 1")
	if permissionDenied(err) {
		t.Fatal("an unrelated failure must not read as permission denied")
	}
	if permissionDenied(nil) {
		t.Fatal("success is not a denial")
	}
}
