package outbox

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"avora-agent/internal/collect"
	"avora-agent/internal/ingest"
	"avora-agent/internal/queue"
)

type counter struct{ n int }

func (c *counter) Next() int         { c.n++; return c.n }
func (c *counter) Advance(k int) int { c.n += k; return c.n }

// fakeServer mimics the backend's replay rule: seq must exceed the last one.
type fakeServer struct {
	last     int
	down     bool
	failNext error // returned once, after optionally accepting
	accepted []string
	seqs     []int
	shots    int
}

func (f *fakeServer) SendActivity(seq int, e *queue.Entry) error {
	if f.down {
		return errors.New("dial tcp: network is unreachable")
	}
	if seq <= f.last {
		return ingest.ErrReplay
	}
	f.last = seq
	f.seqs = append(f.seqs, seq)
	f.accepted = append(f.accepted, e.Sample.ActiveWindow)
	if f.failNext != nil { // accepted, but the reply is lost
		err := f.failNext
		f.failNext = nil
		return err
	}
	return nil
}

func (f *fakeServer) SendScreenshot(e *queue.Entry) error {
	if f.down {
		return errors.New("offline")
	}
	f.shots++
	return nil
}

type clock struct{ t time.Time }

func setup(t *testing.T, srv *fakeServer, seq *counter) (*Outbox, *clock, *queue.Queue) {
	t.Helper()
	q, err := queue.Open(t.TempDir(), queue.Limits{MaxAge: 12 * time.Hour}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{time.Now()}
	o := New(q, srv, seq, nil)
	o.now = func() time.Time { return c.t }
	o.jitter = func() float64 { return 0 }
	return o, c, q
}

func sample(app string) *queue.Entry {
	return &queue.Entry{Kind: queue.KindActivity, CapturedAt: time.Now(), Sample: &collect.Sample{ActiveWindow: app}}
}

func TestOfflineQueueThenDrainInOrderWithMonotonicSequences(t *testing.T) {
	srv, seq := &fakeServer{}, &counter{}
	o, c, q := setup(t, srv, seq)

	o.Submit(sample("live1"))
	srv.down = true
	for _, app := range []string{"q1", "q2", "q3"} {
		if res := o.Submit(sample(app)); !res.Queued {
			t.Fatalf("%s should be queued while offline", app)
		}
	}
	srv.down = false
	// Still backing off: a live sample must queue behind the backlog, not jump it.
	if res := o.Submit(sample("live2")); !res.Queued {
		t.Fatal("live sample must not overtake the backlog during backoff")
	}
	c.t = c.t.Add(maxBackoff + time.Second)
	o.Drain(DrainBudget)
	if q.Len() != 0 {
		t.Fatalf("queue should be drained, %d left", q.Len())
	}
	want := []string{"live1", "q1", "q2", "q3", "live2"}
	for i, app := range want {
		if srv.accepted[i] != app {
			t.Fatalf("order: got %v want %v", srv.accepted, want)
		}
	}
	for i := 1; i < len(srv.seqs); i++ {
		if srv.seqs[i] <= srv.seqs[i-1] {
			t.Fatalf("sequences not strictly increasing: %v", srv.seqs)
		}
	}
}

func TestLostReplyThen409IsTreatedAsDelivered(t *testing.T) {
	srv, seq := &fakeServer{failNext: &ingest.StatusError{Op: "ingest", Code: 502}}, &counter{}
	o, c, q := setup(t, srv, seq)
	if res := o.Submit(sample("a")); !res.Queued {
		t.Fatal("a 502 must queue the sample")
	}
	c.t = c.t.Add(time.Minute)
	o.Drain(DrainBudget)
	if q.Len() != 0 || len(srv.accepted) != 1 {
		t.Fatalf("409 on a maybe-delivered seq must count as delivered, not duplicate: %v", srv.accepted)
	}
}

func TestCounterBehindServerHeals(t *testing.T) {
	srv, seq := &fakeServer{last: 100}, &counter{}
	o, _, _ := setup(t, srv, seq)
	for i := 0; i < 5; i++ {
		o.Submit(sample("x"))
		o.Drain(DrainBudget)
	}
	if len(srv.accepted) == 0 {
		t.Fatal("a counter behind the server must climb past it")
	}
}

func TestPermanent4xxIsDropped(t *testing.T) {
	q, _ := queue.Open(t.TempDir(), queue.Limits{}, nil)
	tr := &rejecting{code: http.StatusUnprocessableEntity}
	o := New(q, tr, &counter{}, nil)
	res := o.Submit(sample("a"))
	if res.Queued || q.Len() != 0 {
		t.Fatal("a 422 must be dropped, not retried forever")
	}
	tr.code = http.StatusTooManyRequests
	if res := o.Submit(sample("b")); !res.Queued {
		t.Fatal("a 429 must be retried")
	}
}

type rejecting struct{ code int }

func (r *rejecting) SendActivity(int, *queue.Entry) error {
	return &ingest.StatusError{Op: "ingest", Code: r.code}
}
func (r *rejecting) SendScreenshot(*queue.Entry) error {
	return &ingest.StatusError{Op: "screenshot upload", Code: r.code}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	if Backoff(1) != baseBackoff || Backoff(2) != 2*baseBackoff {
		t.Fatal("backoff should start at base and double")
	}
	if Backoff(50) != maxBackoff {
		t.Fatal("backoff must cap")
	}
}

func TestBackoffBlocksDrainUntilDue(t *testing.T) {
	srv, seq := &fakeServer{down: true}, &counter{}
	o, c, q := setup(t, srv, seq)
	o.Submit(sample("a"))
	srv.down = false
	o.Drain(DrainBudget)
	if q.Len() != 1 {
		t.Fatal("drain must wait for the backoff")
	}
	c.t = c.t.Add(Backoff(1) + time.Second)
	o.Drain(DrainBudget)
	if q.Len() != 0 {
		t.Fatal("drain should send once the backoff has passed")
	}
}

func TestQueuePersistsSequencePinAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	q, _ := queue.Open(dir, queue.Limits{}, nil)
	srv := &fakeServer{down: true}
	o := New(q, srv, &counter{}, nil)
	o.Submit(sample("a"))
	q2, _ := queue.Open(dir, queue.Limits{}, nil)
	e := q2.Peek()
	if e == nil || e.Sequence != 1 || !e.SeqSent {
		t.Fatalf("pinned sequence must survive a restart: %+v", e)
	}
}
