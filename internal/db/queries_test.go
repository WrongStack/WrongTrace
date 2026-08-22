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
		EventID:    id,
		RunID:      runID,
		RepoName:   "test",
		FilePath:   "f.go",
		Signature:  sig,
		NodeType:   "function",
		Action:     action,
		BodyHash:   "h" + id,
		LOC:        3,
		OccurredAt: at,
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
