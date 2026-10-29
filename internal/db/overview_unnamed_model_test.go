package db

import (
	"testing"
	"time"
)

// Round 5 (proof-driven bug hunt): db.Overview counted an EMPTY model_name as a
// distinct model, so one unattributed run inflated the dashboard's "Unique
// Models" KPI by one.
//
// Every sibling surface already treats an unnamed run as "not a model":
// GetFileReadStats skips it before counting (pinned by
// TestGetFileReadStats_EmptyModelIsNotUnique), ModelComparison's spend CTE
// filters `model_name <> ''`, and models.IsJunkModel keeps the "unknown" bucket
// out of the leaderboard. Overview was the one aggregate that disagreed.
//
// Reachability: UpsertRun binds RunRecord.ModelName verbatim and POST
// /api/telemetry accepts a body with no model_name, so an unattributed run is
// an ordinary row rather than a corrupt one.
//
// Pre-fix probe FAIL (through db.Open -> Migrate -> UpsertRun -> Overview):
//
//	case A (1 named + 1 unattributed run): UniqueModels=2 TotalRuns=2 TotalCost=2.00
//	case C (sibling convention, same two model names): GetFileReadStats.UniqueModels=1
//	FAIL: case A: UniqueModels = 2, want 1 — an empty model_name is counted as a distinct model
//
// Both halves of the fix are pinned below: the unfiltered branch and the
// repo-filtered CTE branch each have their own failing case.

// TestOverview_UnnamedModelIsNotCounted covers the unfiltered branch. The run
// and cost totals must still include the unattributed run: it is a run, and its
// spend is real — only the model COUNT changes.
func TestOverview_UnnamedModelIsNotCounted(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	seedRun(t, s, RunRecord{RunID: "run-named", ModelName: "gpt-4o", Provider: "OpenAI", CostUSD: 1.5, CreatedAt: now})
	seedRun(t, s, RunRecord{RunID: "run-unattributed", ModelName: "", Provider: "", CostUSD: 0.5, CreatedAt: now})

	ov, err := s.Overview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if ov.UniqueModels != 1 {
		t.Errorf("UniqueModels = %d, want 1: an empty model_name is not a model", ov.UniqueModels)
	}
	if ov.TotalRuns != 2 {
		t.Errorf("TotalRuns = %d, want 2: an unattributed run is still a run", ov.TotalRuns)
	}
	if ov.TotalCost < 1.99 || ov.TotalCost > 2.01 {
		t.Errorf("TotalCost = %v, want 2.00: unattributed spend is real spend", ov.TotalCost)
	}
}

// TestOverview_UnnamedModelIsNotCountedWithRepoFilter covers the repo-filtered
// CTE branch, where the empty-name guard has to survive the OR in
// `no_other OR run_id IN (matched)` — without explicit parentheses the guard
// would apply to only one side of the disjunction.
//
// The "other" repo's event makes no_other false, so the run set is genuinely
// filtered rather than falling back to "every run".
func TestOverview_UnnamedModelIsNotCountedWithRepoFilter(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	seedRun(t, s, RunRecord{RunID: "run-named", ModelName: "gpt-4o", Provider: "OpenAI", CostUSD: 1.5, CreatedAt: now})
	seedRun(t, s, RunRecord{RunID: "run-unattributed", ModelName: "", Provider: "", CostUSD: 0.5, CreatedAt: now})
	seedRun(t, s, RunRecord{RunID: "run-other-repo", ModelName: "gpt-4o-mini", Provider: "OpenAI", CostUSD: 9.0, CreatedAt: now})

	seedRepoEvent(t, s, "ev-proj", "run-named", "proj", "f.go", "function:f.go::A", now)
	seedRepoEvent(t, s, "ev-proj-unattributed", "run-unattributed", "proj", "g.go", "function:g.go::B", now)
	seedRepoEvent(t, s, "ev-other", "run-other-repo", "other", "h.go", "function:h.go::C", now)

	ov, err := s.Overview("proj")
	if err != nil {
		t.Fatalf("overview(proj): %v", err)
	}
	if ov.UniqueModels != 1 {
		t.Errorf("UniqueModels = %d, want 1: an empty model_name is not a model (filtered branch)", ov.UniqueModels)
	}
	if ov.TotalRuns != 2 {
		t.Errorf("TotalRuns = %d, want 2: proj's two runs only, not the other repo's", ov.TotalRuns)
	}

	// Control: the same filter with named models only still counts them, so the
	// guard did not disable the aggregate.
	other := openTestStore(t)
	seedRun(t, other, RunRecord{RunID: "r-a", ModelName: "gpt-4o", CreatedAt: now})
	seedRun(t, other, RunRecord{RunID: "r-b", ModelName: "claude-sonnet", CreatedAt: now})
	seedRepoEvent(t, other, "e-a", "r-a", "proj", "f.go", "function:f.go::A", now)
	ovControl, err := other.Overview("proj")
	if err != nil {
		t.Fatalf("overview control: %v", err)
	}
	if ovControl.UniqueModels != 2 {
		t.Errorf("control: UniqueModels = %d, want 2 (two distinct named models)", ovControl.UniqueModels)
	}
}

// seedEvent appends one code_node_events row so a run can be attributed to a
// repo. InsertEvent is the production writer.
func seedRepoEvent(t *testing.T, s *Store, id, runID, repo, path, sig string, at time.Time) {
	t.Helper()
	if err := s.InsertEvent(EventRecord{
		EventID:    id,
		RunID:      runID,
		RepoName:   repo,
		FilePath:   path,
		Signature:  sig,
		NodeType:   "function",
		Action:     "MODIFIED",
		BodyHash:   "hash",
		LOC:        3,
		StartLine:  1,
		EndLine:    3,
		OccurredAt: at,
	}); err != nil {
		t.Fatalf("seed event %s: %v", id, err)
	}
}
