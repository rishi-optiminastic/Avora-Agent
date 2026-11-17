package supervise

import (
	"errors"
	"testing"
	"time"
)

func TestBackoffDoublesToACapAndResetsAfterAHealthyRun(t *testing.T) {
	d := NextDelay(0, time.Second)
	if d != firstDelay {
		t.Fatalf("first restart should wait %s, got %s", firstDelay, d)
	}
	for i := 0; i < 10; i++ {
		d = NextDelay(d, time.Second)
	}
	if d != maxDelay {
		t.Fatalf("a crash loop must cap at %s, got %s", maxDelay, d)
	}
	if got := NextDelay(d, healthyRun); got != firstDelay {
		t.Fatalf("a long healthy run must reset the backoff, got %s", got)
	}
}

func TestLoopKeepsRestartingAChildThatKeepsDying(t *testing.T) {
	starts := 0
	var waits []time.Duration
	stop := errors.New("stop")
	defer func() {
		if r := recover(); r != stop {
			t.Fatalf("unexpected: %v", r)
		}
		if starts != 4 {
			t.Fatalf("want 4 starts, got %d", starts)
		}
		if waits[0] != firstDelay || waits[1] != 2*firstDelay {
			t.Fatalf("waits should back off: %v", waits)
		}
	}()
	Loop(func() error { starts++; return errors.New("crashed") }, func(d time.Duration) {
		waits = append(waits, d)
		if len(waits) == 4 {
			panic(stop) // end the otherwise endless loop
		}
	})
}
