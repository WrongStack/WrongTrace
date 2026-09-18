package core

import (
	"sync"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/ipc"
)

// Concurrent dashboard fetches share one snapshot build, a generation bump
// (any run/event write) invalidates it, and the cached copy never serves a
// stale ActiveRuns list.
func TestMetricsCacheCoalescesAndInvalidates(t *testing.T) {
	e, _, _ := newAtlasTestEngine(t)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			switch i % 3 {
			case 0:
				_, err = e.Metrics()
			case 1:
				_, err = e.ThrashingRows()
			default:
				_, err = e.ModelRows()
			}
			if err != nil {
				t.Errorf("call %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	before, err := e.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if before.Overview.TotalRuns != 0 || len(before.ActiveRuns) != 0 {
		t.Fatalf("unexpected initial snapshot: %+v", before.Overview)
	}

	if err := e.ReportRun(ipc.TelemetryReport{RunID: "run-1", AgentName: "a", ModelName: "m"}); err != nil {
		t.Fatal(err)
	}
	after, err := e.Metrics()
	if err != nil {
		t.Fatal(err)
	}
	if after.Overview.TotalRuns != 1 {
		t.Errorf("TotalRuns after ReportRun = %d, want 1 (generation bump must invalidate)", after.Overview.TotalRuns)
	}
	if len(after.ActiveRuns) != 1 {
		t.Errorf("ActiveRuns = %d, want 1", len(after.ActiveRuns))
	}
	if len(e.metricsCalls) != 0 {
		t.Errorf("in-flight builds leaked: %d", len(e.metricsCalls))
	}
}
