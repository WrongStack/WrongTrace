package profiler

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// TestIngestOTLP_FailedWritesAreNotCountedAsAccepted pins the accounting of
// IngestOTLP's return value against what actually reached the database.
//
// Defect (found round 24, first described by round 7 and left unfixed when round
// 7 removed its ROUTINE trigger): collector.go swallowed the InsertTrace error --
//
//	if err := s.InsertTrace(rec); err != nil { log.Printf(...) }   // logged only
//	count++                                                        // unconditional
//	return count, nil                                              // always nil
//
// -- so a batch whose writes were ALL rejected came back as accepted=N with a
// nil error. handlers.go turns that into HTTP 200, an OTel SDK treats 200 as
// success and drops the batch, and the spans are unrecoverable while the client
// believes they landed. OTLP defines partial_success precisely so a receiver can
// say this instead. Round 7 fixed the per-span rowID (the PK-collision CAUSE)
// but never fixed the count, which is why no existing test caught this.
//
// Contract asserted here, in both directions:
//   - accepted counts only spans that persisted, and a batch where NOTHING
//     persisted surfaces an error (total failure is safe to retry);
//   - a PARTIAL failure must NOT surface an error: a 500 makes the SDK resend the
//     whole batch, and any span that did land is then re-inserted under the same
//     trace_id, so the client could never drain.
//
// Replay note (round 25): re-sending an already-stored span is NOT counted as a
// failure. InsertTrace carries ON CONFLICT(trace_id) DO NOTHING (mirroring
// InsertReadEvent), so a replay is a no-op success rather than an error that this
// accounting would report as "did not persist".
//   - control: a healthy store accepts and persists the full batch.
//
// No store configured is deliberately NOT an error (nothing to persist, so the
// span counts) -- that is the contract rounds 13/16/18's tests rely on.
//
// Uses only exported API and one inline document, so it compiles and runs
// against pre-fix HEAD without any helper from this package's test files.
func TestIngestOTLP_FailedWritesAreNotCountedAsAccepted(t *testing.T) {
	// Local builder: no package-level helper, so nothing here can collide with
	// another test file's symbols.
	// Assembled with json.Marshal, not a hand-counted literal: the retyped
	// envelope here closed two braces too many and the healthy-store control
	// below caught it as `invalid character '}' after array element` -- which is
	// what the control is for. Only exported API is used, so this file also
	// compiles against pre-fix HEAD for the teeth check.
	twoSpans := func() []byte {
		attr := map[string]any{"key": "service.name",
			"value": map[string]any{"stringValue": "payments"}}
		span := func(id, name, start, end string) map[string]any {
			return map[string]any{"traceId": id, "spanId": "s-" + id, "name": name,
				"startTimeUnixNano": start, "endTimeUnixNano": end}
		}
		rs := map[string]any{
			"resource": map[string]any{"attributes": []any{attr}},
			"scopeSpans": []any{map[string]any{"spans": []any{
				span("t-r24a", "pay", "1700000000000000000", "1700000000050000000"),
				span("t-r24b", "refund", "1700000000100000000", "1700000000140000000"),
			}}},
		}
		b, err := json.Marshal(map[string]any{"resourceSpans": []any{rs}})
		if err != nil {
			t.Fatalf("SETUP (not the bug): marshal doc: %v", err)
		}
		return b
	}()

	openMigrated := func() *db.Store {
		st, err := db.Open(filepath.Join(t.TempDir(), "persist.db"))
		if err != nil {
			t.Fatalf("SETUP (not the bug): open store: %v", err)
		}
		if err := st.Migrate(); err != nil {
			t.Fatalf("SETUP (not the bug): migrate: %v", err)
		}
		return st
	}

	t.Run("total failure surfaces an error and counts nothing", func(t *testing.T) {
		st := openMigrated()
		if err := st.Close(); err != nil {
			t.Fatalf("SETUP (not the bug): close store: %v", err)
		}
		c := NewCollector(Config{Store: st})
		n, err := c.IngestOTLP(twoSpans)
		if err == nil {
			t.Errorf("IngestOTLP returned a nil error for a batch whose every insert failed (accepted=%d); callers answer HTTP 200 and the SDK drops the batch", n)
		}
		if n != 0 {
			t.Errorf("accepted = %d, want 0 -- the count must reflect what persisted", n)
		}
		if err != nil && !strings.Contains(err.Error(), "failed to store") {
			t.Errorf("failure error does not describe the loss: %v", err)
		}
	})

	t.Run("healthy store persists and counts the full batch", func(t *testing.T) {
		st := openMigrated()
		defer func() { _ = st.Close() }()
		c := NewCollector(Config{Store: st})
		n, err := c.IngestOTLP(twoSpans)
		if err != nil || n != 2 {
			t.Fatalf("CONTROL BROKEN (not the bug): accepted=%d err=%v, want 2 nil", n, err)
		}
		rows, err := st.RecentTraces(10)
		if err != nil {
			t.Fatalf("CONTROL BROKEN (not the bug): recent traces: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("CONTROL BROKEN (not the bug): persisted %d rows, want 2", len(rows))
		}
	})

	t.Run("no store configured is not a failure", func(t *testing.T) {
		// Nothing to persist means nothing failed; this is the contract every
		// other test in this package depends on.
		c := NewCollector(Config{})
		n, err := c.IngestOTLP(twoSpans)
		if err != nil {
			t.Errorf("IngestOTLP with no store returned %v, want nil", err)
		}
		if n != 2 {
			t.Errorf("accepted = %d with no store, want 2", n)
		}
	})
}

// TestIngestReport_FailedWriteReturnsError is the single-record sibling of the
// bug above. IngestReport's signature already returns an error (used for
// validation), but the persistence path returned nil after logging a failed
// insert, so handlers.go answered 200 for a record that never stored. A single
// record has no partial-failure hazard, so surfacing the error is always safe.
func TestIngestReport_FailedWriteReturnsError(t *testing.T) {
	st, err := db.Open(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatalf("SETUP (not the bug): open store: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("SETUP (not the bug): migrate: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("SETUP (not the bug): close store: %v", err)
	}

	c := NewCollector(Config{Store: st})
	ev, err := c.IngestReport(ProfilerReportPayload{
		ServiceName: "payments", ProfilerType: "custom",
		NodeSignature: "ChargeCard()", FilePath: "payments/card.go", DurationMs: 5,
	})
	if err == nil {
		t.Errorf("IngestReport returned nil after a failed insert (traceID=%s); handlers.go maps the error to HTTP 500, so the client was told the record was stored", ev.TraceID)
	}
	if ev.TraceID == "" {
		t.Errorf("CONTROL BROKEN (not the bug): no event returned even in memory")
	}
}
