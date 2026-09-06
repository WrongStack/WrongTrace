package profiler

import (
	"path/filepath"
	"reflect"
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

// TestIngestOTLP_PreservesNonStringAttributeMetadata pins the metadata type
// contract of Collector.IngestOTLP: OTLP attributes are oneof-typed, and
// TraceEvent.Metadata — the OnTrace broadcast payload and the metadata_json
// column — must carry each attribute's actual Go type (bool, int64, float64,
// string). Storing OTLPVal.StringValue alone turned every numeric/boolean
// attribute (http.status_code, cpu.usage_pct, feature.enabled) into an empty
// string even though the same record's typed fields were populated from the
// very same attributes.
func TestIngestOTLP_PreservesNonStringAttributeMetadata(t *testing.T) {
	var captured []TraceEvent
	collector := NewCollector(Config{
		OnTrace: func(ev TraceEvent) { captured = append(captured, ev) },
	})

	payload := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "checkout"}}]},
			"scopeSpans": [{"spans": [{
				"traceId": "trace-1",
				"spanId": "span-1",
				"name": "controller.handle",
				"attributes": [
					{"key": "feature.enabled", "value": {"boolValue": true}},
					{"key": "cpu.usage_pct", "value": {"doubleValue": 77.5}},
					{"key": "http.status_code", "value": {"intValue": "404"}},
					{"key": "client.id", "value": {"intValue": "42"}},
					{"key": "span.kind", "value": {"stringValue": "web"}}
				]
			}]}]
		}]
	}`

	count, err := collector.IngestOTLP([]byte(payload))
	if err != nil {
		t.Fatalf("IngestOTLP: %v", err)
	}
	if count != 1 || len(captured) != 1 {
		t.Fatalf("expected 1 span captured, got count=%d captured=%d", count, len(captured))
	}

	meta := captured[0].Metadata
	for _, c := range []struct {
		key  string
		want any
	}{
		{"feature.enabled", true},
		{"cpu.usage_pct", 77.5},
		{"http.status_code", int64(404)},
		{"client.id", int64(42)},
		{"span.kind", "web"},
	} {
		got, ok := meta[c.key]
		if !ok {
			t.Errorf("metadata[%q] missing (have %v)", c.key, meta)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("metadata[%q] = %#v (%T), want %#v (%T)", c.key, got, got, c.want, c.want)
		}
	}
}

// TestIngestOTLP_SharedTraceIDKeepsAllSpans pins the persistence contract
// for multi-span OTLP traces: every span of a trace shares one traceId, but
// runtime_traces.trace_id is the PRIMARY KEY and InsertTrace is a plain
// INSERT, so persisted rows must be keyed per span (traceId-spanId;
// span-spanId when only the spanId exists; randomID when neither). Keying
// by the group ID made spans 2..N fail the UNIQUE constraint and vanish —
// the error was only logged while IngestOTLP still returned the full
// count, so ProfilerOverview/RecentTraces/Hotspots undercounted silently.
// The broadcast event keeps the raw traceId; only the row key changes.
func TestIngestOTLP_SharedTraceIDKeepsAllSpans(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var captured []TraceEvent
	collector := NewCollector(Config{
		Store:   store,
		OnTrace: func(ev TraceEvent) { captured = append(captured, ev) },
	})

	payload := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "checkout"}}]},
			"scopeSpans": [{"spans": [
				{
					"traceId": "trace-shared", "spanId": "span-1",
					"name": "controller.handle",
					"attributes": [
						{"key": "code.filepath", "value": {"stringValue": "src/controller.go"}},
						{"key": "code.function", "value": {"stringValue": "controller.handle"}}
					]
				},
				{
					"traceId": "trace-shared", "spanId": "span-2",
					"name": "db.query",
					"attributes": [
						{"key": "code.filepath", "value": {"stringValue": "src/db.go"}},
						{"key": "code.function", "value": {"stringValue": "db.query"}}
					]
				},
				{
					"traceId": "trace-solo", "spanId": "span-3",
					"name": "solo.run", "attributes": []
				}
			]}]
		}]
	}`

	count, err := collector.IngestOTLP([]byte(payload))
	if err != nil {
		t.Fatalf("IngestOTLP: %v", err)
	}
	if count != 3 || len(captured) != 3 {
		t.Fatalf("expected 3 spans ingested and captured, got count=%d captured=%d", count, len(captured))
	}

	// The broadcast keeps the raw traceId — row keying must not leak into
	// the event stream.
	for _, ev := range captured[:2] {
		if ev.TraceID != "trace-shared" {
			t.Errorf("broadcast TraceID = %q, want the raw \"trace-shared\"", ev.TraceID)
		}
	}

	// Every span must persist: overview counts all three...
	overview, err := store.ProfilerOverview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.TotalTraces != 3 {
		t.Errorf("TotalTraces = %d, want 3 (spans 2..N of a shared traceId must not be dropped)", overview.TotalTraces)
	}

	// ...and the rows are keyed per span under the documented scheme.
	rows, err := store.RecentTraces(10)
	if err != nil {
		t.Fatalf("recent traces: %v", err)
	}
	rowIDs := map[string]bool{}
	for _, r := range rows {
		rowIDs[r.TraceID] = true
	}
	for _, want := range []string{"trace-shared-span-1", "trace-shared-span-2", "trace-solo-span-3"} {
		if !rowIDs[want] {
			t.Errorf("persisted row %q missing (have %v)", want, rowIDs)
		}
	}
}
