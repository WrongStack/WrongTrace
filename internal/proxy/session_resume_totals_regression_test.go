package proxy

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ipc"
)

// storeLikeReporter mimics Store.UpsertRun: each report REPLACES the run's
// totals; RunTotals reads them back (the core.Engine contract).
type storeLikeReporter struct {
	mu   sync.Mutex
	runs map[string]ipc.TelemetryReport
}

func (r *storeLikeReporter) ReportRun(p ipc.TelemetryReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[p.RunID] = p
	return nil
}

func (r *storeLikeReporter) RunTotals(id string) (int64, int64, float64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.runs[id]
	return p.PromptTokens, p.CompletionTokens, p.CostUSD, ok
}

// An explicit run the proxy no longer tracks in memory -- evicted under the
// session cap, or from before a daemon restart -- resumes from its stored
// totals. The store replaces totals on every report, so restarting the
// tracker from zero erased the run's earlier spend.
func TestRecordRun_ExplicitRunTotalsSurviveEvictionAndRestart(t *testing.T) {
	rep := &storeLikeReporter{runs: map[string]ipc.TelemetryReport{}}
	report := func(p *GatewayProxy, run string, pt, ct int64, cost float64) {
		p.recordRun("m", "prov", "agent", "", "", "", run, "", pt, ct, cost, "")
	}

	// 1. Eviction under the session cap.
	p := &GatewayProxy{cfg: Config{Reporter: rep}}
	report(p, "sess-A", 100, 50, 1.0)
	p.sessions["sess-A"].lastSeen = time.Now().Add(-20 * time.Minute)
	for i := 0; i < 251; i++ {
		report(p, fmt.Sprintf("other-%d", i), 1, 1, 0)
	}
	if p.hasSession("sess-A") {
		t.Fatal("setup: sess-A was not evicted")
	}
	report(p, "sess-A", 10, 5, 0.1)
	if got := rep.runs["sess-A"]; got.PromptTokens != 110 || got.CompletionTokens != 55 {
		t.Fatalf("after eviction stored run = %d/%d; want 110/55", got.PromptTokens, got.CompletionTokens)
	}

	// 2. Daemon restart: a fresh proxy resumes the stored run.
	p2 := &GatewayProxy{cfg: Config{Reporter: rep}}
	report(p2, "sess-A", 1, 1, 0)
	if got := rep.runs["sess-A"]; got.PromptTokens != 111 {
		t.Fatalf("after restart stored prompt = %d; want 111", got.PromptTokens)
	}

	// 3. Edge: a brand-new explicit run starts from its own tokens only.
	report(p2, "sess-new", 7, 3, 0)
	if got := rep.runs["sess-new"]; got.PromptTokens != 7 {
		t.Fatalf("new run prompt = %d; want 7", got.PromptTokens)
	}

	// 4. Edge: inferred sessions get a fresh random run id and no seeding.
	p2.recordRun("m", "prov", "agent", "", "", "", "", "key-X", 4, 4, 0, "")
	for id, r := range rep.runs {
		if r.PromptTokens == 4 && id == "key-X" {
			t.Fatal("inferred session reused its session key as run id")
		}
	}
}
