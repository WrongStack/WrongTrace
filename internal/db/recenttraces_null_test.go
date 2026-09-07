package db

import (
	"path/filepath"
	"testing"
)

// TestRecentTraces_ToleratesNullColumns pins the round-44 contract: duration_ms,
// cpu_usage_pct, memory_bytes, status_code and timestamp are schema-nullable in
// runtime_traces, so a schema-legal row carrying an explicit NULL must not
// abort the whole scan. Before the fix, one NULL duration_ms made RecentTraces
// return "scan recent trace: converting NULL to double is unsupported" — and
// cmd/wrongtrace export swallowed that error ("traces, _ :="), silently
// shipping the official export artifact with no traces at all, exit 0.
func TestRecentTraces_ToleratesNullColumns(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "nulltrace.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	insertRaw := func(id string, duration any) {
		t.Helper()
		if _, err := st.DB().Exec(
			`INSERT INTO runtime_traces (trace_id, service_name, profiler_type, duration_ms, timestamp)
			 VALUES (?, ?, ?, ?, ?)`,
			id, "proofsvc", "custom", duration, "2026-09-06 10:00:00",
		); err != nil {
			t.Fatalf("raw insert %s: %v", id, err)
		}
	}
	insertRaw("tr-null-dur", nil) // explicit NULL in a nullable column: schema-legal
	insertRaw("tr-control", 12.5)

	// Secondary branches: NULL status_code materializes at the schema DEFAULT
	// (200), and a NULL timestamp reads back as the zero time ("unknown") —
	// both matching the COALESCE convention used for the string columns.
	if _, err := st.DB().Exec(
		`INSERT INTO runtime_traces (trace_id, service_name, profiler_type, duration_ms, status_code, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"tr-null-status-ts", "proofsvc", "custom", 5.0, nil, nil,
	); err != nil {
		t.Fatalf("raw insert tr-null-status-ts: %v", err)
	}

	traces, err := st.RecentTraces(50)
	if err != nil {
		t.Fatalf("RecentTraces must survive schema-legal NULL columns: %v", err)
	}

	byID := make(map[string]RuntimeTraceRecord, len(traces))
	for _, tr := range traces {
		byID[tr.TraceID] = tr
	}

	nullRow, ok := byID["tr-null-dur"]
	if !ok {
		t.Fatalf("NULL-duration row missing from RecentTraces results")
	}
	if nullRow.DurationMs != 0 {
		t.Errorf("NULL-duration row read back with DurationMs=%v, want 0", nullRow.DurationMs)
	}

	ctrl, ok := byID["tr-control"]
	if !ok {
		t.Fatalf("control row missing from RecentTraces results")
	}
	if ctrl.DurationMs != 12.5 {
		t.Errorf("control row read back with DurationMs=%v, want 12.5", ctrl.DurationMs)
	}

	statusRow, ok := byID["tr-null-status-ts"]
	if !ok {
		t.Fatalf("NULL-status row missing from RecentTraces results")
	}
	if statusRow.StatusCode != 200 {
		t.Errorf("NULL-status row read back with StatusCode=%d, want schema default 200", statusRow.StatusCode)
	}
	if !statusRow.Timestamp.IsZero() {
		t.Errorf("NULL-timestamp row read back as %v, want the zero time (unknown)", statusRow.Timestamp)
	}
}
