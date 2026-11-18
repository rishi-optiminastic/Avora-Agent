package runner

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"avora-agent/internal/config"
	"avora-agent/internal/health"
	"avora-agent/internal/ingest"
	"avora-agent/internal/outbox"
	"avora-agent/internal/queue"
)

// Offline queue bounds. Twelve hours of activity at one sample per ~30s is
// ~1,440 entries; screenshots dominate the bytes.
//
// The backend files every sample and screenshot under its RECEIVE time and
// applies the work-session gate then (an org decision: the laptop clock is not
// trusted). A replayed backlog therefore lands at the moment it arrives, or is
// dropped if that is after checkout. So activity is only kept long enough to
// bridge a short network blip (a longer gap stays a gap rather than a burst of
// fake activity at reconnect), while screenshots, which carry their capture
// time and are evidence in their own right, are kept for longer.
const (
	queueDirName  = "queue"
	queueMaxItems = 3000
	queueMaxBytes = 200 << 20
	queueMaxAge   = 12 * time.Hour
	// activityMaxAge bridges a short outage only; see above.
	activityMaxAge = 15 * time.Minute

	// outgoingBuffer holds captures waiting for the courier. At one capture
	// every ~15s that is over an hour of slack, far beyond any single slow call.
	outgoingBuffer = 256
	// courierTick is how often the courier drains the backlog and reports health
	// when nothing new has arrived.
	courierTick = 5 * time.Second
	// screenshotTimeout is generous because uploads no longer block capture: a
	// slow office link can take its time instead of failing at 20s.
	screenshotTimeout = 60 * time.Second
)

// agent is the state the main loop carries between ticks.
type agent struct {
	client        *http.Client
	pingClient    *http.Client
	outgoing      chan *queue.Entry
	cfg           *config.Config
	outbox        *outbox.Outbox
	health        *health.Tracker
	reporter      *health.Reporter
	lastApp       string
	lastEventShot time.Time
}

func newAgent(client *http.Client, cfg *config.Config) *agent {
	a := &agent{
		client:     client,
		pingClient: client,
		cfg:        cfg,
		health:     health.NewTracker(runtime.GOOS),
	}
	q, err := OpenQueue()
	if err != nil {
		// Without a queue the agent still runs; undelivered items are dropped,
		// exactly as before the queue existed.
		fmt.Println("  warn: offline queue unavailable, failed sends will be dropped: " + err.Error())
		q = nil
	}
	shotClient := &http.Client{Timeout: screenshotTimeout}
	a.outbox = outbox.New(q, transport{client, shotClient, cfg}, sequencer{cfg}, logLine)
	a.reporter = health.NewReporter(func(r health.Report) error {
		return ingest.SendHealth(client, cfg, r)
	}, logLine)
	return a
}

// OpenQueue opens the offline queue under the agent's data dir (~/.avora/queue).
func OpenQueue() (*queue.Queue, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return queue.Open(filepath.Join(dir, queueDirName), queue.Limits{
		MaxItems: queueMaxItems,
		MaxBytes: queueMaxBytes,
		MaxAge:   queueMaxAge,
		MaxAgeByKind: map[string]time.Duration{
			queue.KindActivity: activityMaxAge,
		},
	}, logLine)
}

// ClearQueue discards anything waiting. Called on (re-)enrollment: queued data
// belongs to the previous enrollment, possibly a different person.
func ClearQueue() {
	q, err := OpenQueue()
	if err != nil {
		return
	}
	q.Clear()
}

func logLine(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

// startCourier launches the goroutine that owns every upload. The capture loop
// only ever hands entries over (submit), so capture keeps its rhythm however
// slow the network is; the outbox and its sequence counter are touched by this
// one goroutine only, which keeps delivery strictly in order.
func (a *agent) startCourier() {
	a.outgoing = make(chan *queue.Entry, outgoingBuffer)
	go a.courier()
}

// submit hands a capture to the courier without ever blocking. If the courier
// has fallen a whole buffer behind, the capture is dropped and said so, rather
// than freezing capture behind it.
func (a *agent) submit(e *queue.Entry) {
	if a.outgoing == nil { // courier not started (tests): deliver inline
		a.noteDelivery(e.Kind, a.outbox.Submit(e))
		return
	}
	select {
	case a.outgoing <- e:
	default:
		fmt.Printf("  warn: delivery is %d captures behind; dropping a %s\n", outgoingBuffer, e.Kind)
	}
}

// courier runs forever. Each step is recovered on its own, so a panic in one
// send costs that send, never the goroutine.
func (a *agent) courier() {
	ticker := time.NewTicker(courierTick)
	defer ticker.Stop()
	for {
		a.courierStep(ticker.C)
	}
}

func (a *agent) courierStep(tick <-chan time.Time) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("  warn: delivery recovered from panic, continuing: %v\n", r)
		}
	}()
	select {
	case e := <-a.outgoing:
		a.noteDelivery(e.Kind, a.outbox.Submit(e))
	case <-tick:
		a.deliverBacklog()
		a.reportHealth()
	}
}

// deliverBacklog drains the offline queue a bounded amount per tick.
func (a *agent) deliverBacklog() {
	res := a.outbox.Drain(outbox.DrainBudget)
	if res.Err != nil {
		recordAuth(a.cfg, res.Err)
	} else if res.Delivered {
		recordAuth(a.cfg, nil)
		// The server is reachable again: earlier send failures are over.
		a.health.NoteSuccess("activity send")
		a.health.NoteSuccess("screenshot send")
	}
}

// noteDelivery folds one submit result into the auth state and the log.
func (a *agent) noteDelivery(kind string, res outbox.Result) {
	switch {
	case res.Err != nil:
		recordAuth(a.cfg, res.Err)
		a.health.RecordError(kind+" send", errors.New(ingest.Short(res.Err)))
		suffix := ""
		if res.Queued {
			suffix = " (queued for retry)"
		}
		fmt.Printf("  warn (%s): %s%s\n", kind, res.Err.Error(), suffix)
	case res.Delivered:
		recordAuth(a.cfg, nil)
		a.health.NoteSuccess(kind + " send")
		if !res.Queued {
			fmt.Printf("  ✓ %s delivered (seq %d)\n", kind, res.Sequence)
		}
	}
}

// reportHealth posts the capture-health report when due (best effort).
func (a *agent) reportHealth() {
	st := a.outbox.Stats()
	report := a.health.Build(config.Version(), runtime.GOOS, health.QueueInfo{
		Items: st.Items, Bytes: st.Bytes, OldestAge: st.OldestAge,
	})
	if report.LastError != nil && a.cfg.DeviceToken != "" {
		// Defence in depth: the token is never in an error message today.
		redacted := strings.ReplaceAll(*report.LastError, a.cfg.DeviceToken, "[redacted]")
		report.LastError = &redacted
	}
	if err := a.reporter.MaybeSend(report); errors.Is(err, ingest.ErrUnauthorized) {
		recordAuth(a.cfg, err)
	}
}

// transport adapts the ingest calls to the outbox.
type transport struct {
	client     *http.Client
	shotClient *http.Client
	cfg        *config.Config
}

func (t transport) SendActivity(seq int, e *queue.Entry) error {
	if e.Sample == nil {
		return &ingest.StatusError{Op: "ingest", Code: http.StatusBadRequest, Body: "queued sample is empty"}
	}
	return ingest.Send(t.client, t.cfg, seq, *e.Sample, e.CapturedAt)
}

func (t transport) SendScreenshot(e *queue.Entry) error {
	if e.Shot == nil {
		return &ingest.StatusError{Op: "screenshot upload", Code: http.StatusBadRequest, Body: "queued shot is empty"}
	}
	return ingest.SendScreenshot(t.shotClient, t.cfg, *e.Shot, e.CapturedAt)
}

// sequencer persists the per-device counter in agent.json BEFORE the number
// is used, so a crash can only ever skip numbers (harmless), never reuse one.
type sequencer struct{ cfg *config.Config }

func (s sequencer) Next() int { return s.Advance(1) }

func (s sequencer) Advance(n int) int {
	stateMu.Lock()
	defer stateMu.Unlock()
	s.cfg.Sequence += n
	if err := s.cfg.Save(); err != nil {
		fmt.Println("  warn (sequence save): " + err.Error())
	}
	return s.cfg.Sequence
}
