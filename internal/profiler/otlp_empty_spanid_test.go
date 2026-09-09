package profiler

import (
	"path/filepath"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// TestIngestOTLP_EmptySpanIDKeepsAllSpans pins the fourth shape of the
// per-span row-key contract documented on TestIngestOTLP_SharedTraceIDKeepsAllSpans.
// A span carrying a traceId but an EMPTY spanId has no per-span key half, and
// keying such rows by the bare traceId collapsed every span of the trace onto
// one runtime_traces.trace_id; ON CONFLICT(trace_id) DO NOTHING then silently
// kept only the first row while IngestOTLP still counted and reported every
// span as accepted — the exact "counted but not stored" undercount the
// shared-traceId fix eliminated, now fully silent because a conflict no-op
// does not even log. protojson renders an explicitly-empty bytes field and an
// absent one as the same value, so the shape reaches here from real senders.
// The fix mints the missing half (traceId + randomID), mirroring how an
// identity-less trace gets a minted traceID.
func TestIngestOTLP_EmptySpanIDKeepsAllSpans(t *testing.T) {
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
				{"traceId": "trace-empty-span", "spanId": "", "name": "controller.handle", "attributes": []},
				{"traceId": "trace-empty-span", "spanId": "", "name": "db.query", "attributes": []},
				{"traceId": "trace-empty-span", "spanId": "", "name": "cache.get", "attributes": []}
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
	for _, ev := range captured {
		if ev.TraceID != "trace-empty-span" {
			t.Errorf("broadcast TraceID = %q, want the raw \"trace-empty-span\"", ev.TraceID)
		}
	}

	overview, err := store.ProfilerOverview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.TotalTraces != 3 {
		t.Errorf("TotalTraces = %d, want 3 (spans with an empty spanId must each persist)", overview.TotalTraces)
	}

	rows, err := store.RecentTraces(10)
	if err != nil {
		t.Fatalf("recent traces: %v", err)
	}
	rowIDs := map[string]bool{}
	for _, r := range rows {
		if rowIDs[r.TraceID] {
			t.Errorf("duplicate row key %q — every span must land in its own row", r.TraceID)
		}
		rowIDs[r.TraceID] = true
	}
	if len(rowIDs) != 3 {
		t.Errorf("persisted %d distinct row keys, want 3 (have %v)", len(rowIDs), rowIDs)
	}
	for id := range rowIDs {
		if id == "trace-empty-span" {
			t.Errorf("row keyed by the bare group id %q — the empty-spanId fallback must mint a per-span suffix", id)
		}
	}
}

// TestIngestOTLP_EmptySpanIDKeyingNeighbors pins the two neighboring keying
// shapes the fix must not disturb: a fully identified span keeps its exact
// traceId-spanId key (the shape InsertTrace's replay no-op depends on), and a
// span with NEITHER id still persists under a unique minted key.
func TestIngestOTLP_EmptySpanIDKeyingNeighbors(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	collector := NewCollector(Config{Store: store})

	payload := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "checkout"}}]},
			"scopeSpans": [{"spans": [
				{"traceId": "t9", "spanId": "s9", "name": "identified.span", "attributes": []},
				{"traceId": "", "spanId": "", "name": "identityless.span", "attributes": []}
			]}]
		}]
	}`

	count, err := collector.IngestOTLP([]byte(payload))
	if err != nil {
		t.Fatalf("IngestOTLP: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}

	rows, err := store.RecentTraces(10)
	if err != nil {
		t.Fatalf("recent traces: %v", err)
	}
	rowIDs := map[string]bool{}
	for _, r := range rows {
		rowIDs[r.TraceID] = true
	}
	if !rowIDs["t9-s9"] {
		t.Errorf("identified row key %q missing — the traceId-spanId scheme must stay untouched (have %v)", "t9-s9", rowIDs)
	}
	if len(rowIDs) != 2 {
		t.Errorf("persisted %d distinct row keys, want 2 (the identityless span must still get a unique minted key; have %v)", len(rowIDs), rowIDs)
	}
}
