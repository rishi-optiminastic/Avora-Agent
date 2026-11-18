package health

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"avora-agent/internal/capture"
	"avora-agent/internal/collect"
	"avora-agent/internal/ingest"
)

func TestScreenCaptureFailingAfterThreeThenRecovers(t *testing.T) {
	tr := NewTracker("windows")
	boom := errors.New("BitBlt failed")
	tr.ObserveCapture(nil)
	tr.ObserveCapture(boom)
	tr.ObserveCapture(boom)
	if tr.Statuses().ScreenCapture != StatusOK {
		t.Fatal("two failures must not flip to failing")
	}
	tr.ObserveCapture(boom)
	if tr.Statuses().ScreenCapture != StatusFailing {
		t.Fatal("three consecutive failures must flip to failing")
	}
	tr.ObserveCapture(nil)
	if tr.Statuses().ScreenCapture != StatusOK {
		t.Fatal("one success must flip back to ok")
	}
}

func TestScreenCapturePermissionDenied(t *testing.T) {
	tr := NewTracker("darwin")
	tr.ObserveCapture(fmt.Errorf("%w: exit status 1", capture.ErrPermissionDenied))
	if tr.Statuses().ScreenCapture != StatusPermissionDenied {
		t.Fatal("permission errors map to permission_denied at once")
	}
}

func TestWindowEmptyOnlyAfterTenActiveSamples(t *testing.T) {
	tr := NewTracker("windows")
	for i := 0; i < 20; i++ {
		tr.ObserveSample(collect.Sample{IdleSeconds: 600}, nil) // idle: no evidence
	}
	if tr.Statuses().WindowInfo != StatusUnknown {
		t.Fatal("idle samples must not count")
	}
	for i := 0; i < 9; i++ {
		tr.ObserveSample(collect.Sample{}, nil)
	}
	if tr.Statuses().WindowInfo == StatusEmpty {
		t.Fatal("nine empties is not yet empty")
	}
	tr.ObserveSample(collect.Sample{}, nil)
	if tr.Statuses().WindowInfo != StatusEmpty {
		t.Fatal("ten non-idle empties must flip to empty")
	}
	tr.ObserveSample(collect.Sample{ActiveWindow: "code"}, nil)
	if tr.Statuses().WindowInfo != StatusOK {
		t.Fatal("a named app must flip back to ok")
	}
	tr.ObserveSample(collect.Sample{WindowDenied: true}, nil)
	if tr.Statuses().WindowInfo != StatusPermissionDenied {
		t.Fatal("denied must map to permission_denied")
	}
}

func TestBrowserStatuses(t *testing.T) {
	if NewTracker("linux").Statuses().BrowserURL != StatusUnsupported {
		t.Fatal("linux has no browser reader")
	}
	tr := NewTracker("windows")
	tr.ObserveSample(collect.Sample{ActiveWindow: "chrome", Browser: "Google Chrome", URL: "https://x"}, nil)
	if tr.Statuses().BrowserURL != StatusOK {
		t.Fatal("url read -> ok")
	}
	for i := 0; i < 10; i++ {
		tr.ObserveSample(collect.Sample{ActiveWindow: "chrome", Browser: "Google Chrome"}, nil)
	}
	if tr.Statuses().BrowserURL != StatusEmpty {
		t.Fatal("ten browser samples without url -> empty")
	}
	tr.ObserveSample(collect.Sample{ActiveWindow: "code"}, nil)
	if tr.Statuses().BrowserURL != StatusEmpty {
		t.Fatal("non-browser samples must not change browser status")
	}
}

func TestLastErrorIsCapped(t *testing.T) {
	tr := NewTracker("windows")
	tr.RecordError("x", errors.New(strings.Repeat("a", 500)+"\nsecond line"))
	if len(tr.LastError()) > maxErrorLen || strings.Contains(tr.LastError(), "second") {
		t.Fatalf("last_error not sanitised: %d", len(tr.LastError()))
	}
}

func TestReporterCadenceChangeAnd404(t *testing.T) {
	now := time.Now()
	sent := 0
	var sendErr error
	logs := 0
	r := NewReporter(func(Report) error { sent++; return sendErr }, func(string, ...any) { logs++ })
	r.now = func() time.Time { return now }
	rep := NewTracker("windows").Build("v1", "windows", QueueInfo{})

	_ = r.MaybeSend(rep)
	_ = r.MaybeSend(rep)
	if sent != 1 {
		t.Fatalf("unchanged status within interval must not resend, sent=%d", sent)
	}
	rep.ScreenCapture = StatusFailing
	_ = r.MaybeSend(rep)
	if sent != 2 {
		t.Fatal("a status change must send immediately")
	}
	now = now.Add(ReportInterval)
	sendErr = ingest.ErrNotFound
	if err := r.MaybeSend(rep); err != nil {
		t.Fatal("404 must be swallowed")
	}
	now = now.Add(ReportInterval)
	_ = r.MaybeSend(rep)
	if logs != 1 {
		t.Fatalf("404 must be logged exactly once, got %d", logs)
	}
	// A failed send is not retried before the next interval (no retry storm).
	sendErr = errors.New("offline")
	now = now.Add(ReportInterval)
	_ = r.MaybeSend(rep)
	before := sent
	_ = r.MaybeSend(rep)
	if sent != before {
		t.Fatal("failed post must not be retried immediately")
	}
}

func TestReportQueueAge(t *testing.T) {
	r := NewTracker("windows").Build("v1", "windows", QueueInfo{})
	if r.OldestQueuedAgeSeconds != nil || r.LastError != nil {
		t.Fatal("empty queue / no error must be null")
	}
	r = NewTracker("windows").Build("v1", "windows", QueueInfo{Items: 2, Bytes: 10, OldestAge: 90 * time.Second})
	if r.OldestQueuedAgeSeconds == nil || *r.OldestQueuedAgeSeconds != 90 {
		t.Fatal("oldest age in seconds")
	}
}

func TestAnErrorClearsWhenTheSameSourceRecovers(t *testing.T) {
	tr := NewTracker("windows")
	tr.RecordError("activity send", errors.New("dial tcp: lookup api: no such host"))
	if tr.LastError() == "" {
		t.Fatal("the error should be recorded")
	}
	tr.NoteSuccess("screenshot send")
	if tr.LastError() == "" {
		t.Fatal("a success elsewhere must not hide a different failure")
	}
	tr.NoteSuccess("activity send")
	if got := tr.LastError(); got != "" {
		t.Fatalf("a recovered send must clear its error, got %q", got)
	}
}

func TestAnOldErrorStopsBeingReported(t *testing.T) {
	tr := NewTracker("windows")
	clock := time.Now()
	tr.now = func() time.Time { return clock }
	tr.RecordError("screenshot send", errors.New("context deadline exceeded"))
	if tr.Build("v1", "windows", QueueInfo{}).LastError == nil {
		t.Fatal("a fresh error is reported")
	}
	clock = clock.Add(errorFreshFor + time.Minute)
	if tr.Build("v1", "windows", QueueInfo{}).LastError != nil {
		t.Fatal("an error older than the freshness window must not be reported")
	}
}
