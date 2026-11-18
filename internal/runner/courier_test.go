package runner

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"avora-agent/internal/collect"
	"avora-agent/internal/config"
	"avora-agent/internal/health"
	"avora-agent/internal/outbox"
	"avora-agent/internal/queue"
)

// slowServer takes its time over every send, like an overloaded backend.
type slowServer struct {
	mu    sync.Mutex
	delay time.Duration
	seen  []string
}

func (s *slowServer) SendActivity(_ int, e *queue.Entry) error {
	time.Sleep(s.delay)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, e.Sample.ActiveWindow)
	return nil
}

func (s *slowServer) SendScreenshot(*queue.Entry) error { time.Sleep(s.delay); return nil }

func (s *slowServer) delivered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func TestASlowServerNeverHoldsUpCapture(t *testing.T) {
	withHome(t)
	cfg := &config.Config{}
	server := &slowServer{delay: 200 * time.Millisecond}
	a := &agent{cfg: cfg, health: health.NewTracker(runtime.GOOS)}
	a.outbox = outbox.New(nil, server, sequencer{cfg}, logLine)
	a.startCourier()

	began := time.Now()
	apps := []string{"a", "b", "c", "d", "e"}
	for _, app := range apps {
		a.submit(&queue.Entry{
			Kind:       queue.KindActivity,
			CapturedAt: time.Now(),
			Sample:     &collect.Sample{ActiveWindow: app},
		})
	}
	if spent := time.Since(began); spent > 50*time.Millisecond {
		t.Fatalf("handing captures over must not wait on the network, took %s", spent)
	}

	deadline := time.Now().Add(5 * time.Second)
	for len(server.delivered()) < len(apps) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := server.delivered()
	for i, app := range apps {
		if i >= len(got) || got[i] != app {
			t.Fatalf("every capture must arrive, in order: want %v, got %v", apps, got)
		}
	}
}
