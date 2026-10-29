package ingest

import (
	"context"
	"testing"
	"time"
)

// StartPolling's completion signal is the contract the daemon's shutdown relies
// on: runStart cancels the poller, awaits this channel, and only then lets its
// deferred Closes tear down the store, watcher and AST engine. Before the fix
// the call returned nothing, so the poller could still be inside PollOnce — or
// its final saveOffsets — while the store was already closed.
func TestStartPolling_CompletionSignalClosesOnCancel(t *testing.T) {
	sw := NewSessionWatcher(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := sw.StartPolling(ctx, time.Millisecond)

	// Control: a running poller must not report completion.
	select {
	case <-done:
		t.Fatal("the completion signal closed before the context was cancelled")
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the completion signal never closed after cancellation: shutdown cannot wait for the poller")
	}
}

// The signal must be observable repeatedly and must not close early: an
// awaiting shutdown may select on it more than once, and a premature close
// would let the Closes run underneath a still-running poller.
func TestStartPolling_CompletionSignalStaysClosedAfterCancel(t *testing.T) {
	sw := NewSessionWatcher(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := sw.StartPolling(ctx, time.Millisecond)
	cancel()

	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("await %d did not observe completion", i)
		}
	}
}
