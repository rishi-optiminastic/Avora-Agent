package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"avora-agent/internal/config"
)

// maxLogBytes caps agent.log before it is rolled to agent.log.1 at the start of
// a `run`, so two files bound the disk cost at ~8 MB per agent process.
const maxLogBytes = 4 << 20

// startFileLog sends stdout and stderr to ~/.avora/agent.log when nothing can
// see a console. roll asks for an oversized log to be rotated first, which only
// the long-lived `run` invocation may do (see main).
//
// The release binary is built -H windowsgui and auto-start gives it no console,
// so until now every diagnostic the agent printed — the warnings, the reason it
// stopped, the token being refused — was written to a handle that goes nowhere.
// A machine that went quiet left no trace of why, which is exactly why this took
// so long to pin down. Reassigning os.Stdout/os.Stderr captures every existing
// print with no other change.
//
// Best-effort: if the log cannot be opened, carry on without it. Losing the log
// must never stop the agent from running.
func startFileLog(roll bool) {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, "agent.log")
	if roll {
		rollIfLarge(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	os.Stdout = f
	os.Stderr = f
}

// rollIfLarge keeps one previous generation, so the reason a machine died is
// still on disk after it has been restarted a few times.
func rollIfLarge(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxLogBytes {
		return
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}

// logPath is where startFileLog writes, for `status` to point at.
func logPath() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "agent.log")
}

// tailLog copies the last of the log to w, for `avora-agent log`.
func tailLog(w io.Writer, limit int64) error {
	f, err := os.Open(logPath())
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > limit {
		if _, err := f.Seek(-limit, io.SeekEnd); err != nil {
			return err
		}
	}
	// Read it ALL before writing a byte. With no console, startFileLog has made
	// os.Stdout this very file in append mode, so streaming would append every
	// chunk it had just read — the reader could never reach a moving end-of-file
	// and the copy would run until the disk filled. LimitReader bounds it even
	// if something else is appending.
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// printLog is the `log` subcommand: show recent agent output. This is the first
// thing to run on a machine that has gone quiet.
func printLog() {
	if err := tailLog(os.Stdout, 64<<10); err != nil {
		fmt.Fprintln(os.Stderr, "no agent log yet at "+logPath())
	}
}
