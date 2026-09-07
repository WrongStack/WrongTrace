package db

import (
	"path/filepath"
	"testing"
)

// TestRecentEvents_ToleratesNullLinesOfCode pins the round-38 contract: the
// schema declares lines_of_code INTEGER (nullable, no DEFAULT), so a
// schema-legal row with a NULL value must not abort the whole scan. It reads
// back with LOC=0 while healthy rows in the same result keep their values.
// Before the fix, a single NULL row made RecentEvents return
// "scan recent event: converting NULL to int is unsupported" — breaking the
// dashboard live feed and every IPC path routed through this query.
func TestRecentEvents_ToleratesNullLinesOfCode(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "nullloc.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	insertRaw := func(id string, loc any) {
		t.Helper()
		if _, err := st.DB().Exec(
			`INSERT INTO code_node_events (event_id, repo_name, file_path, node_signature, node_type, action, lines_of_code, event_time)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "proof-repo", "proof/file.go", "proof.Func", "function", "MODIFIED", loc, "2026-09-06 10:00:00",
		); err != nil {
			t.Fatalf("raw insert %s: %v", id, err)
		}
	}
	insertRaw("ev-null-loc", nil) // column omitted by the writer: schema-legal NULL
	insertRaw("ev-control", 42)

	events, err := st.RecentEvents(50)
	if err != nil {
		t.Fatalf("RecentEvents must survive a NULL lines_of_code row: %v", err)
	}

	byID := make(map[string]EventRecord, len(events))
	for _, e := range events {
		byID[e.EventID] = e
	}

	nullRow, ok := byID["ev-null-loc"]
	if !ok {
		t.Fatalf("NULL-LOC row missing from RecentEvents results")
	}
	if nullRow.LOC != 0 {
		t.Errorf("NULL-LOC row read back with LOC=%d, want 0", nullRow.LOC)
	}

	ctrl, ok := byID["ev-control"]
	if !ok {
		t.Fatalf("control row missing from RecentEvents results")
	}
	if ctrl.LOC != 42 {
		t.Errorf("control row read back with LOC=%d, want 42", ctrl.LOC)
	}
}
