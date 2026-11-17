package health

import (
	"errors"
	"time"

	"avora-agent/internal/ingest"
)

// ReportInterval is how often a report is posted when nothing changes.
const ReportInterval = 5 * time.Minute

// Reporter decides when to post and remembers what it last attempted. Not safe
// for concurrent use.
type Reporter struct {
	send func(Report) error
	logf func(format string, args ...any)
	now  func() time.Time

	lastAttempt  time.Time
	lastStatuses Statuses
	attempted    bool
	missingNoted bool
}

// NewReporter builds a reporter around send (normally ingest.SendHealth).
func NewReporter(send func(Report) error, logf func(format string, args ...any)) *Reporter {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Reporter{send: send, logf: logf, now: time.Now}
}

// Due reports whether a post should go out now: on the first call, when the
// interval has passed, or when any status differs from the last attempt.
func (r *Reporter) Due(s Statuses) bool {
	if !r.attempted || s != r.lastStatuses {
		return true
	}
	return r.now().Sub(r.lastAttempt) >= ReportInterval
}

// MaybeSend posts report if Due, and returns the send error (nil when nothing
// was sent). A failed post is NOT retried until the next interval or the next
// status change: health is the latest state only, and must never add a retry
// storm on top of whatever is already going wrong.
//
// A 404 means the backend predates the endpoint. It is logged once and then
// ignored quietly; posting continues at the normal interval so the reports
// start landing as soon as the backend is upgraded.
func (r *Reporter) MaybeSend(report Report) error {
	s := Statuses{report.ScreenCapture, report.WindowInfo, report.BrowserURL}
	if !r.Due(s) {
		return nil
	}
	r.attempted = true
	r.lastAttempt = r.now()
	r.lastStatuses = s

	err := r.send(report)
	switch {
	case err == nil:
		r.missingNoted = false
	case errors.Is(err, ingest.ErrNotFound):
		if !r.missingNoted {
			r.logf("  note: backend has no /devices/health endpoint yet; health reports skipped")
			r.missingNoted = true
		}
		return nil
	default:
		r.logf("  warn (health): %s", ingest.Short(err))
	}
	return err
}
