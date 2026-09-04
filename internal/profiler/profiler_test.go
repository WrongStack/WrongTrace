package profiler

import (
	"path/filepath"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
)

func TestProfilerCollector(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test-profiler.db")
	store, err := db.Open(tmp)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var capturedEvents []TraceEvent
	collector := NewCollector(Config{
		Store: store,
		OnTrace: func(ev TraceEvent) {
			capturedEvents = append(capturedEvents, ev)
		},
	})

	// 1. Ingest custom report
	ev, err := collector.IngestReport(ProfilerReportPayload{
		ServiceName:   "backend-api",
		NodeSignature: "func:auth.go::ValidateToken",
		FilePath:      "src/auth.go",
		DurationMs:    45.2,
		CPUUsagePct:   12.5,
		MemoryBytes:   1024 * 1024,
		StatusCode:    200,
		ProfilerType:  "pprof",
	})
	if err != nil {
		t.Fatalf("ingest report: %v", err)
	}
	if ev.TraceID == "" {
		t.Errorf("expected trace ID, got empty")
	}

	// 2. Ingest OTLP JSON payload
	otlpJSON := `{
		"resourceSpans": [
			{
				"resource": {
					"attributes": [
						{"key": "service.name", "value": {"stringValue": "payment-svc"}}
					]
				},
				"scopeSpans": [
					{
						"spans": [
							{
								"traceId": "trace-12345678",
								"spanId": "span-123",
								"name": "ProcessPayment",
								"startTimeUnixNano": "1700000000000000000",
								"endTimeUnixNano":   "1700000000050000000",
								"attributes": [
									{"key": "code.filepath", "value": {"stringValue": "src/pay.go"}},
									{"key": "code.function", "value": {"stringValue": "ProcessPayment"}},
									{"key": "http.status_code", "value": {"intValue": "200"}}
								]
							}
						]
					}
				]
			}
		]
	}`

	count, err := collector.IngestOTLP([]byte(otlpJSON))
	if err != nil {
		t.Fatalf("ingest otlp: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 span ingested, got %d", count)
	}

	// 3. Hotspots & Overview
	hotspots, err := collector.Hotspots(10)
	if err != nil {
		t.Fatalf("hotspots: %v", err)
	}
	if len(hotspots) == 0 {
		t.Errorf("expected hotspots, got none")
	}

	overview, err := collector.Overview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.TotalTraces != 2 {
		t.Errorf("expected 2 total traces, got %d", overview.TotalTraces)
	}

	// 4. Recent
	recent, err := collector.Recent(5)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 2 {
		t.Errorf("expected 2 recent traces, got %d", len(recent))
	}

	// 5. Ingest error payload & fallback
	errEv, _ := collector.IngestReport(ProfilerReportPayload{
		ErrorMsg: "syntax error",
	})
	if errEv.StatusCode != 500 {
		t.Errorf("expected status 500 for error payload, got %d", errEv.StatusCode)
	}

	// 6. Malformed OTLP
	if _, err := collector.IngestOTLP([]byte(`{invalid}`)); err == nil {
		t.Errorf("expected error for invalid OTLP JSON")
	}

	// 7. Dynamic GetStore config
	collectorWithFn := NewCollector(Config{
		GetStore: func() *db.Store { return store },
	})
	if collectorWithFn.store() != store {
		t.Errorf("expected store from GetStore callback")
	}
}
