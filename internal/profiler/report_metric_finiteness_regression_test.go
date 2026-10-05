package profiler

import (
	"encoding/json"
	"fmt"
	"github.com/wrongstack/wrongtrace/internal/db"
	"math"
	"testing"
)

func TestReportMetricsRejectNonfiniteBeforePublication(t *testing.T) {
	callbacks, stores := 0, 0
	c := NewCollector(Config{OnTrace: func(TraceEvent) { callbacks++ }, GetStore: func() *db.Store { stores++; return nil }})
	for _, field := range []string{"duration", "cpu"} {
		for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			p := ProfilerReportPayload{}
			if field == "duration" {
				p.DurationMs = v
			} else {
				p.CPUUsagePct = v
			}
			ev, err := c.IngestReport(p)
			if err == nil || ev.TraceID != "" || callbacks != 0 || stores != 0 {
				t.Fatalf("invalid published: %v %v %d %d", ev, err, callbacks, stores)
			}
		}
	}
	recent, err := c.Recent(10)
	if err != nil || len(recent) != 0 {
		t.Fatal("invalid recent event")
	}
	for _, v := range []float64{0, -1, 3.5, math.MaxFloat64} {
		ev, err := c.IngestReport(ProfilerReportPayload{DurationMs: v, CPUUsagePct: v})
		_, marshalErr := json.Marshal(ev)
		if err != nil || marshalErr != nil || ev.DurationMs != v || ev.CPUUsagePct != v || ev.StatusCode != 200 {
			t.Fatalf("finite metric changed: %v %v", err, marshalErr)
		}
	}
	recent, err = c.Recent(10)
	if err != nil || len(recent) != 4 || callbacks != 4 || stores != 4 {
		t.Fatalf("recovery counts: %d %d %d %v", len(recent), callbacks, stores, err)
	}
	fmt.Println("EXPECTED: reject six nonfinite values before publication; preserve zero/negative/fractional/max finite and recover | ACTUAL: passed")

}
