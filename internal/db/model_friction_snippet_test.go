package db

import (
	"testing"
	"time"
)

// ModelFrictionMatrix keeps diff_snippet out of its window sort and joins it
// back for the LIMITed rows only. Each returned collision must still carry
// its own event's snippet, in newest-first order.
func TestModelFrictionMatrix_SnippetJoinedAfterLimit(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	seedRun(t, s, RunRecord{RunID: "run-a", ModelName: "model-a", CreatedAt: now})
	seedRun(t, s, RunRecord{RunID: "run-b", ModelName: "model-b", CreatedAt: now})

	insert := func(id, runID, action string, at time.Time) {
		t.Helper()
		if err := s.InsertEvent(EventRecord{
			EventID: id, RunID: runID, RepoName: "repo", FilePath: "a.go",
			Signature: "func:a.go::F", NodeType: "function", Action: action,
			DiffSnippet: "snippet-" + id, AttributionConfidence: 0.95, OccurredAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	insert("e0", "run-a", "ADDED", now)
	insert("e1", "run-b", "MODIFIED", now.Add(1*time.Second))
	insert("e2", "run-a", "MODIFIED", now.Add(2*time.Second))
	insert("e3", "run-b", "MODIFIED", now.Add(3*time.Second))

	report, err := s.ModelFrictionMatrix(2)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalCollisions != 3 {
		t.Errorf("TotalCollisions = %d, want 3 (aggregates are unlimited)", report.TotalCollisions)
	}
	if len(report.RecentCollisions) != 2 {
		t.Fatalf("RecentCollisions = %d, want 2", len(report.RecentCollisions))
	}
	for i, want := range []string{"e3", "e2"} {
		got := report.RecentCollisions[i]
		if got.EventID != want || got.DiffSnippet != "snippet-"+want {
			t.Errorf("collision[%d] = %s/%q, want %s/%q", i, got.EventID, got.DiffSnippet, want, "snippet-"+want)
		}
	}
}
