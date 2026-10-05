package profiler

import (
	"errors"
	"math"
	"testing"
)

type failingMetadata struct{ err error }

func (m failingMetadata) MarshalJSON() ([]byte, error) { return nil, m.err }

func TestReportMetadataErrorsAbortPublication(t *testing.T) {
	callbackCount := 0
	c := NewCollector(Config{OnTrace: func(TraceEvent) { callbackCount++ }})
	for _, metadata := range []map[string]interface{}{nil, {}, {"valid": "fixture"}} {
		if _, err := c.IngestReport(ProfilerReportPayload{Metadata: metadata}); err != nil {
			t.Fatal(err)
		}
	}
	cycle := map[string]interface{}{}
	cycle["cycle"] = cycle
	sentinel := errors.New("fixture encoding failed")
	for _, metadata := range []map[string]interface{}{{"bad": make(chan int)}, {"bad": func() {}}, {"bad": math.NaN()}, cycle, {"bad": failingMetadata{sentinel}}} {
		ev, err := c.IngestReport(ProfilerReportPayload{Metadata: metadata})
		if err == nil || ev.TraceID != "" {
			t.Fatalf("invalid metadata accepted: ev=%+v err=%v", ev, err)
		}
		if _, ok := metadata["bad"].(failingMetadata); ok && !errors.Is(err, sentinel) {
			t.Fatal("marshal error identity lost")
		}
	}
	recent, err := c.Recent(10)
	if err != nil || len(recent) != 3 || callbackCount != 3 {
		t.Fatalf("invalid event published: recent=%d callbacks=%d err=%v", len(recent), callbackCount, err)
	}
}
