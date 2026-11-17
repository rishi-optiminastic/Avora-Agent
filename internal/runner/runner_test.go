package runner

import (
	"errors"
	"testing"
	"time"

	"avora-agent/internal/config"
	"avora-agent/internal/ingest"
)

// withHome points the config's Dir() at a throwaway home, so these tests never
// touch the developer's real ~/.avora/agent.json.
func withHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)        // darwin/linux
	t.Setenv("USERPROFILE", home) // windows
}

// The regression this whole state machine exists for: a single 401 must not be
// treated as a revocation. It used to delete the machine's auto-start and wipe
// its token, which is why laptops had to be reinstalled by hand.
func TestOneUnauthorizedDoesNotRevoke(t *testing.T) {
	withHome(t)
	cfg := &config.Config{DeviceToken: "tok"}

	recordAuth(cfg, ingest.ErrUnauthorized)

	if cfg.DeviceToken != "tok" {
		t.Fatal("a 401 must never clear the device token")
	}
	if cfg.UnauthorizedSince == "" {
		t.Fatal("a 401 should start the rejection clock")
	}
	if rejectedFor(cfg) > revokedAfter {
		t.Fatal("a single fresh 401 must not count as revoked")
	}
}

func TestRejectionRunKeepsItsFirstTimestamp(t *testing.T) {
	withHome(t)
	cfg := &config.Config{DeviceToken: "tok"}

	recordAuth(cfg, ingest.ErrUnauthorized)
	first := cfg.UnauthorizedSince
	time.Sleep(1100 * time.Millisecond) // RFC3339 has second resolution
	recordAuth(cfg, ingest.ErrUnauthorized)

	if cfg.UnauthorizedSince != first {
		t.Fatalf("later 401s must not restart the clock: %q -> %q", first, cfg.UnauthorizedSince)
	}
}

func TestSuccessClearsTheRejectionRun(t *testing.T) {
	withHome(t)
	cfg := &config.Config{DeviceToken: "tok"}
	recordAuth(cfg, ingest.ErrUnauthorized)

	recordAuth(cfg, nil)

	if cfg.UnauthorizedSince != "" {
		t.Fatal("an accepted call must clear the rejection run")
	}
	if rejectedFor(cfg) != 0 {
		t.Fatal("rejectedFor should be zero once cleared")
	}
}

// A replay (409) is proof the token authenticated: auth runs before the route.
func TestReplayCountsAsAccepted(t *testing.T) {
	withHome(t)
	cfg := &config.Config{DeviceToken: "tok"}
	recordAuth(cfg, ingest.ErrUnauthorized)

	recordAuth(cfg, ingest.ErrReplay)

	if cfg.UnauthorizedSince != "" {
		t.Fatal("a 409 reached the server past auth, so it must clear the run")
	}
}

// A laptop on a train must not drift towards dormancy, and must not escape one
// either: a transport error says nothing at all about the token.
func TestNetworkErrorIsNeutral(t *testing.T) {
	withHome(t)
	cfg := &config.Config{DeviceToken: "tok"}
	offline := errors.New("dial tcp: no route to host")

	recordAuth(cfg, offline)
	if cfg.UnauthorizedSince != "" {
		t.Fatal("a network error must not start the rejection clock")
	}

	recordAuth(cfg, ingest.ErrUnauthorized)
	recordAuth(cfg, offline)
	if cfg.UnauthorizedSince == "" {
		t.Fatal("a network error must not clear an existing rejection run")
	}
}

func TestDormantOnlyAfterTheFullWindow(t *testing.T) {
	withHome(t)
	cfg := &config.Config{DeviceToken: "tok"}

	cfg.UnauthorizedSince = time.Now().UTC().Add(-(revokedAfter - time.Hour)).Format(time.RFC3339)
	if rejectedFor(cfg) > revokedAfter {
		t.Fatal("still inside the grace window; must keep capturing")
	}

	cfg.UnauthorizedSince = time.Now().UTC().Add(-(revokedAfter + time.Hour)).Format(time.RFC3339)
	if rejectedFor(cfg) <= revokedAfter {
		t.Fatal("past the grace window; should be dormant")
	}
	// Dormant is not uninstalled: the token stays so the agent can recover alone.
	if cfg.DeviceToken != "tok" {
		t.Fatal("going dormant must keep the token")
	}
}

// The clock going backwards (or a stamp from the future) must not read as a
// negative age and park the agent outside dormancy forever.
func TestFutureStampDoesNotWedge(t *testing.T) {
	withHome(t)
	cfg := &config.Config{UnauthorizedSince: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}
	if got := rejectedFor(cfg); got != 0 {
		t.Fatalf("future stamp should read as 0, got %v", got)
	}
}

func TestUnreadableStampResetsInsteadOfSulking(t *testing.T) {
	withHome(t)
	cfg := &config.Config{UnauthorizedSince: "not-a-timestamp"}
	if got := rejectedFor(cfg); got != 0 {
		t.Fatalf("unreadable stamp should read as 0, got %v", got)
	}
	if cfg.UnauthorizedSince != "" {
		t.Fatal("unreadable stamp should be cleared, not kept forever")
	}
}

// The rejection clock has to survive a restart, or an agent that reboots nightly
// could never accumulate the window and would never reach a real revocation.
func TestRejectionRunSurvivesRestart(t *testing.T) {
	withHome(t)
	cfg := &config.Config{DeviceToken: "tok"}
	recordAuth(cfg, ingest.ErrUnauthorized)

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.UnauthorizedSince != cfg.UnauthorizedSince {
		t.Fatalf("rejection clock lost across restart: %q", reloaded.UnauthorizedSince)
	}
	if reloaded.DeviceToken != "tok" {
		t.Fatal("token lost across restart")
	}
}

// The watchdog must never fire on a legitimately slow iteration. The longest
// one is a dormant tick (sleep + one probe), and a self-update tick (download +
// smoke test) also has to fit. If someone raises dormantInterval without
// raising loopStallTimeout, every dormant agent would restart itself in a loop.
func TestWatchdogWindowClearsTheSlowestIteration(t *testing.T) {
	if loopStallTimeout <= dormantInterval {
		t.Fatalf("loopStallTimeout (%s) must exceed dormantInterval (%s)",
			loopStallTimeout, dormantInterval)
	}
	// Headroom for the HTTP call at the end of a dormant tick.
	if loopStallTimeout < dormantInterval+5*time.Minute {
		t.Fatalf("loopStallTimeout (%s) leaves too little headroom over dormantInterval (%s)",
			loopStallTimeout, dormantInterval)
	}
	if watchdogPoll >= loopStallTimeout {
		t.Fatalf("watchdogPoll (%s) must be well under loopStallTimeout (%s)",
			watchdogPoll, loopStallTimeout)
	}
}

// Dormancy must be reachable but never quick: a day of unbroken refusals.
func TestGraceWindowIsLongEnoughToRideOutAnOutage(t *testing.T) {
	if revokedAfter < 12*time.Hour {
		t.Fatalf("revokedAfter (%s) is too short to survive a backend outage", revokedAfter)
	}
}
