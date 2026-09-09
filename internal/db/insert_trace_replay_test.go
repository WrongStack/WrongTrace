package db

// Store-layer half of the round-24/25 invariant.
//
// Round 24 changed IngestOTLP to count only the spans whose InsertTrace returned
// nil, and to surface an error when nothing persisted. That accounting is only
// correct while the store treats a *replay* as success. It did not:
// runtime_traces.trace_id is the PRIMARY KEY and InsertTrace was a plain INSERT,
// so re-sending a batch -- which OTLP and LangChain SDKs do whenever they doubt a
// response -- raised SQLITE_CONSTRAINT. The collector then counted already-stored
// spans as "did not persist" and POST /v1/traces answered an error for data that
// was already in the database. The repo's own guard caught it:
// TestOTLPIngestRejectsOversizedBodyNamingTheLimit/controls failed with
//   {"error":"parse otlp traces: otlp: all 1 spans failed to store"}
// which is how the regression was found rather than shipped.
//
// The fix is the clause its sibling InsertReadEvent already carries
// (ON CONFLICT(read_id) DO NOTHING). This file pins the store contract so the
// clause cannot be "tidied" back into a plain INSERT, and so nobody has to
// re-derive it from a failing HTTP test two layers away.
//
// Losing spans to a BAD key is still detected one layer up, by
// TestIngestOTLP_SharedTraceIDKeepsAllSpans, which counts rows actually stored
// instead of trusting a conflict error. Do not weaken that test to compensate
// for this clause.

import (
	"path/filepath"
	"testing"
	"time"
)

func TestInsertTraceReplayIsNoOpSuccess(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rec := RuntimeTraceRecord{
		TraceID:       "trace-replay-1",
		ServiceName:   "payments",
		NodeSignature: "func:checkout.go::Charge",
		DurationMs:    12.5,
		ProfilerType:  "otlp",
		Timestamp:     time.Now().UTC(),
	}

	countFor := func(id string) int {
		t.Helper()
		rows, err := store.RecentTraces(50)
		if err != nil {
			t.Fatalf("RecentTraces: %v", err)
		}
		n := 0
		for _, r := range rows {
			if r.TraceID == id {
				n++
			}
		}
		return n
	}

	if err := store.InsertTrace(rec); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// The replay must not error: the row is already stored, which is exactly the
	// state the caller wanted. This is the assertion that round 24's accounting
	// depends on.
	if err := store.InsertTrace(rec); err != nil {
		t.Fatalf("replay of an already-stored trace must be a no-op success, got: %v", err)
	}
	if got := countFor(rec.TraceID); got != 1 {
		t.Fatalf("row count for replayed trace = %d, want 1 (DO NOTHING must not duplicate)", got)
	}

	// Boundary: the conflict clause is keyed on trace_id only, so a distinct id
	// must still land normally rather than being silently swallowed.
	other := rec
	other.TraceID = "trace-replay-2"
	if err := store.InsertTrace(other); err != nil {
		t.Fatalf("distinct insert: %v", err)
	}
	if got := countFor(other.TraceID); got != 1 {
		t.Fatalf("row count for distinct trace = %d, want 1", got)
	}
	if got := countFor(""); got != 0 {
		t.Fatalf("count for an absent id = %d, want 0 (helper must not over-match)", got)
	}
}
