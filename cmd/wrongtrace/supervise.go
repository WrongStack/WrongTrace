package main

import (
	"context"
	"log"
	"runtime/debug"
	"time"
)

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
			if !sleepCtx(ctx, pol.minDelay) {
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

		if !sleepCtx(ctx, delay) {
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
