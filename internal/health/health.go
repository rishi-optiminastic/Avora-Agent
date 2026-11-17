// Package health tracks whether the agent is actually capturing anything and
// reports it to the backend, so a machine that is "online" but sending blank
// data (no screenshots, empty app names, no browser URLs) is visible on the
// dashboard instead of silently producing empty reports.
//
// Tracker folds every capture/collect outcome into a small set of statuses;
// Reporter decides when to post them: every ReportInterval, and at once when a
// status changes. Reports are best-effort and never queued: only the latest
// state matters.
package health

import (
	"errors"
	"strings"
	"time"

	"avora-agent/internal/capture"
	"avora-agent/internal/collect"
)

// Status values, exactly as the backend contract spells them.
const (
	StatusOK               = "ok"
	StatusFailing          = "failing"
	StatusEmpty            = "empty"
	StatusPermissionDenied = "permission_denied"
	StatusUnsupported      = "unsupported"
	StatusUnknown          = "unknown"
)

const (
	// captureFailuresBeforeFailing consecutive screenshot errors flip
	// screen_capture to "failing"; one success flips it back.
	captureFailuresBeforeFailing = 3
	// emptySamplesBeforeEmpty consecutive non-idle samples with no foreground
	// app (or, in a browser, no URL) flip window_info / browser_url to "empty".
	emptySamplesBeforeEmpty = 10
	// idleThresholdSeconds matches the backend's idle cut-off
	// (IDLE_SAMPLE_SECONDS): a sample idle this long is not evidence either way.
	idleThresholdSeconds = 300
	// maxErrorLen is the contract's cap on last_error.
	maxErrorLen = 300
)

// Statuses is the part of the report that triggers an immediate post when it
// changes.
type Statuses struct {
	ScreenCapture string
	WindowInfo    string
	BrowserURL    string
}

// Tracker accumulates capture outcomes. Not safe for concurrent use.
type Tracker struct {
	statuses       Statuses
	captureFails   int
	windowEmpty    int
	browserEmpty   int
	lastError      string
	browserSupport bool
}

// NewTracker starts every status at "unknown"; browser_url is "unsupported" on
// platforms without a browser reader (anything but macOS and Windows).
func NewTracker(goos string) *Tracker {
	t := &Tracker{
		statuses:       Statuses{StatusUnknown, StatusUnknown, StatusUnknown},
		browserSupport: goos == "darwin" || goos == "windows",
	}
	if !t.browserSupport {
		t.statuses.BrowserURL = StatusUnsupported
	}
	return t
}

// Statuses returns the current statuses.
func (t *Tracker) Statuses() Statuses { return t.statuses }

// LastError is the most recent recorded error, already sanitised.
func (t *Tracker) LastError() string { return t.lastError }

// ObserveCapture records one screenshot attempt.
func (t *Tracker) ObserveCapture(err error) {
	if err == nil {
		t.captureFails = 0
		t.statuses.ScreenCapture = StatusOK
		return
	}
	t.captureFails++
	t.RecordError("screenshot", err)
	switch {
	case errors.Is(err, capture.ErrPermissionDenied):
		t.statuses.ScreenCapture = StatusPermissionDenied
	case t.captureFails >= captureFailuresBeforeFailing:
		t.statuses.ScreenCapture = StatusFailing
	}
}

// ObserveSample records one activity collection.
func (t *Tracker) ObserveSample(s collect.Sample, err error) {
	if err != nil {
		t.RecordError("collect", err)
		return // the platform cannot collect at all: leave statuses as they are
	}
	t.observeWindow(s)
	if t.browserSupport {
		t.observeBrowser(s)
	}
}

func (t *Tracker) observeWindow(s collect.Sample) {
	switch {
	case s.WindowDenied:
		t.windowEmpty = 0
		t.statuses.WindowInfo = StatusPermissionDenied
	case s.ActiveWindow != "":
		t.windowEmpty = 0
		t.statuses.WindowInfo = StatusOK
	case s.IdleSeconds >= idleThresholdSeconds:
		// Away from the keyboard (or locked): an empty answer proves nothing.
	default:
		t.windowEmpty++
		if t.windowEmpty >= emptySamplesBeforeEmpty {
			t.statuses.WindowInfo = StatusEmpty
		}
	}
}

func (t *Tracker) observeBrowser(s collect.Sample) {
	switch {
	case s.Browser == "":
		// Not in a browser: nothing to learn about the URL reader.
	case s.BrowserDenied:
		t.browserEmpty = 0
		t.statuses.BrowserURL = StatusPermissionDenied
	case s.URL != "":
		t.browserEmpty = 0
		t.statuses.BrowserURL = StatusOK
	case s.IdleSeconds >= idleThresholdSeconds:
	default:
		t.browserEmpty++
		if t.browserEmpty >= emptySamplesBeforeEmpty {
			t.statuses.BrowserURL = StatusEmpty
		}
	}
}

// RecordError keeps err as last_error: first line only, capped at 300 chars.
// Callers must pass errors that carry no window titles, URLs or server replies
// (use ingest.Short for HTTP errors).
func (t *Tracker) RecordError(source string, err error) {
	if err == nil {
		return
	}
	t.lastError = Sanitize(source + ": " + err.Error())
}

// Sanitize trims an error message to its first line and the contract's length.
func Sanitize(msg string) string {
	msg, _, _ = strings.Cut(msg, "\n")
	msg = strings.TrimSpace(msg)
	if len(msg) > maxErrorLen {
		// Cut on bytes, then drop a rune the cut may have split.
		msg = strings.ToValidUTF8(msg[:maxErrorLen], "")
	}
	return msg
}

// Report is the JSON body of POST /api/v1/devices/health.
type Report struct {
	AgentVersion           string  `json:"agent_version"`
	OS                     string  `json:"os"`
	ScreenCapture          string  `json:"screen_capture"`
	WindowInfo             string  `json:"window_info"`
	BrowserURL             string  `json:"browser_url"`
	LastError              *string `json:"last_error"`
	QueueItems             int     `json:"queue_items"`
	QueueBytes             int64   `json:"queue_bytes"`
	OldestQueuedAgeSeconds *int    `json:"oldest_queued_age_seconds"`
}

// QueueInfo is the queue part of a report.
type QueueInfo struct {
	Items     int
	Bytes     int64
	OldestAge time.Duration
}

// Build assembles a report from the tracker's state and the queue's.
func (t *Tracker) Build(version, goos string, q QueueInfo) Report {
	r := Report{
		AgentVersion:  version,
		OS:            goos,
		ScreenCapture: t.statuses.ScreenCapture,
		WindowInfo:    t.statuses.WindowInfo,
		BrowserURL:    t.statuses.BrowserURL,
		QueueItems:    q.Items,
		QueueBytes:    q.Bytes,
	}
	if t.lastError != "" {
		msg := t.lastError
		r.LastError = &msg
	}
	if q.Items > 0 {
		age := int(q.OldestAge / time.Second)
		r.OldestQueuedAgeSeconds = &age
	}
	return r
}
