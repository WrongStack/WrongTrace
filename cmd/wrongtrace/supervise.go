package main

import (
	"context"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// waitTimeout waits for wg, giving up after d. It reports whether every
// goroutine finished; shutdown uses it so a wedged component delays exit by at
// most d instead of hanging the daemon forever.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// awaitPollerDone waits for the session poller's completion signal, giving up
// after d. The poller runs its own loop rather than under superviseLoop (it must
// not be restarted or duplicated), so shutdown awaits it here: its in-flight
// poll and its final offset checkpoint must finish before the deferred Close
// calls tear the store down underneath it. A nil channel means no poller was
// started, which needs no wait.
func awaitPollerDone(done <-chan struct{}, d time.Duration) bool {
	if done == nil {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// supervisePolicy is the restart timing for one supervised component.
type supervisePolicy struct {
	// minDelay is the wait after the first failure and the value the backoff
	// resets to.
	minDelay time.Duration
	// maxDelay caps the ramp so a permanently broken component still retries
	// on a useful cadence.
	maxDelay time.Duration
	// healthyRun is how long an attempt must last to count as "it was
	// working", which resets the backoff. Without it a component that ran fine
	// for hours would restart at maxDelay after its first hiccup.
	healthyRun time.Duration
	// sleep is the ctx-aware wait between attempts. Production leaves it nil,
	// which means sleepCtx. A test sets it to observe the EXACT ramp the loop
	// requests: inferring the ramp from wall-clock gaps between attempt starts
	// also measures the attempt's own runtime and scheduler wakeup jitter, so a
	// correct ramp failed its assertion on a loaded machine (see
	// TestSuperviseLoop_BacksOffOnRepeatedFailure).
	sleep func(context.Context, time.Duration) bool
}

// wait performs one inter-attempt wait for this policy.
func (p supervisePolicy) wait(ctx context.Context, d time.Duration) bool {
	if p.sleep != nil {
		return p.sleep(ctx, d)
	}
	return sleepCtx(ctx, d)
}

// defaultSupervisePolicy is what the daemon's components run under. The values
// are separated from superviseLoop so tests can drive the same logic on a
// millisecond timescale instead of paying seconds of wall clock per assertion.
var defaultSupervisePolicy = supervisePolicy{
	minDelay:   1 * time.Second,
	maxDelay:   30 * time.Second,
	healthyRun: 60 * time.Second,
}

// superviseLoop runs fn repeatedly until ctx is cancelled, recovering from
// panics and backing off between attempts that fail.
//
// The backoff is the point. The per-component loops this replaced slept a
// fixed 1-2s, which is right for a transient fault and wrong for a permanent
// one: a port held by another process made the HTTP loop log an identical
// "restarting in 2s" line roughly every two seconds, forever, burying every
// other line the operator needed. Ramping to maxDelay turns that into a couple
// of lines a minute while still recovering promptly once the obstruction
// clears.
//
// Every wait is ctx-aware: shutdown must not block for the length of whatever
// backoff happened to be in effect.
func superviseLoop(ctx context.Context, name string, fn func() error) {
	superviseLoopWith(ctx, name, defaultSupervisePolicy, fn)
}

func superviseLoopWith(ctx context.Context, name string, pol supervisePolicy, fn func() error) {
	delay := pol.minDelay
	attempt := 0

	for {
		if ctx.Err() != nil {
			return
		}

		started := time.Now()
		err, panicked := runGuarded(name, fn)
		ranFor := time.Since(started)

		if ctx.Err() != nil {
			return
		}

		if err == nil && !panicked {
			// A clean return is normal for these components (the HTTP server
			// returns nil on graceful close); loop straight back with the
			// backoff reset.
			delay, attempt = pol.minDelay, 0
			if !pol.wait(ctx, pol.minDelay) {
				return
			}
			continue
		}

		if ranFor >= pol.healthyRun {
			delay, attempt = pol.minDelay, 0
		}
		attempt++

		if err != nil {
			log.Printf("%s: %v (attempt %d failed after %s, retrying in %s)",
				name, err, attempt, ranFor.Round(time.Millisecond), delay)
		}

		if !pol.wait(ctx, delay) {
			return
		}
		if delay *= 2; delay > pol.maxDelay {
			delay = pol.maxDelay
		}
	}
}

// runGuarded calls fn, converting a panic into panicked=true so the caller can
// back off on it exactly as it does on a returned error. The stack is logged
// here because it is worthless once unwound.
func runGuarded(name string, fn func() error) (err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			log.Printf("PANIC in %s: %v\n%s", name, r, debug.Stack())
		}
	}()
	return fn(), false
}

// sleepCtx waits for d, or until ctx is cancelled. It reports false when the
// wait was cut short by cancellation, which every caller treats as "stop".
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
