// Package singleton makes sure only one copy of an agent role runs per user.
//
// Two copies of `run` would race over the same device sequence counter and
// offline queue, and two supervisors would each keep their own copy of `run`
// alive. A scheduled task's IgnoreNew policy prevented that before, but a
// supervisor, a self-repair that starts the task again, and a self-update
// restart can all now start a second copy, so every role takes a lock first.
package singleton

import (
	"errors"
	"time"
)

// ErrHeld means another process already holds the lock.
var ErrHeld = errors.New("another copy is already running")

const retryEvery = 2 * time.Second

// Acquire takes the named lock, retrying for up to `wait` so a process that is
// replacing an older copy (a self-update restart) gets in once the old one has
// exited. The returned release must be kept alive for the life of the process.
func Acquire(name string, wait time.Duration) (release func(), err error) {
	deadline := time.Now().Add(wait)
	for {
		release, err = tryAcquire(name)
		if err == nil || !errors.Is(err, ErrHeld) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(retryEvery)
	}
}
