package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"avora-agent/internal/config"
)

// withHome points config.Dir() — and so logPath() — at a throwaway home.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home) // darwin/linux
	t.Setenv("USERPROFILE", home)
	dir, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// cappedAppender writes back into the log file it is being read from — exactly
// what `avora-agent log` does when there is no console, because startFileLog
// has made os.Stdout that same file in append mode. The cap turns the
// regression (streaming the log into itself, so the reader chases an
// end-of-file that its own writes keep pushing away, until the disk fills) into
// an error instead of a hung test.
type cappedAppender struct {
	f       *os.File
	written int
	limit   int
}

func (c *cappedAppender) Write(p []byte) (int, error) {
	if c.written+len(p) > c.limit {
		return 0, errors.New("tailLog kept appending to the log it was reading")
	}
	n, err := c.f.Write(p)
	c.written += n
	return n, err
}

func TestTailLogIntoItsOwnFileTerminates(t *testing.T) {
	withHome(t)
	const tail = 4 << 10
	body := strings.Repeat("avora agent line\n", 2000) // comfortably over `tail`
	if err := os.WriteFile(logPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(logPath(), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	// Twice the tail is slack for a short write; an unbounded copy blows past it.
	w := &cappedAppender{f: f, limit: 2 * tail}
	if err := tailLog(w, tail); err != nil {
		t.Fatalf("tailLog: %v", err)
	}
	if w.written != tail {
		t.Fatalf("wrote %d bytes, want exactly the %d-byte tail", w.written, tail)
	}
}

func TestTailLogReportsAMissingLog(t *testing.T) {
	withHome(t)
	if err := tailLog(os.Stdout, 1<<10); err == nil {
		t.Fatal("a machine with no log yet must report that, not pretend success")
	}
}

// A short-lived invocation must not rotate the log a live `run` is writing to:
// the running agent's fd follows the rename, so every later diagnostic would
// land in agent.log.1 while `avora-agent log` showed an empty agent.log.
func TestStartFileLogOnlyRollsWhenAsked(t *testing.T) {
	withHome(t)
	oversized := make([]byte, maxLogBytes+1)
	if err := os.WriteFile(logPath(), oversized, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := os.Stdout, os.Stderr
	defer func() { os.Stdout, os.Stderr = stdout, stderr }()

	startFileLog(false)
	if _, err := os.Stat(logPath() + ".1"); !os.IsNotExist(err) {
		t.Fatal("a non-run invocation rolled the log out from under the agent")
	}

	startFileLog(true)
	if _, err := os.Stat(logPath() + ".1"); err != nil {
		t.Fatalf("run should roll an oversized log: %v", err)
	}
}
