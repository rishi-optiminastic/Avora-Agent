// Package outbox delivers activity samples and screenshots to the backend,
// buffering them in the durable queue when the backend cannot be reached and
// draining that backlog oldest-first with capped exponential backoff.
//
// # Ordering and sequence numbers
//
// The backend rejects any activity sequence <= the last one it accepted (409),
// so sequences must rise in the order the SERVER receives samples, not the
// order they were captured. Two rules guarantee that:
//
//  1. A sequence is assigned when a sample is first SENT, from the persisted
//     per-device counter, and pinned to that sample for its retries.
//  2. Delivery is strictly FIFO: while anything is queued (or a backoff is in
//     force) a new sample joins the back of the queue instead of being sent
//     directly, and a drain stops at the first entry that has to be retried.
//
// So nothing newer is ever sent ahead of an older pinned sample, and every
// later sample draws a higher number from the counter.
//
// # 409 handling
//
// A 409 for a sequence that may already have reached the server (an earlier
// attempt's reply was lost, or the server failed after reading it) means the
// sample WAS accepted: it is dropped as delivered. A 409 for a freshly drawn
// sequence means the local counter is behind the server's (e.g. agent.json was
// lost); the counter jumps forward with a doubling step until accepted, so even
// a counter that is thousands behind heals within a few attempts.
package outbox

import (
	"errors"
	"math/rand"
	"os"
	"time"

	"avora-agent/internal/ingest"
	"avora-agent/internal/queue"
)

const (
	// DrainBudget is the most queued entries sent per Drain. The backend rate
	// limits each device (120 requests/minute, shared with ping polls and live
	// traffic); at one drain per 15s tick this stays well under it — which is
	// why Submit enqueues rather than draining a second time in the same tick.
	DrainBudget = 15

	baseBackoff = 15 * time.Second
	maxBackoff  = 10 * time.Minute
	// backoffJitter spreads retries so a fleet does not return in lockstep
	// after a backend outage.
	backoffJitter = 0.2

	// maxReplayJumps bounds how many times one send re-draws a sequence after
	// fresh 409s. With a doubling step this covers a counter ~2^8 behind in
	// one go; a larger gap keeps healing on later ticks (the step persists).
	maxReplayJumps = 8
	maxReplayStep  = 1 << 20
)

// Sequencer is the persisted per-device sequence counter.
type Sequencer interface {
	// Next increments, persists and returns the counter.
	Next() int
	// Advance moves the counter forward by n, persists and returns it.
	Advance(n int) int
}

// Transport sends one item. Errors follow the ingest package's contract
// (ingest.ErrReplay, ingest.ErrUnauthorized, *ingest.StatusError, or a
// transport error).
type Transport interface {
	SendActivity(seq int, e *queue.Entry) error
	SendScreenshot(e *queue.Entry) error
}

// Result reports one delivery attempt.
type Result struct {
	Delivered bool
	Queued    bool
	Sequence  int
	// Err is the last error seen (nil when delivered cleanly); the runner uses
	// it for its auth bookkeeping and logging.
	Err error
}

// Outbox owns the queue and the backoff state. Not safe for concurrent use:
// the runner drives it from its single loop.
type Outbox struct {
	q         *queue.Queue // nil => no persistence: failed items are dropped
	transport Transport
	seq       Sequencer
	logf      func(format string, args ...any)
	now       func() time.Time
	jitter    func() float64

	failures    int
	nextAttempt time.Time
	replayStep  int
}

// New builds an outbox. q may be nil if the queue could not be opened; the
// agent then behaves as it did before the queue existed.
func New(q *queue.Queue, t Transport, s Sequencer, logf func(format string, args ...any)) *Outbox {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Outbox{
		q: q, transport: t, seq: s, logf: logf,
		now:        time.Now,
		jitter:     rand.Float64,
		replayStep: 1,
	}
}

// Submit delivers a freshly captured entry, or queues it behind the backlog.
//
// A queued entry is NOT sent from here: the runner drains the backlog once per
// tick, after collecting, so draining here as well meant up to four drains in a
// single tick (activity, event screenshot, periodic screenshot, backlog) — four
// times DrainBudget in requests, well over the backend's 120/minute per-device
// limit, which answers 403 and then backs the whole queue off for minutes at a
// time. Enqueueing costs nothing extra: the backlog drain later in the same
// tick picks this entry up in its turn.
func (o *Outbox) Submit(e *queue.Entry) Result {
	if o.q != nil && (o.q.Len() > 0 || o.backingOff()) {
		if err := o.q.Push(e); err != nil {
			o.logf("  warn (queue): could not queue %s: %v", e.Kind, err)
			return Result{Err: err}
		}
		return Result{Queued: true}
	}

	outcome, err := o.attempt(e)
	switch outcome {
	case delivered:
		o.resetBackoff()
		return Result{Delivered: true, Sequence: e.Sequence, Err: err}
	case dropped:
		o.resetBackoff()
		return Result{Err: err}
	}
	o.noteFailure()
	if o.q == nil {
		o.logf("  warn: %s not delivered and no queue available, dropped", e.Kind)
		return Result{Err: err}
	}
	if qerr := o.q.Push(e); qerr != nil {
		o.logf("  warn (queue): could not queue %s: %v", e.Kind, qerr)
		return Result{Err: err}
	}
	return Result{Queued: true, Sequence: e.Sequence, Err: err}
}

// Drain sends up to budget queued entries, oldest first, unless a backoff is
// in force. It stops at the first entry that must be retried.
func (o *Outbox) Drain(budget int) Result {
	if o.q == nil {
		return Result{}
	}
	o.q.PruneExpired(o.now())
	var res Result
	for sent := 0; sent < budget && !o.backingOff(); sent++ {
		e := o.q.Peek()
		if e == nil {
			break
		}
		outcome, err := o.attempt(e)
		res.Err = err
		switch outcome {
		case delivered:
			o.q.Remove(e.ID)
			o.resetBackoff()
			res.Delivered = true
			res.Sequence = e.Sequence
		case dropped:
			o.q.Remove(e.ID)
			o.resetBackoff()
		case retry:
			e.Attempts++
			if uerr := o.q.Update(e); uerr != nil && !errors.Is(uerr, os.ErrNotExist) {
				o.logf("  warn (queue): could not update entry: %v", uerr)
			}
			o.noteFailure()
			return res
		}
	}
	return res
}

// Stats exposes the queue's size for the health report.
func (o *Outbox) Stats() queue.Stats {
	if o.q == nil {
		return queue.Stats{}
	}
	return o.q.Stats(o.now())
}

// Clear empties the queue (after a re-enrollment).
func (o *Outbox) Clear() {
	if o.q != nil {
		o.q.Clear()
	}
}

type outcome int

const (
	delivered outcome = iota
	dropped
	retry
)

// attempt sends one entry and classifies the result.
func (o *Outbox) attempt(e *queue.Entry) (outcome, error) {
	var err error
	switch e.Kind {
	case queue.KindActivity:
		err = o.sendActivity(e)
	case queue.KindScreenshot:
		err = o.transport.SendScreenshot(e)
	default:
		o.logf("  warn (queue): dropping entry of unknown kind %q", e.Kind)
		return dropped, nil
	}
	switch {
	case err == nil:
		return delivered, nil
	case errors.Is(err, ingest.ErrReplay):
		// sendActivity only lets a replay through after exhausting its jumps;
		// try again next time with the step it has built up.
		return retry, err
	case ingest.Retryable(err):
		return retry, err
	}
	o.logf("  warn: %s rejected by the server, dropping it: %s", e.Kind, ingest.Short(err))
	return dropped, err
}

// sendActivity sends an activity entry under its pinned sequence, drawing one
// on first use, and resolves 409s as described in the package comment.
func (o *Outbox) sendActivity(e *queue.Entry) error {
	if e.Sequence == 0 {
		o.pin(e, o.seq.Next())
	}
	for jumps := 0; ; jumps++ {
		err := o.transport.SendActivity(e.Sequence, e)
		if !errors.Is(err, ingest.ErrReplay) {
			if err != nil && ingest.MaybeDelivered(err) {
				e.SeqSent = true
				o.persist(e)
			}
			if err == nil {
				o.replayStep = 1
			}
			return err
		}
		if e.SeqSent {
			// An earlier attempt with this exact sequence reached the server.
			o.replayStep = 1
			return nil
		}
		if jumps >= maxReplayJumps {
			o.logf("  warn: sequence %d still behind the server after %d jumps, will keep climbing",
				e.Sequence, jumps)
			return err
		}
		o.pin(e, o.seq.Advance(o.replayStep))
		if o.replayStep < maxReplayStep {
			o.replayStep *= 2
		}
	}
}

func (o *Outbox) pin(e *queue.Entry, seq int) {
	e.Sequence = seq
	e.SeqSent = false
	o.persist(e)
}

// persist writes a queued entry's new state; entries not in the queue (a live
// first attempt) have nothing on disk to update yet.
func (o *Outbox) persist(e *queue.Entry) {
	if o.q == nil || e.ID == "" {
		return
	}
	if err := o.q.Update(e); err != nil {
		o.logf("  warn (queue): could not update entry: %v", err)
	}
}

func (o *Outbox) backingOff() bool {
	return !o.nextAttempt.IsZero() && o.now().Before(o.nextAttempt)
}

func (o *Outbox) resetBackoff() {
	o.failures = 0
	o.nextAttempt = time.Time{}
}

// noteFailure schedules the next attempt: 15s, 30s, 60s, ... capped at 10 min,
// each stretched by up to 20% jitter.
func (o *Outbox) noteFailure() {
	o.failures++
	d := Backoff(o.failures)
	d += time.Duration(float64(d) * backoffJitter * o.jitter())
	o.nextAttempt = o.now().Add(d)
}

// Backoff is the un-jittered delay after n consecutive failures (n >= 1).
func Backoff(n int) time.Duration {
	d := baseBackoff
	for i := 1; i < n && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}
