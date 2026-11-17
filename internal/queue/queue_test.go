package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"avora-agent/internal/capture"
	"avora-agent/internal/collect"
)

func activity(app string, at time.Time) *Entry {
	return &Entry{Kind: KindActivity, CapturedAt: at, Sample: &collect.Sample{ActiveWindow: app}}
}

func TestPersistsAcrossReopenInOrder(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(dir, Limits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, app := range []string{"a", "b", "c"} {
		if err := q.Push(activity(app, now)); err != nil {
			t.Fatal(err)
		}
	}
	// A crash mid-write leaves a temp file; reopening must ignore and remove it.
	_ = os.WriteFile(filepath.Join(dir, "junk.json.tmp"), []byte("{"), 0o600)

	q2, err := Open(dir, Limits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a", "b", "c"} {
		e := q2.Peek()
		if e == nil || e.Sample.ActiveWindow != want {
			t.Fatalf("want %q, got %+v", want, e)
		}
		q2.Remove(e.ID)
	}
	if q2.Len() != 0 {
		t.Fatal("queue should be empty")
	}
	if _, err := os.Stat(filepath.Join(dir, "junk.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp leftover should be cleaned on open")
	}
	// New ids continue after the old ones, so order survives restarts.
	_ = q2.Push(activity("d", now))
	if e := q2.Peek(); e.Sample.ActiveWindow != "d" {
		t.Fatal("push after reopen")
	}
}

func TestCapDropsOldest(t *testing.T) {
	q, _ := Open(t.TempDir(), Limits{MaxItems: 2}, nil)
	now := time.Now()
	for _, app := range []string{"a", "b", "c"} {
		_ = q.Push(activity(app, now))
	}
	if q.Len() != 2 || q.Peek().Sample.ActiveWindow != "b" {
		t.Fatalf("expected oldest dropped, head=%v len=%d", q.Peek().Sample.ActiveWindow, q.Len())
	}
}

func TestByteCapDropsOldest(t *testing.T) {
	q, _ := Open(t.TempDir(), Limits{MaxBytes: 300}, nil)
	now := time.Now()
	for _, app := range []string{"a", "b", "c", "d"} {
		_ = q.Push(activity(app, now))
	}
	if st := q.Stats(now); st.Bytes > 300 || st.Items == 0 || st.Items == 4 {
		t.Fatalf("byte cap not enforced: %+v", st)
	}
	if q.Peek().Sample.ActiveWindow == "a" {
		t.Fatal("oldest should have been dropped first")
	}
}

func TestPruneExpired(t *testing.T) {
	q, _ := Open(t.TempDir(), Limits{MaxAge: time.Hour}, nil)
	now := time.Now()
	_ = q.Push(activity("old", now.Add(-2*time.Hour)))
	_ = q.Push(activity("new", now))
	if n := q.PruneExpired(now); n != 1 {
		t.Fatalf("want 1 pruned, got %d", n)
	}
	if q.Peek().Sample.ActiveWindow != "new" {
		t.Fatal("wrong survivor")
	}
}

func TestCorruptEntryIsSkipped(t *testing.T) {
	dir := t.TempDir()
	q, _ := Open(dir, Limits{}, nil)
	now := time.Now()
	_ = q.Push(activity("a", now))
	_ = q.Push(activity("b", now))
	first := q.Peek()
	matches, _ := filepath.Glob(filepath.Join(dir, first.ID+"-*.json"))
	_ = os.WriteFile(matches[0], []byte{0, 0}, 0o600)
	if e := q.Peek(); e == nil || e.Sample.ActiveWindow != "b" {
		t.Fatal("corrupt head must be dropped, not wedge the queue")
	}
}

func TestPruneUsesEachKindsOwnAge(t *testing.T) {
	dir := t.TempDir()
	limits := Limits{MaxAge: 12 * time.Hour, MaxAgeByKind: map[string]time.Duration{KindActivity: 15 * time.Minute}}
	q, err := Open(dir, limits, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-time.Hour)
	if err := q.Push(&Entry{Kind: KindActivity, CapturedAt: old, Sample: &collect.Sample{}}); err != nil {
		t.Fatal(err)
	}
	if err := q.Push(&Entry{Kind: KindScreenshot, CapturedAt: old, Shot: &capture.Shot{}}); err != nil {
		t.Fatal(err)
	}
	if got := q.PruneExpired(now); got != 1 {
		t.Fatalf("dropped %d, want only the hour-old activity sample", got)
	}
	// The kind survives a restart: reopen and prune again, nothing more goes.
	q2, err := Open(dir, limits, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q2.Len() != 1 || q2.PruneExpired(now) != 0 {
		t.Fatalf("after reopen: len %d", q2.Len())
	}
}
