package db

import (
	"path/filepath"
	"testing"
	"time"
)

// openTestStore opens a migrated store in a per-test temp directory.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func seedRun(t *testing.T, s *Store, r RunRecord) {
	t.Helper()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if err := s.UpsertRun(r); err != nil {
		t.Fatalf("seed run %s: %v", r.RunID, err)
	}
}

func seedEvent(t *testing.T, s *Store, id, runID, sig, action string, at time.Time) {
	t.Helper()
	err := s.InsertEvent(EventRecord{
		EventID:               id,
		RunID:                 runID,
		RepoName:              "test",
		FilePath:              "f.go",
		Signature:             sig,
		NodeType:              "function",
		Action:                action,
		BodyHash:              "h" + id,
		LOC:                   3,
		AttributionSource:     "tool_path",
		AttributionConfidence: 0.95,
		OccurredAt:            at,
	})
	if err != nil {
		t.Fatalf("seed event %s: %v", id, err)
	}
}

func rowFor(t *testing.T, rows []ModelRow, model string) ModelRow {
	t.Helper()
	for _, r := range rows {
		if r.Model == model {
			return r
		}
	}
	t.Fatalf("model %q missing from ModelComparison result: %+v", model, rows)
	return ModelRow{}
}

func approx(got, want float64) bool {
	return got > want-1e-9 && got < want+1e-9
}

// TestModelComparison_SurvivalAndROI seeds backdated rows (15+ days) and
// asserts the survival-rate and cost-per-surviving-node math end to end:
//
//	model-a: Alpha survives (added 20d ago, edited since, never deleted),
//	         Beta died recently (added 20d ago, deleted 5d ago).
//	         -> 2 nodes, 1 active, 50% survival, 1 survivor, $1.00 spend
//	         -> cost_per_surviving_node = $1.00
//	model-b: Gamma died long ago (added 20d, deleted 18d).
//	         -> 1 node, 0 active, 0% survival, 0 survivors, $3.00 spend
//	         -> cost_per_surviving_node stays 0 (undefined, guarded)
func TestModelComparison_SurvivalAndROI(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()
	ago := func(days int) time.Time { return now.AddDate(0, 0, -days) }

	seedRun(t, s, RunRecord{RunID: "a1", TaskID: "T1", AgentName: "t", ModelName: "model-a", Provider: "p", CostUSD: 1.00, CreatedAt: ago(20)})
	seedRun(t, s, RunRecord{RunID: "b1", TaskID: "T2", AgentName: "t", ModelName: "model-b", Provider: "p", CostUSD: 3.00, CreatedAt: ago(20)})

	// model-a lifecycle
	seedEvent(t, s, "e1", "a1", "func:Alpha", "ADDED", ago(20))
	seedEvent(t, s, "e2", "a1", "func:Alpha", "MODIFIED", ago(10)) // edits don't change survival
	seedEvent(t, s, "e3", "a1", "func:Beta", "ADDED", ago(20))
	seedEvent(t, s, "e4", "a1", "func:Beta", "DELETED", ago(5)) // recent death kills an old survivor
	// model-b lifecycle
	seedEvent(t, s, "e5", "b1", "func:Gamma", "ADDED", ago(20))
	seedEvent(t, s, "e6", "b1", "func:Gamma", "DELETED", ago(18))

	rows, err := s.ModelComparison()
	if err != nil {
		t.Fatalf("ModelComparison: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 model rows, got %d: %+v", len(rows), rows)
	}

	a := rowFor(t, rows, "model-a")
	if a.TotalNodes != 2 || a.ActiveNodes != 1 {
		t.Errorf("model-a nodes: want total=2 active=1, got total=%d active=%d", a.TotalNodes, a.ActiveNodes)
	}
	if !approx(a.SurvivalRatePct, 50.0) {
		t.Errorf("model-a survival_rate_pct: want 50.0, got %v", a.SurvivalRatePct)
	}
	if a.TotalSurvivedNodes != 1 {
		t.Errorf("model-a survived nodes: want 1 (Alpha only; Beta deleted), got %d", a.TotalSurvivedNodes)
	}
	if !approx(a.TotalCostUSD, 1.00) {
		t.Errorf("model-a total_cost_usd: want 1.00, got %v", a.TotalCostUSD)
	}
	if !approx(a.CostPerSurvNode, 1.00) {
		t.Errorf("model-a cost_per_surviving_node: want 1.00, got %v", a.CostPerSurvNode)
	}
	if a.AvgLongevityDays <= 0 {
		t.Errorf("model-a avg_longevity_days: want > 0, got %v", a.AvgLongevityDays)
	}
	if a.RunCount != 1 {
		t.Errorf("model-a run_count: want 1, got %d", a.RunCount)
	}

	b := rowFor(t, rows, "model-b")
	if b.TotalNodes != 1 || b.ActiveNodes != 0 {
		t.Errorf("model-b nodes: want total=1 active=0, got total=%d active=%d", b.TotalNodes, b.ActiveNodes)
	}
	if !approx(b.SurvivalRatePct, 0) {
		t.Errorf("model-b survival_rate_pct: want 0, got %v", b.SurvivalRatePct)
	}
	if b.TotalSurvivedNodes != 0 || !approx(b.CostPerSurvNode, 0) {
		t.Errorf("model-b roi: want 0 survivors and 0 cost_per_surviving_node, got %d / %v", b.TotalSurvivedNodes, b.CostPerSurvNode)
	}
	if !approx(b.TotalCostUSD, 3.00) {
		t.Errorf("model-b total_cost_usd: want 3.00, got %v", b.TotalCostUSD)
	}
}

// TestModelComparison_SpendWithoutEvents locks in the fix that models with
// reported runs but zero correlated AST events still appear (previously the
// query drove FROM the events-only lifecycle CTE and such models vanished,
// hiding their spend until churn occurred).
func TestModelComparison_SpendWithoutEvents(t *testing.T) {
	s := openTestStore(t)
	seedRun(t, s, RunRecord{RunID: "c1", TaskID: "T3", AgentName: "t", ModelName: "model-c", Provider: "p", CostUSD: 0.144})

	rows, err := s.ModelComparison()
	if err != nil {
		t.Fatalf("ModelComparison: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 model row, got %d: %+v", len(rows), rows)
	}
	c := rowFor(t, rows, "model-c")
	if c.RunCount != 1 || c.TotalNodes != 0 {
		t.Errorf("model-c: want run_count=1 total_nodes=0, got run_count=%d total_nodes=%d", c.RunCount, c.TotalNodes)
	}
	if !approx(c.TotalCostUSD, 0.144) {
		t.Errorf("model-c total_cost_usd: want 0.144 visible pre-churn, got %v", c.TotalCostUSD)
	}
	if !approx(c.SurvivalRatePct, 0) || !approx(c.CostPerSurvNode, 0) {
		t.Errorf("model-c: want 0 survival / 0 roi, got %v / %v", c.SurvivalRatePct, c.CostPerSurvNode)
	}
}

// TestModelComparison_EmptyStore asserts a fresh DB yields no rows without error.
func TestModelComparison_EmptyStore(t *testing.T) {
	s := openTestStore(t)
	rows, err := s.ModelComparison()
	if err != nil {
		t.Fatalf("ModelComparison on empty store: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want 0 rows on empty store, got %+v", rows)
	}
}

func TestAllNodeStats_And_Thrashing(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	seedRun(t, s, RunRecord{
		RunID:     "run-1",
		TaskID:    "task-1",
		AgentName: "TestAgent",
		ModelName: "gpt-4o",
		Provider:  "openai",
		CostUSD:   0.05,
		CreatedAt: now,
	})

	seedEvent(t, s, "ev-1", "run-1", "func:foo", "ADDED", now.Add(-5*time.Minute))
	seedEvent(t, s, "ev-2", "run-1", "func:foo", "MODIFIED", now.Add(-3*time.Minute))
	seedEvent(t, s, "ev-3", "run-1", "func:foo", "MODIFIED", now.Add(-1*time.Minute))

	// Test AllNodeStats
	stats, err := s.AllNodeStats()
	if err != nil {
		t.Fatalf("AllNodeStats failed: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 node stat, got %d", len(stats))
	}
	stat := stats["func:foo"]
	if stat.EditCount != 3 {
		t.Errorf("expected 3 edits, got %d", stat.EditCount)
	}
	if stat.LastModel != "gpt-4o" {
		t.Errorf("expected last model gpt-4o, got %s", stat.LastModel)
	}

	// Test Thrashing
	thrashing, err := s.Thrashing(2, 7)
	if err != nil {
		t.Fatalf("Thrashing query failed: %v", err)
	}
	if len(thrashing) != 1 {
		t.Fatalf("expected 1 thrashing row, got %d", len(thrashing))
	}
	if thrashing[0].Signature != "func:foo" {
		t.Errorf("thrashing signature = %s, want func:foo", thrashing[0].Signature)
	}

	// Test Overview
	ov, err := s.Overview()
	if err != nil {
		t.Fatalf("Overview failed: %v", err)
	}
	if ov.TotalRuns != 1 || ov.TotalEvents != 3 {
		t.Errorf("Overview mismatch: runs=%d events=%d", ov.TotalRuns, ov.TotalEvents)
	}
}

func TestHelpers(t *testing.T) {
	if toInt(int64(42)) != 42 || toInt(int(42)) != 42 || toInt(float64(42.0)) != 42 || toInt("invalid") != 0 {
		t.Error("toInt helper failed")
	}
	if toFloat(float64(3.14)) != 3.14 || toFloat(int64(3)) != 3.0 || toFloat(int(3)) != 3.0 || toFloat("invalid") != 0 {
		t.Error("toFloat helper failed")
	}
}

func TestFileReadEventsAndStats(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	// Insert reads
	err := s.InsertReadEvent(FileReadRecord{
		ReadID:         "read-1",
		RunID:          "run-1",
		RepoName:       "test-repo",
		FilePath:       "internal/core/engine.go",
		AgentName:      "Antigravity",
		ModelName:      "gemini-3.7-flash",
		Provider:       "google",
		ToolName:       "view_file",
		StartLine:      1,
		EndLine:        100,
		LinesReadCount: 100,
		PromptTokens:   1200,
		CostUSD:        0.002,
		Intent:         "Inspect engine implementation",
		ReadTime:       now.Add(-10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("InsertReadEvent 1 failed: %v", err)
	}

	err = s.InsertReadEvent(FileReadRecord{
		ReadID:         "read-2",
		RunID:          "run-2",
		RepoName:       "test-repo",
		FilePath:       "internal/core/engine.go",
		AgentName:      "Cursor",
		ModelName:      "claude-3-7-sonnet",
		Provider:       "anthropic",
		ToolName:       "read_file",
		StartLine:      50,
		EndLine:        150,
		LinesReadCount: 101,
		PromptTokens:   2500,
		CostUSD:        0.008,
		Intent:         "Check correlation window",
		ReadTime:       now.Add(-2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("InsertReadEvent 2 failed: %v", err)
	}

	// 1. Test GetRecentFileReads
	recent, err := s.GetRecentFileReads(10)
	if err != nil {
		t.Fatalf("GetRecentFileReads failed: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent reads, got %d", len(recent))
	}
	if recent[0].ReadID != "read-2" {
		t.Errorf("expected most recent read-2 first, got %s", recent[0].ReadID)
	}

	// 2. Test GetFileReadStats
	stats, err := s.GetFileReadStats("internal/core/engine.go")
	if err != nil {
		t.Fatalf("GetFileReadStats failed: %v", err)
	}
	if stats.TotalReads != 2 {
		t.Errorf("expected 2 total reads, got %d", stats.TotalReads)
	}
	if stats.TotalLinesRead != 201 {
		t.Errorf("expected 201 total lines read, got %d", stats.TotalLinesRead)
	}
	if stats.UniqueModels != 2 {
		t.Errorf("expected 2 unique models, got %d", stats.UniqueModels)
	}
	if stats.ModelBreakdown["gemini-3.7-flash"] != 1 || stats.ModelBreakdown["claude-3-7-sonnet"] != 1 {
		t.Errorf("unexpected model breakdown: %+v", stats.ModelBreakdown)
	}
	if stats.ProviderBreakdown["google"] != 1 || stats.ProviderBreakdown["anthropic"] != 1 {
		t.Errorf("unexpected provider breakdown: %+v", stats.ProviderBreakdown)
	}

	// 3. Test GetFileReadHeatmap
	heatmap, err := s.GetFileReadHeatmap("internal/core/engine.go")
	if err != nil {
		t.Fatalf("GetFileReadHeatmap failed: %v", err)
	}
	if len(heatmap) != 2 {
		t.Fatalf("expected 2 heatmap slices, got %d", len(heatmap))
	}
}

func TestStore_ComprehensiveCoverage(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	// 1. Overview
	seedRun(t, s, RunRecord{RunID: "run-overview-1", TaskID: "t-1", AgentName: "agy", ModelName: "gpt-4o", Provider: "openai", CostUSD: 0.05, CreatedAt: now})
	seedEvent(t, s, "ev-1", "run-overview-1", "func:DoTask", "ADDED", now)

	overview, err := s.Overview()
	if err != nil {
		t.Fatalf("Overview failed: %v", err)
	}
	if overview.TotalRuns < 1 || overview.TotalEvents < 1 {
		t.Errorf("expected positive overview counts: %+v", overview)
	}

	overviewFiltered, err := s.Overview("test")
	if err != nil {
		t.Fatalf("Overview filtered failed: %v", err)
	}
	if overviewFiltered.TotalEvents < 1 {
		t.Errorf("expected filtered events >= 1: %+v", overviewFiltered)
	}

	// 2. SymbolHistory & FileModelActivity
	seedEvent(t, s, "ev-2", "run-overview-1", "func:DoTask", "MODIFIED", now.Add(time.Minute))
	history, err := s.SymbolHistory("f.go", "func:DoTask", 10)
	if err != nil {
		t.Fatalf("SymbolHistory failed: %v", err)
	}
	if len(history) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(history))
	}

	activity, err := s.FileModelActivity("f.go")
	if err != nil {
		t.Fatalf("FileModelActivity failed: %v", err)
	}
	if len(activity) == 0 {
		t.Errorf("expected activity entries for f.go")
	}

	// 3. ModelFrictionMatrix
	seedRun(t, s, RunRecord{RunID: "run-overview-2", TaskID: "t-2", AgentName: "devin", ModelName: "claude-3-7-sonnet", Provider: "anthropic", CostUSD: 0.10, CreatedAt: now.Add(2 * time.Minute)})
	seedEvent(t, s, "ev-3", "run-overview-2", "func:DoTask", "MODIFIED", now.Add(2*time.Minute))

	friction, err := s.ModelFrictionMatrix(50)
	if err != nil {
		t.Fatalf("ModelFrictionMatrix failed: %v", err)
	}
	if len(friction.Edges) == 0 && len(friction.RecentCollisions) == 0 {
		t.Logf("friction matrix result: %+v", friction)
	}

	// 4. AllFilesHealth & AllNodeStats & RecentFileEvents
	fileHealths, err := s.AllFilesHealth("test")
	if err != nil {
		t.Fatalf("AllFilesHealth failed: %v", err)
	}
	if len(fileHealths) == 0 {
		t.Errorf("expected file health entries")
	}

	nodeStats, err := s.AllNodeStats("test")
	if err != nil {
		t.Fatalf("AllNodeStats failed: %v", err)
	}
	if len(nodeStats) == 0 {
		t.Errorf("expected node stats")
	}

	fileEvs, err := s.RecentFileEvents("f.go", 10)
	if err != nil {
		t.Fatalf("RecentFileEvents failed: %v", err)
	}
	if len(fileEvs) == 0 {
		t.Errorf("expected recent file events")
	}

	// 5. Profiler traces
	err = s.InsertTrace(RuntimeTraceRecord{
		TraceID:       "tr-1",
		RunID:         "run-overview-1",
		ServiceName:   "backend",
		NodeSignature: "func:DoTask",
		FilePath:      "f.go",
		DurationMs:    150,
		CPUUsagePct:   12.5,
		MemoryBytes:   1024 * 1024,
		StatusCode:    200,
		ProfilerType:  "custom",
		Timestamp:     now,
	})
	if err != nil {
		t.Fatalf("InsertTrace failed: %v", err)
	}

	profOverview, err := s.ProfilerOverview()
	if err != nil {
		t.Fatalf("ProfilerOverview failed: %v", err)
	}
	if profOverview.TotalTraces != 1 {
		t.Errorf("expected 1 profiler trace, got %d", profOverview.TotalTraces)
	}

	traces, err := s.RecentTraces(10)
	if err != nil {
		t.Fatalf("RecentTraces failed: %v", err)
	}
	if len(traces) != 1 {
		t.Errorf("expected 1 recent trace, got %d", len(traces))
	}

	// 6. Maintenance: ClearStale & Vacuum
	deleted, err := s.ClearStale(30)
	if err != nil {
		t.Fatalf("ClearStale failed: %v", err)
	}
	t.Logf("ClearStale(30) deleted %d records", deleted)

	if err := s.Vacuum(); err != nil {
		t.Fatalf("Vacuum failed: %v", err)
	}

	// 7. DB accessor & health
	if s.DB() == nil {
		t.Errorf("DB() should not be nil")
	}
}

func TestModelFrictionMatrix_IgnoresLowConfidenceAttribution(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()
	seedRun(t, s, RunRecord{RunID: "run-author", ModelName: "model-a", CreatedAt: now})
	seedRun(t, s, RunRecord{RunID: "run-overwriter", ModelName: "model-b", CreatedAt: now})
	seedEvent(t, s, "ev-author", "run-author", "func:Work", "ADDED", now)

	if err := s.InsertEvent(EventRecord{
		EventID:               "ev-ambiguous",
		RunID:                 "run-overwriter",
		RepoName:              "test",
		FilePath:              "f.go",
		Signature:             "func:Work",
		NodeType:              "function",
		Action:                "MODIFIED",
		BodyHash:              "ambiguous",
		AttributionSource:     "single_active_run",
		AttributionConfidence: 0.60,
		OccurredAt:            now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	report, err := s.ModelFrictionMatrix(20)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalCollisions != 0 {
		t.Fatalf("low-confidence attribution produced %d collision(s)", report.TotalCollisions)
	}
}
