// Package supervise keeps `avora-agent run` alive on machines that have no real
// supervisor.
//
// Task Scheduler (Windows) and launchd (macOS) restart the agent when it exits.
// But on locked-down Windows machines creating a scheduled task is denied
// (0x80070005), and the installer falls back to the per-user Run key, which
// starts the agent once at logon and never again. There, any exit - a crash,
// the run loop's own watchdog ending a frozen process, a failed self-update -
// left the machine dark until the next login. On those machines the Run key now
// starts `avora-agent supervise`, a few dozen lines that run the agent as a
// child and start it again whenever it stops.
package supervise

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

const (
	firstDelay = 2 * time.Second
	maxDelay   = 60 * time.Second
	// A child that stayed up this long was healthy; the next failure starts the
	// backoff from the beginning instead of waiting the full minute.
	healthyRun = 10 * time.Minute
)

// NextDelay is how long to wait before restarting a child that ran for `lived`,
// given the previous wait. Quick failures double the wait up to maxDelay so a
// crash loop cannot spin the CPU; a long, healthy run resets it.
func NextDelay(prev, lived time.Duration) time.Duration {
	if lived >= healthyRun || prev <= 0 {
		return firstDelay
	}
	next := prev * 2
	if next > maxDelay {
		return maxDelay
	}
	return next
}

// Starter launches one child process and waits for it to exit.
type Starter func() error

// Loop runs start forever, sleeping NextDelay between runs. It never returns:
// the supervisor's only job is to outlive the agent.
func Loop(start Starter, sleep func(time.Duration)) {
	delay := time.Duration(0)
	for {
		began := time.Now()
		err := start()
		lived := time.Since(began)
		delay = NextDelay(delay, lived)
		fmt.Printf("  supervise: agent exited after %s (%v); restarting in %s\n",
			lived.Round(time.Second), err, delay)
		sleep(delay)
	}
}

// ChildStarter runs `<execPath> run`, re-reading execPath each time so a
// self-update that swapped the binary is picked up on the next start.
func ChildStarter(execPath string, hide func(*exec.Cmd)) Starter {
	return func() error {
		cmd := exec.Command(execPath, "run")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if hide != nil {
			hide(cmd)
		}
		return cmd.Run()
	}
}
