package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
)

// newTestEngine returns an Engine backed by a real SQLite store in a temp
// dir. AST is nil: none of the correlation/telemetry paths touch it.
func newTestEngine(t *testing.T) (*Engine, *db.Store) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewEngine(Config{RepoName: "test-repo", Store: store}), store
}

func reportFor(runID string) ipc.TelemetryReport {
	return ipc.TelemetryReport{
		RunID:     runID,
		TaskID:    "TASK-402",
		AgentName: "Claude-Code",
		ModelName: "claude-3-7-sonnet",
		Provider:  "anthropic",
		Intent:    "Refactor auth",
	}
}

func activeByID(runs []ActiveRun) map[string]ActiveRun {
	out := make(map[string]ActiveRun, len(runs))
	for _, r := range runs {
		out[r.RunID] = r
	}
	return out
}

// ---------------------------------------------------------------
// run registration + validation
// ---------------------------------------------------------------

func TestReportRun_RegistersAndPersists(t *testing.T) {
	e, store := newTestEngine(t)

	if err := e.ReportRun(ipc.TelemetryReport{}); err == nil {
		t.Error("empty run_id must be rejected")
	}

	rep := reportFor("run-1")
	rep.CostUSD = 0.5
	if err := e.ReportRun(rep); err != nil {
		t.Fatalf("ReportRun: %v", err)
	}

	runs := e.ActiveRuns()
	if len(runs) != 1 {
		t.Fatalf("want 1 active run, got %d: %+v", len(runs), runs)
	}
	got := activeByID(runs)["run-1"]
	if got.AgentName != "Claude-Code" || got.ModelName != "claude-3-7-sonnet" || got.TaskID != "TASK-402" {
		t.Errorf("active-run metadata wrong: %+v", got)
	}
	if got.StartedAt.IsZero() || got.LastSeen.IsZero() {
		t.Errorf("timestamps not stamped: %+v", got)
	}

	ov, err := store.Overview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if ov.TotalRuns != 1 {
		t.Errorf("agent_runs rows = %d, want 1", ov.TotalRuns)
	}
	if ov.TotalCost != 0.5 {
		t.Errorf("total cost = %v, want 0.5", ov.TotalCost)
	}
}

func TestRecentRunID_LastSeenWins(t *testing.T) {
	e, _ := newTestEngine(t)

	if got := e.recentRunID(); got != "" {
		t.Errorf("no runs reported, recentRunID = %q, want empty", got)
	}

	if err := e.ReportRun(reportFor("r-first")); err != nil {
		t.Fatalf("report r-first: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // ensure distinct LastSeen
	if err := e.ReportRun(reportFor("r-second")); err != nil {
		t.Fatalf("report r-second: %v", err)
	}

	if got := e.recentRunID(); got != "r-second" {
		t.Errorf("recentRunID = %q, want r-second (last-seen wins)", got)
	}
	// Both stay active within the window.
	if n := len(e.ActiveRuns()); n != 2 {
		t.Errorf("active runs = %d, want 2", n)
	}
}

// TestCorrelation_WindowPruning shrinks the correlation window so expiry can
// be exercised in real time: expired runs are pruned lazily on read and no
// longer offered for event attribution.
func TestCorrelation_WindowPruning(t *testing.T) {
	e, _ := newTestEngine(t)
	e.correlate = 50 * time.Millisecond

	if err := e.ReportRun(reportFor("r-old")); err != nil {
		t.Fatalf("report r-old: %v", err)
	}
	time.Sleep(70 * time.Millisecond)
	if err := e.ReportRun(reportFor("r-new")); err != nil {
		t.Fatalf("report r-new: %v", err)
	}

	ids := activeByID(e.ActiveRuns())
	if len(ids) != 1 {
		t.Fatalf("expired run not pruned: %+v", ids)
	}
	if _, ok := ids["r-new"]; !ok {
		t.Errorf("surviving run should be r-new, got %+v", ids)
	}
	if got := e.recentRunID(); got != "r-new" {
		t.Errorf("recentRunID = %q, want r-new", got)
	}

	time.Sleep(70 * time.Millisecond)

	if runs := e.ActiveRuns(); len(runs) != 0 {
		t.Errorf("all runs expired, ActiveRuns = %+v", runs)
	}
	if got := e.recentRunID(); got != "" {
		t.Errorf("all runs expired, recentRunID = %q, want empty", got)
	}
}

// TestPersistAndBroadcast_BackfillsRunID drives the correlation behavior on
// real DB rows: unattributed events keep an empty run_id, events inside the
// window are back-filled with the active run, and after expiry attribution
// stops.
func TestPersistAndBroadcast_BackfillsRunID(t *testing.T) {
	e, store := newTestEngine(t)

	event := func(sig string) ast.Event {
		return ast.Event{
			RepoName:   "test-repo",
			FilePath:   "f.go",
			Signature:  sig,
			NodeType:   ast.NodeFunction,
			Action:     ast.ActionAdded,
			BodyHash:   "h-" + sig,
			LOC:        2,
			OccurredAt: time.Now().UTC(),
		}
	}

	// No active run: run_id stays empty.
	e.persistAndBroadcast(ast.DiffResult{Events: []ast.Event{event("s1")}})

	// Active run: back-filled.
	if err := e.ReportRun(reportFor("run-1")); err != nil {
		t.Fatalf("report: %v", err)
	}
	e.persistAndBroadcast(ast.DiffResult{Events: []ast.Event{event("s2")}})

	// Window expired: attribution stops.
	e.correlate = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	e.persistAndBroadcast(ast.DiffResult{Events: []ast.Event{event("s3")}})

	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	bySig := map[string]string{}
	for _, ev := range events {
		bySig[ev.Signature] = ev.RunID
	}
	if len(bySig) != 3 {
		t.Fatalf("want 3 persisted events, got %+v", bySig)
	}
	if bySig["s1"] != "" {
		t.Errorf("s1 attributed to %q, want empty (no active run)", bySig["s1"])
	}
	if bySig["s2"] != "run-1" {
		t.Errorf("s2 attributed to %q, want run-1", bySig["s2"])
	}
	if bySig["s3"] != "" {
		t.Errorf("s3 attributed to %q, want empty (window expired)", bySig["s3"])
	}
}

func TestReportRunMCP_Adapter(t *testing.T) {
	e, store := newTestEngine(t)

	runID, err := e.ReportRunMCP("gpt-5", "openai", "T-9", "do the thing", 10, 5, 0.25)
	if err != nil {
		t.Fatalf("ReportRunMCP: %v", err)
	}
	if runID == "" {
		t.Fatal("ReportRunMCP returned empty run_id")
	}

	runs := e.ActiveRuns()
	if len(runs) != 1 {
		t.Fatalf("want 1 active run, got %d", len(runs))
	}
	got := activeByID(runs)[runID]
	if got.AgentName != "MCP" || got.ModelName != "gpt-5" || got.TaskID != "T-9" {
		t.Errorf("adapter metadata wrong: %+v", got)
	}

	ov, _ := store.Overview()
	if ov.TotalRuns != 1 || ov.TotalCost != 0.25 {
		t.Errorf("persisted run wrong: runs=%d cost=%v", ov.TotalRuns, ov.TotalCost)
	}
}

func TestPing(t *testing.T) {
	e, _ := newTestEngine(t)
	if err := e.Ping(); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

// ---------------------------------------------------------------
// FileHealth delegation
// ---------------------------------------------------------------

// TestFileHealth_DelegatesToStore proves the engine is a pure delegator: the
// IPCHealth it returns is exactly db.FileHealth re-mapped field for field,
// with the fragility thresholds intact (6 recent edits on one signature →
// score 52, fragile, warning populated).
func TestFileHealth_DelegatesToStore(t *testing.T) {
	e, store := newTestEngine(t)

	cold, err := e.FileHealth("src/cold.go")
	if err != nil {
		t.Fatalf("FileHealth(cold): %v", err)
	}
	if cold.HealthScore != 100 || cold.IsFragile || cold.RecentThrashingCount != 0 || cold.Warning != "" {
		t.Errorf("healthy file scored wrong: %+v", cold)
	}
	if cold.FilePath != "src/cold.go" {
		t.Errorf("file path not echoed: %+v", cold)
	}

	for i := 0; i < 6; i++ {
		if err := store.InsertEvent(db.EventRecord{
			EventID:    fmt.Sprintf("hot-%d", i),
			RepoName:   "test-repo",
			FilePath:   "src/hot.go",
			Signature:  "function:hot.go::Alpha",
			NodeType:   "function",
			Action:     "MODIFIED",
			BodyHash:   fmt.Sprintf("h%d", i),
			LOC:        1,
			OccurredAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
	}

	hot, err := e.FileHealth("src/hot.go")
	if err != nil {
		t.Fatalf("FileHealth(hot): %v", err)
	}
	want, err := store.FileHealth("src/hot.go")
	if err != nil {
		t.Fatalf("store.FileHealth(hot): %v", err)
	}

	wantMapped := IPCHealth{
		FilePath:             want.FilePath,
		HealthScore:          want.HealthScore,
		IsFragile:            want.IsFragile,
		RecentThrashingCount: want.RecentThrashingCount,
		Warning:              want.Warning,
	}
	if hot != wantMapped {
		t.Errorf("engine result diverged from store: got %+v, want %+v", hot, want)
	}
	if !hot.IsFragile || hot.HealthScore != 52 {
		t.Errorf("6 edits / 1 signature should be fragile at score 52: %+v", hot)
	}
	if !strings.Contains(hot.Warning, "6 edits") {
		t.Errorf("warning should mention edit count: %q", hot.Warning)
	}
}
