package db

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Regression tests for the queries.go audit round: per-file suffix matching
// without a directory boundary, per-model lifecycle split across models,
// signature-only node keys, LIMIT-truncated friction aggregates, oldest-N
// symbol history, whole-lookback thrashing span, swallowed query errors, and
// non-deterministic model-activity output.
//
// Only the exported surface is used, so each assertion holds independently of
// how the queries are written.

func openAuditStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func auditEvent(t *testing.T, st *Store, id, run, path, sig, action string, at time.Time) {
	t.Helper()
	if err := st.InsertEvent(EventRecord{
		EventID: id, RunID: run, RepoName: "audit", FilePath: path, Signature: sig,
		NodeType: "function", Action: action, BodyHash: "h", LOC: 3,
		AttributionSource: "tool_path", AttributionConfidence: 1.0, OccurredAt: at,
	}); err != nil {
		t.Fatalf("InsertEvent %s: %v", id, err)
	}
}

func auditRead(t *testing.T, st *Store, id, path, model, provider string, at time.Time) {
	t.Helper()
	if err := st.InsertReadEvent(FileReadRecord{
		ReadID: id, RepoName: "audit", FilePath: path, AgentName: "agent",
		ModelName: model, Provider: provider, ToolName: "read_file",
		StartLine: 1, EndLine: 10, LinesReadCount: 10, CostUSD: 0.01, ReadTime: at,
	}); err != nil {
		t.Fatalf("InsertReadEvent %s: %v", id, err)
	}
}

func auditRun(t *testing.T, st *Store, runID, model string) {
	t.Helper()
	if err := st.UpsertRun(RunRecord{RunID: runID, TaskID: "t", AgentName: "agent-" + runID, ModelName: model, Provider: "prov"}); err != nil {
		t.Fatalf("UpsertRun %s: %v", runID, err)
	}
}

// TestAuditRound_PerFileSuffixMatchHasDirectoryBoundary: a suffix arm of the
// form '%' || path matched "cmd/domain.go" for a "main.go" query (and a stored
// "a.go" for an "internal/data.go" query), merging another file's history into
// every per-file reader. Suffix arms must be anchored on '/'.
func TestAuditRound_PerFileSuffixMatchHasDirectoryBoundary(t *testing.T) {
	st := openAuditStore(t)
	now := time.Now().UTC().Add(-time.Minute)
	auditRun(t, st, "run-a", "model-a")
	auditEvent(t, st, "e1", "run-a", "cmd/domain.go", "function:domain.go::F", "MODIFIED", now)
	auditEvent(t, st, "e2", "run-a", "a.go", "function:a.go::G", "MODIFIED", now)
	auditRead(t, st, "r1", "cmd/domain.go", "model-a", "prov", now)

	if evs, err := st.RecentFileEvents("main.go", 50); err != nil || len(evs) != 0 {
		t.Errorf("RecentFileEvents(main.go) = %d rows (err %v), want 0: %v", len(evs), err, eventFilePaths(evs))
	}
	if evs, err := st.RecentFileEvents("internal/data.go", 50); err != nil || len(evs) != 0 {
		t.Errorf("RecentFileEvents(internal/data.go) = %d rows (err %v), want 0 (stored a.go is not its suffix)", len(evs), err)
	}
	if h, err := st.SymbolHistory("main.go", "", 50); err != nil || len(h) != 0 {
		t.Errorf("SymbolHistory(main.go) = %d rows (err %v), want 0", len(h), err)
	}
	if a, err := st.FileModelActivity("main.go"); err != nil || len(a) != 0 {
		t.Errorf("FileModelActivity(main.go) = %d summaries (err %v), want 0", len(a), err)
	}
	if s, err := st.GetFileReadStats("main.go"); err != nil || s.TotalReads != 0 {
		t.Errorf("GetFileReadStats(main.go).TotalReads = %d (err %v), want 0", s.TotalReads, err)
	}
	if hm, err := st.GetFileReadHeatmap("main.go"); err != nil || len(hm) != 0 {
		t.Errorf("GetFileReadHeatmap(main.go) = %d rows (err %v), want 0", len(hm), err)
	}

	// Positive controls: real path-component suffixes and exact matches still work.
	st2 := openAuditStore(t)
	auditEvent(t, st2, "p1", "", "internal/cmd/main.go", "function:main.go::main", "MODIFIED", now)
	auditRead(t, st2, "pr1", `internal\cmd\main.go`, "model-a", "prov", now)
	for _, q := range []string{"cmd/main.go", "internal/cmd/main.go", "/abs/repo/internal/cmd/main.go", "INTERNAL/cmd/Main.go"} {
		if evs, err := st2.RecentFileEvents(q, 50); err != nil || len(evs) != 1 {
			t.Errorf("RecentFileEvents(%q) = %d rows (err %v), want 1", q, len(evs), err)
		}
		if h, err := st2.SymbolHistory(q, "", 50); err != nil || len(h) != 1 {
			t.Errorf("SymbolHistory(%q) = %d rows (err %v), want 1", q, len(h), err)
		}
	}
	for _, q := range []string{"cmd/main.go", "internal/cmd/main.go", `internal\cmd\main.go`, "Internal/CMD/main.go"} {
		if s, err := st2.GetFileReadStats(q); err != nil || s.TotalReads != 1 {
			t.Errorf("GetFileReadStats(%q).TotalReads = %d (err %v), want 1", q, s.TotalReads, err)
		}
	}
}

// TestAuditRound_ModelComparisonAttributesNodeToCreator: lifecycle grouped by
// (node_signature, model) made a node ADDED by model-a and DELETED by model-b
// a survivor for model-a and a dead phantom node for model-b.
func TestAuditRound_ModelComparisonAttributesNodeToCreator(t *testing.T) {
	st := openAuditStore(t)
	old := time.Now().UTC().AddDate(0, 0, -30)
	auditRun(t, st, "ra", "model-a")
	auditRun(t, st, "rb", "model-b")
	auditEvent(t, st, "e1", "ra", "x.go", "function:x.go::Foo", "ADDED", old)
	auditEvent(t, st, "e2", "rb", "x.go", "function:x.go::Foo", "DELETED", old.Add(time.Hour))

	rows, err := st.ModelComparison()
	if err != nil {
		t.Fatalf("ModelComparison: %v", err)
	}
	got := map[string]ModelRow{}
	for _, r := range rows {
		got[r.Model] = r
	}
	a, b := got["model-a"], got["model-b"]
	if a.TotalNodes != 1 || a.ActiveNodes != 0 || a.TotalSurvivedNodes != 0 {
		t.Errorf("model-a = total %d active %d survived %d, want 1/0/0 (its node was deleted)", a.TotalNodes, a.ActiveNodes, a.TotalSurvivedNodes)
	}
	if b.TotalNodes != 0 || b.ActiveNodes != 0 {
		t.Errorf("model-b = total %d active %d, want 0/0 (it created no node)", b.TotalNodes, b.ActiveNodes)
	}
}

// TestAuditRound_NodeKeysIncludeFilePath: signatures embed only the basename,
// so keying by signature alone merged same-named files in different
// directories (AllNodeStats, and ModelComparison's never-deleted check), and
// the event_time = MAX(event_time) re-join picked an arbitrary row on a
// same-second tie.
func TestAuditRound_NodeKeysIncludeFilePath(t *testing.T) {
	now := time.Now().UTC().Add(-time.Hour)
	const sig = "function:main.go::main"

	st := openAuditStore(t)
	auditEvent(t, st, "a1", "", "cmd/a/main.go", sig, "ADDED", now)
	auditEvent(t, st, "b1", "", "cmd/b/main.go", sig, "ADDED", now)
	auditEvent(t, st, "b2", "", "cmd/b/main.go", sig, "MODIFIED", now.Add(time.Second))

	stats, err := st.AllNodeStats()
	if err != nil {
		t.Fatalf("AllNodeStats: %v", err)
	}
	byFile := map[string]NodeStat{}
	for _, ns := range stats {
		byFile[ns.FilePath] = ns
	}
	if len(stats) != 2 {
		t.Errorf("AllNodeStats entries = %d, want 2 (one per file): %+v", len(stats), stats)
	}
	if byFile["cmd/a/main.go"].EditCount != 1 || byFile["cmd/a/main.go"].LastAction != "ADDED" {
		t.Errorf("cmd/a/main.go stat = %+v, want 1 edit, last ADDED", byFile["cmd/a/main.go"])
	}
	if byFile["cmd/b/main.go"].EditCount != 2 || byFile["cmd/b/main.go"].LastAction != "MODIFIED" {
		t.Errorf("cmd/b/main.go stat = %+v, want 2 edits, last MODIFIED", byFile["cmd/b/main.go"])
	}

	// Same-second tie: event_id DESC decides, independent of insertion order.
	tie := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for _, order := range [][]string{{"z", "a"}, {"a", "z"}} {
		s := openAuditStore(t)
		for _, id := range order {
			action := "ADDED"
			if id == "z" {
				action = "DELETED"
			}
			auditEvent(t, s, id, "", "t.go", "function:t.go::T", action, tie)
		}
		m, err := s.AllNodeStats()
		if err != nil {
			t.Fatalf("AllNodeStats tie: %v", err)
		}
		for _, ns := range m {
			if ns.LastAction != "DELETED" || ns.EditCount != 2 {
				t.Errorf("insertion %v: tie stat = %+v, want last DELETED (event_id z), 2 edits", order, ns)
			}
		}
	}

	// ROI: deleting "init" in b.go must not un-survive "init" in a.go.
	roi := openAuditStore(t)
	old := time.Now().UTC().AddDate(0, 0, -30)
	auditRun(t, roi, "ra", "model-a")
	auditEvent(t, roi, "i1", "ra", "a.go", "function:x.go::init", "ADDED", old)
	auditEvent(t, roi, "i2", "", "pkg/x.go", "function:x.go::init", "DELETED", old)
	rows, err := roi.ModelComparison()
	if err != nil {
		t.Fatalf("ModelComparison: %v", err)
	}
	for _, r := range rows {
		if r.Model == "model-a" && (r.TotalSurvivedNodes != 1 || r.ActiveNodes != 1) {
			t.Errorf("model-a survived=%d active=%d, want 1/1 (the deletion was in another file)", r.TotalSurvivedNodes, r.ActiveNodes)
		}
	}
}

// TestAuditRound_FrictionAggregatesIgnoreLimit: edges, TotalCollisions,
// CrossAgentRatio and TopFrictionPair were tallied from the LIMITed rows.
func TestAuditRound_FrictionAggregatesIgnoreLimit(t *testing.T) {
	st := openAuditStore(t)
	auditRun(t, st, "ra", "model-a")
	auditRun(t, st, "rb", "model-b")
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 30; i++ {
		run, action := "ra", "MODIFIED"
		if i%2 == 1 {
			run = "rb"
		}
		if i == 0 {
			action = "ADDED"
		}
		auditEvent(t, st, fmt.Sprintf("e%03d", i), run, "x.go", "function:x.go::Foo", action, base.Add(time.Duration(i)*time.Second))
	}

	rep, err := st.ModelFrictionMatrix(5)
	if err != nil {
		t.Fatalf("ModelFrictionMatrix: %v", err)
	}
	if len(rep.RecentCollisions) != 5 {
		t.Errorf("RecentCollisions = %d, want 5 (the limit)", len(rep.RecentCollisions))
	}
	if rep.TotalCollisions != 29 {
		t.Errorf("TotalCollisions = %d, want 29 (all collisions, not the limited page)", rep.TotalCollisions)
	}
	sum := 0
	for _, e := range rep.Edges {
		sum += e.ConflictCount
	}
	if sum != 29 {
		t.Errorf("sum of edge ConflictCount = %d, want 29: %+v", sum, rep.Edges)
	}
	if want := "model-a ➔ model-b (15 collisions)"; rep.TopFrictionPair != want {
		t.Errorf("TopFrictionPair = %q, want %q", rep.TopFrictionPair, want)
	}
}

// TestAuditRound_SymbolHistoryLimitKeepsNewest: the filtered variants sorted
// ASC before LIMIT and returned the OLDEST N revisions.
func TestAuditRound_SymbolHistoryLimitKeepsNewest(t *testing.T) {
	st := openAuditStore(t)
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		auditEvent(t, st, fmt.Sprintf("e%d", i), "", "x.go", "function:x.go::Foo", "MODIFIED", base.Add(time.Duration(i)*time.Minute))
	}
	for _, tc := range []struct{ name, path, sig string }{
		{"file+signature", "x.go", "function:x.go::Foo"},
		{"file-only", "x.go", ""},
		{"signature-only", "", "function:x.go::Foo"},
	} {
		h, err := st.SymbolHistory(tc.path, tc.sig, 2)
		if err != nil {
			t.Fatalf("%s: SymbolHistory: %v", tc.name, err)
		}
		ids := make([]string, 0, len(h))
		for _, r := range h {
			ids = append(ids, r.EventID)
		}
		if want := []string{"e3", "e4"}; !reflect.DeepEqual(ids, want) {
			t.Errorf("%s: limit 2 = %v, want %v (newest two, chronological)", tc.name, ids, want)
		}
	}
}

// TestAuditRound_ThrashingUsesRollingWindow: MAX-MIN over the whole lookback
// meant one edit days earlier hid a live burst.
func TestAuditRound_ThrashingUsesRollingWindow(t *testing.T) {
	st := openAuditStore(t)
	now := time.Now().UTC().Add(-time.Minute)
	for i := 0; i < 10; i++ {
		auditEvent(t, st, fmt.Sprintf("e%d", i), "", "x.go", "function:x.go::Foo", "MODIFIED", now.Add(-time.Duration(i)*time.Minute))
	}
	auditEvent(t, st, "stale", "", "x.go", "function:x.go::Foo", "MODIFIED", now.Add(-3*24*time.Hour))

	rows, err := st.Thrashing(3, 7)
	if err != nil {
		t.Fatalf("Thrashing: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (the burst must not be hidden by a stale edit): %+v", len(rows), rows)
	}
	r := rows[0]
	if r.EditCount != 10 {
		t.Errorf("EditCount = %d, want 10 (edits inside the 24h window only)", r.EditCount)
	}
	if r.WindowHours > 24 || r.WindowHours < 0.14 || r.WindowHours > 0.16 {
		t.Errorf("WindowHours = %v, want the burst span (9 min)", r.WindowHours)
	}
	if !r.FirstEvent.After(now.Add(-24 * time.Hour)) {
		t.Errorf("FirstEvent = %v, want inside the burst window, not the stale edit", r.FirstEvent)
	}
}

// TestAuditRound_QueryErrorsPropagate: FileModelActivity and
// AllFileModelActivity (and the other readers) swallowed query errors and
// returned an empty, successful-looking result.
func TestAuditRound_QueryErrorsPropagate(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	_ = st.Close()

	if _, err := st.FileModelActivity("x.go"); err == nil {
		t.Error("FileModelActivity on a closed store returned nil error")
	}
	if _, err := st.AllFileModelActivity(10); err == nil {
		t.Error("AllFileModelActivity on a closed store returned nil error")
	}
	if _, err := st.AllFilesHealth(); err == nil {
		t.Error("AllFilesHealth on a closed store returned nil error")
	}
	if _, err := st.GetFileReadStats("x.go"); err == nil {
		t.Error("GetFileReadStats on a closed store returned nil error")
	}
	if _, err := st.ModelFrictionMatrix(10); err == nil {
		t.Error("ModelFrictionMatrix on a closed store returned nil error")
	}
}

// TestAuditRound_FileModelActivityIsDeterministic: FileModelActivity returned
// random Go map order, and a bare provider column under GROUP BY model_name
// picked an arbitrary row's provider.
func TestAuditRound_FileModelActivityIsDeterministic(t *testing.T) {
	st := openAuditStore(t)
	at := time.Now().UTC().Add(-time.Hour)
	auditRead(t, st, "r1", "x.go", "model-m", "p2", at)
	auditRead(t, st, "r2", "x.go", "model-m", "p1", at)
	auditRead(t, st, "r3", "x.go", "model-m", "p1", at)
	for i, m := range []string{"model-c", "model-b", "model-a"} {
		auditRead(t, st, fmt.Sprintf("s%d", i), "x.go", m, "px", at)
	}

	want := []string{"model-m", "model-a", "model-b", "model-c"}
	for i := 0; i < 20; i++ {
		acts, err := st.FileModelActivity("x.go")
		if err != nil {
			t.Fatalf("FileModelActivity: %v", err)
		}
		got := make([]string, 0, len(acts))
		for _, a := range acts {
			got = append(got, a.ModelName)
			if a.ModelName == "model-m" && a.Provider != "p2" {
				t.Fatalf("model-m provider = %q, want deterministic MAX(provider) p2", a.Provider)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d order = %v, want %v (activity DESC, model name ASC)", i, got, want)
		}
	}
}
