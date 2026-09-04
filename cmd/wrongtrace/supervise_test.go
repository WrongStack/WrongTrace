package main

import (
	"context"
	"errors"
	"io"
	"log"
	"sync/atomic"
	"testing"
	"time"
)

// testPolicy runs the real supervision logic on a millisecond timescale. The
// production policy is measured in seconds; driving these assertions at that
// scale would add half a minute of wall clock to the suite for no extra
// coverage, since only the ratios between the delays are under test.
var testPolicy = supervisePolicy{
	minDelay:   20 * time.Millisecond,
	maxDelay:   200 * time.Millisecond,
	healthyRun: 100 * time.Millisecond,
}

// silenceLog mutes the supervisor's restart logging for the duration of a test.
// superviseLoop is deliberately chatty; these tests drive it into failure on
// purpose and the output is noise.
func silenceLog(t *testing.T) {
	t.Helper()
	prev := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prev) })
}

// runSupervised starts superviseLoopWith on testPolicy and waits for it to
// return, failing the test if it never does.
func runSupervised(t *testing.T, ctx context.Context, fn func() error) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		superviseLoopWith(ctx, "test", testPolicy, fn)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("superviseLoop did not return after cancellation")
	}
}

// TestSuperviseLoop_RestartsOnError is the core contract: a component that
// fails must be restarted rather than left dead.
func TestSuperviseLoop_RestartsOnError(t *testing.T) {
	silenceLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	runSupervised(t, ctx, func() error {
		if calls.Add(1) >= 3 {
			cancel()
		}
		return errors.New("boom")
	})

	if got := calls.Load(); got < 3 {
		t.Errorf("fn ran %d times, want at least 3 restarts", got)
	}
}

// TestSuperviseLoop_RecoversFromPanic: a panicking component must be restarted
// like any other failure, never crash the daemon.
func TestSuperviseLoop_RecoversFromPanic(t *testing.T) {
	silenceLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	runSupervised(t, ctx, func() error {
		if calls.Add(1) >= 2 {
			cancel()
			return nil
		}
		panic("component exploded")
	})

	if got := calls.Load(); got < 2 {
		t.Errorf("fn ran %d times, want the panicking component to be restarted", got)
	}
}

// TestSuperviseLoop_BacksOffOnRepeatedFailure guards the reason the shared
// supervisor exists. The per-component loops it replaced slept a fixed 1-2s,
// so a permanent fault -- a port held by another process is the real case --
// logged an identical restart line every couple of seconds forever and buried
// every other line in the log. Consecutive failures must ramp the delay.
func TestSuperviseLoop_BacksOffOnRepeatedFailure(t *testing.T) {
	silenceLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var starts []time.Time
	runSupervised(t, ctx, func() error {
		starts = append(starts, time.Now())
		if len(starts) >= 4 {
			cancel()
		}
		return errors.New("permanent")
	})

	if len(starts) < 4 {
		t.Fatalf("only %d attempts recorded, want 4", len(starts))
	}
	// Waits between attempts 1->2, 2->3, 3->4 must each roughly double.
	var gaps []time.Duration
	for i := 1; i < 4; i++ {
		gaps = append(gaps, starts[i].Sub(starts[i-1]))
	}
	for i := 1; i < len(gaps); i++ {
		if gaps[i] <= gaps[i-1] {
			t.Errorf("wait before attempt %d was %s, not longer than the previous %s; "+
				"repeated failures are not backing off", i+2, gaps[i], gaps[i-1])
		}
	}
	if gaps[len(gaps)-1] < 2*testPolicy.minDelay {
		t.Errorf("last wait was only %s; expected the delay to have ramped past the %s floor",
			gaps[len(gaps)-1], testPolicy.minDelay)
	}
}

// TestSuperviseLoop_CapsBackoff: the ramp must stop at maxDelay so a
// permanently broken component still retries on a useful cadence instead of
// drifting toward never trying again.
func TestSuperviseLoop_CapsBackoff(t *testing.T) {
	silenceLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var starts []time.Time
	runSupervised(t, ctx, func() error {
		starts = append(starts, time.Now())
		if len(starts) >= 8 {
			cancel()
		}
		return errors.New("permanent")
	})

	if len(starts) < 8 {
		t.Fatalf("only %d attempts recorded, want 8", len(starts))
	}
	// By attempt 8 the raw doubling would be far past maxDelay; the observed
	// wait must be near the cap. Generous upper bound: scheduler jitter under
	// -race is real, but an uncapped ramp would be several times larger.
	last := starts[7].Sub(starts[6])
	if last > 3*testPolicy.maxDelay {
		t.Errorf("wait before attempt 8 was %s, well past the %s cap; the backoff is uncapped",
			last, testPolicy.maxDelay)
	}
}

// TestSuperviseLoop_ResetsBackoffAfterHealthyRun pins the other half of the
// backoff rule: a component that ran fine for a while and then failed must
// restart promptly, not at whatever delay the previous failure streak reached.
func TestSuperviseLoop_ResetsBackoffAfterHealthyRun(t *testing.T) {
	silenceLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		attempt     int
		healthyEnd  time.Time
		restartGap  time.Duration
		rampedGap   time.Duration
		lastFailEnd time.Time
	)

	runSupervised(t, ctx, func() error {
		attempt++
		switch {
		case attempt <= 4:
			// Fail fast repeatedly so the delay ramps well past the floor.
			lastFailEnd = time.Now()
			return errors.New("early failure")
		case attempt == 5:
			// Measure how long the ramped-up wait actually was, then run long
			// enough to count as healthy before failing.
			rampedGap = time.Since(lastFailEnd)
			time.Sleep(testPolicy.healthyRun + 20*time.Millisecond)
			healthyEnd = time.Now()
			return errors.New("failure after a healthy run")
		default:
			restartGap = time.Since(healthyEnd)
			cancel()
			return nil
		}
	})

	if rampedGap == 0 || restartGap == 0 {
		t.Fatalf("supervisor did not reach the measured attempts (ramped=%s restart=%s)",
			rampedGap, restartGap)
	}
	if restartGap >= rampedGap {
		t.Errorf("restart after a healthy run waited %s, no shorter than the ramped %s; "+
			"a run lasting longer than healthyRun (%s) must reset the backoff",
			restartGap, rampedGap, testPolicy.healthyRun)
	}
}

// TestSuperviseLoop_CancellationIsPrompt guards shutdown latency. Every wait in
// the loop is ctx-aware for this reason: with a plain time.Sleep, a daemon
// shutting down while the supervisor sat in a 30s backoff would hang for the
// remainder of it.
func TestSuperviseLoop_CancellationIsPrompt(t *testing.T) {
	silenceLog(t)

	// A long cap so cancellation, not expiry, is what ends the wait.
	pol := supervisePolicy{minDelay: 5 * time.Second, maxDelay: 30 * time.Second, healthyRun: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		var once bool
		superviseLoopWith(ctx, "test", pol, func() error {
			if !once {
				once = true
				close(started)
			}
			return errors.New("fail immediately")
		})
		close(done)
	}()

	<-started
	time.Sleep(100 * time.Millisecond) // let the loop settle into its wait
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("superviseLoop did not return within 2s of cancellation despite a %s "+
			"backoff in flight; a wait is not context-aware", pol.minDelay)
	}
}

// TestSleepCtx_ReportsCancellation covers the helper directly: it must
// distinguish "the wait completed" from "we were told to stop".
func TestSleepCtx_ReportsCancellation(t *testing.T) {
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Error("sleepCtx reported cancellation for a wait that completed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(ctx, time.Hour) {
		t.Error("sleepCtx reported success on an already-cancelled context")
	}
	// A zero duration must still respect cancellation rather than blindly
	// returning success.
	if sleepCtx(ctx, 0) {
		t.Error("sleepCtx(ctx, 0) reported success on a cancelled context")
	}
}

// TestDefaultSupervisePolicy_IsSane keeps the production values coherent: the
// tests above run on testPolicy, so a nonsense default would otherwise slip
// through unnoticed.
func TestDefaultSupervisePolicy_IsSane(t *testing.T) {
	p := defaultSupervisePolicy
	if p.minDelay <= 0 {
		t.Errorf("minDelay = %s, must be positive", p.minDelay)
	}
	if p.maxDelay < p.minDelay {
		t.Errorf("maxDelay %s is below minDelay %s; the backoff would never ramp",
			p.maxDelay, p.minDelay)
	}
	if p.healthyRun <= p.maxDelay {
		t.Errorf("healthyRun %s is not longer than maxDelay %s; a component could be "+
			"credited as healthy for a run no longer than one backoff wait",
			p.healthyRun, p.maxDelay)
	}
}
