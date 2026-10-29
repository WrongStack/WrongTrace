package main

import (
	"testing"
	"time"
)

// awaitPollerDone is the shutdown-side half of the session-poller fix: runStart
// calls it before its deferred Closes, so a poll in flight (or the poller's
// final offset checkpoint) cannot be writing into a store that is already
// closed. It must be bounded, so a wedged poller delays exit by at most the
// grace period instead of hanging the daemon.
func TestAwaitPollerDone(t *testing.T) {
	// No poller was started: nothing to wait for.
	if !awaitPollerDone(nil, time.Second) {
		t.Error("a nil completion channel must need no wait")
	}

	// Already finished: reported immediately.
	closed := make(chan struct{})
	close(closed)
	if !awaitPollerDone(closed, time.Second) {
		t.Error("a closed channel must be reported as finished")
	}

	// Never finishes: bounded, and reported false rather than hanging shutdown.
	open := make(chan struct{})
	start := time.Now()
	if awaitPollerDone(open, 20*time.Millisecond) {
		t.Error("a never-closing channel must report a timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("await took %s; it must be bounded by the grace period", elapsed)
	}

	// Finishes during the wait: reported as finished.
	late := make(chan struct{})
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(late)
	}()
	if !awaitPollerDone(late, 2*time.Second) {
		t.Error("a channel that closes during the wait must be reported as finished")
	}
}
