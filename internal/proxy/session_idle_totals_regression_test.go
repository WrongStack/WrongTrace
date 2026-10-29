package proxy

import (
	"testing"
	"time"
)

// The store replaces a run's totals on every report, so a run reported under
// an explicit session ID must keep accumulating across an idle pause: a
// tracker restarted from zero under the same run ID erased the earlier spend.
// Inferred sessions still split after the idle window, with a fresh run ID.
func TestGetOrCreateSession_ExplicitIDKeepsTotalsAcrossIdle(t *testing.T) {
	p := &GatewayProxy{}
	p.getOrCreateSession("sess-A", "", 120, 60, 0.5)
	p.sessions["sess-A"].lastSeen = time.Now().Add(-11 * time.Minute)
	run, prompt, completion, cost := p.getOrCreateSession("sess-A", "", 120, 60, 0.5)
	if run != "sess-A" || prompt != 240 || completion != 120 || cost != 1.0 {
		t.Fatalf("explicit session after idle = %s %d/%d $%v; want sess-A 240/120 $1", run, prompt, completion, cost)
	}

	// Control: an inferred session key starts a new run after the window.
	first, _, _, _ := p.getOrCreateSession("", "key-B", 10, 5, 0)
	p.sessions["key-B"].lastSeen = time.Now().Add(-11 * time.Minute)
	second, prompt, _, _ := p.getOrCreateSession("", "key-B", 10, 5, 0)
	if second == first || prompt != 10 {
		t.Fatalf("inferred session after idle = %s prompt=%d; want a new run with prompt 10 (first run %s)", second, prompt, first)
	}
}
