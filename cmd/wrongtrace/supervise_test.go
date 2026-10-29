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
	runSupervisedWith(t, ctx, testPolicy, fn)
}

// runSupervisedWith is runSupervised on an explicit policy.
func runSupervisedWith(t *testing.T, ctx context.Context, pol supervisePolicy, fn func() error) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		superviseLoopWith(ctx, "test", pol, fn)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("superviseLoop did not return after cancellation")
	}
}

// captureWaits returns testPolicy with its inter-attempt wait replaced by a
// recorder that returns immediately, so the ramp can be asserted on the delays
// the loop REQUESTED -- the actual contract.
//
// The previous assertions measured the wall-clock gap between attempt STARTS,
// which is (the requested wait) + (the attempt's own runtime) + (scheduler
// wakeup jitter). Nothing bounds the last two, so a correct ramp failed its
// assertion as soon as one attempt outlasted the next nominal delay: measured
// on a loaded machine, a 20ms->40ms step read as 46.8ms followed by 40.2ms, and
// a first attempt taking 50ms produces [71.6ms 40.5ms 80.3ms] for the correct
// 20ms/40ms/80ms ramp. The recorded waits are appended from the supervisor's
// goroutine but read only after runSupervisedWith returns, which is ordered by
// the done channel.
func captureWaits(t *testing.T, waits *[]time.Duration) supervisePolicy {
	t.Helper()
	pol := testPolicy
	pol.sleep = func(_ context.Context, d time.Duration) bool {
		*waits = append(*waits, d)
		return true
	}
	return pol
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

	var waits []time.Duration
	var attempts atomic.Int32
	runSupervisedWith(t, ctx, captureWaits(t, &waits), func() error {
		if attempts.Add(1) >= 4 {
			cancel()
		}
		return errors.New("permanent")
	})

	// The contract is the ramp the loop REQUESTS: each consecutive failure
	// doubles the wait. Asserting it on the requested delays is what makes this
	// deterministic -- see captureWaits for why the wall-clock gaps it replaced
	// could fail a ramp that was correct.
	want := []time.Duration{testPolicy.minDelay, 2 * testPolicy.minDelay, 4 * testPolicy.minDelay}
	if len(waits) < len(want) {
		t.Fatalf("supervisor requested %d waits (%v), want at least %d: %v",
			len(waits), waits, len(want), want)
	}
	for i, w := range want {
		if waits[i] != w {
			t.Errorf("wait %d = %s, want %s: consecutive failures must double the delay",
				i+1, waits[i], w)
		}
	}
}

// TestSuperviseLoop_CapsBackoff: the ramp must stop at maxDelay so a
// permanently broken component still retries on a useful cadence instead of
// drifting toward never trying again.
func TestSuperviseLoop_CapsBackoff(t *testing.T) {
	silenceLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var waits []time.Duration
	var attempts atomic.Int32
	runSupervisedWith(t, ctx, captureWaits(t, &waits), func() error {
		if attempts.Add(1) >= 10 {
			cancel()
		}
		return errors.New("permanent")
	})

	// The ramp doubles from the floor and then holds at the cap. With a 20ms
	// floor and a 200ms cap the requested waits are 20, 40, 80, 160, then 200
	// for every further failure.
	if len(waits) < 5 {
		t.Fatalf("supervisor requested %d waits (%v), want at least 5", len(waits), waits)
	}
	delay := testPolicy.minDelay
	for i := 0; i < 4; i++ {
		if waits[i] != delay {
			t.Errorf("wait %d = %s, want %s: the ramp must double from the floor", i+1, waits[i], delay)
		}
		delay *= 2
	}
	for i := 4; i < len(waits); i++ {
		if waits[i] != testPolicy.maxDelay {
			t.Errorf("wait %d = %s, want the %s cap: an uncapped ramp drifts toward never retrying",
				i+1, waits[i], testPolicy.maxDelay)
		}
	}
	if waits[len(waits)-1] != testPolicy.maxDelay {
		t.Errorf("last wait = %s, want the %s cap", waits[len(waits)-1], testPolicy.maxDelay)
	}
}

// TestSuperviseLoop_ResetsBackoffAfterHealthyRun pins the other half of the
// backoff rule: a component that ran fine for a while and then failed must
// restart promptly, not at whatever delay the previous failure streak reached.
func TestSuperviseLoop_ResetsBackoffAfterHealthyRun(t *testing.T) {
	silenceLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var waits []time.Duration
	var attempt int

	runSupervisedWith(t, ctx, captureWaits(t, &waits), func() error {
		attempt++
		switch {
		case attempt <= 4:
			// Fail fast repeatedly so the delay ramps well past the floor.
			return errors.New("early failure")
		case attempt == 5:
			// Run longer than healthyRun: this attempt counts as "it was working".
			time.Sleep(testPolicy.healthyRun + 20*time.Millisecond)
			return errors.New("failure after a healthy run")
		default:
			cancel()
			return nil
		}
	})

	// waits[0..3] are the ramped waits before attempts 2..5; waits[4] is the wait
	// the healthy run must reset to the floor.
	if len(waits) < 5 {
		t.Fatalf("supervisor requested %d waits (%v), want at least 5", len(waits), waits)
	}
	if waits[3] <= waits[2] {
		t.Errorf("the streak must ramp before the healthy run: waits=%v", waits)
	}
	if waits[4] != testPolicy.minDelay {
		t.Errorf("wait after a healthy run = %s, want the %s floor: a run lasting longer than "+
			"healthyRun (%s) must reset the backoff", waits[4], testPolicy.minDelay, testPolicy.healthyRun)
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
