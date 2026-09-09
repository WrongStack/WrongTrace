package db

import (
	"fmt"
	"testing"
	"time"
)

// TestModelFrictionMatrix_TopPairMatchesEdges pins the contract that
// cd51960's "deterministic TopFrictionPair" change was supposed to deliver.
//
// That commit added an edge sort whose comment says it exists because "Go's
// random map iteration makes TopFrictionPair arbitrary on tied cross-agent
// edges", but the value it names was assigned INSIDE the scan loop as a strict
// running max over rows ordered by event_time DESC, while edges is aggregated
// after the loop and sorted after that. A sort cannot reorder a number that is
// already finalised, so the fix was inert and TopFrictionPair kept the row-order
// winner.
//
// The fixture below makes the two rules disagree on purpose, with no map
// iteration involved: two cross-agent pairs from one author tie on conflict
// count, and the pair that reaches the max LAST in time order ("Zebra") is the
// one the old running max reported, while the documented order
// (count desc, then overwriter asc) puts "Adam" first. Both must now agree.
//
// Edges[0] is the same report's other answer to "which pair is worst", so a
// mismatch is not a style question: the two fields contradict each other while
// both are rendered by the dashboard and returned by the MCP tool.
func TestModelFrictionMatrix_TopPairMatchesEdges(t *testing.T) {
	s := openTestStore(t)
	author := "SeedAuthor"
	// Strictly increasing, unambiguous timestamps: each tie-break signal points
	// the same way, so the only variable is which rule the code consults.
	seedRun(t, s, RunRecord{RunID: "run-author", ModelName: author, CreatedAt: time.Unix(100, 0).UTC()})
	seedRun(t, s, RunRecord{RunID: "run-adam", ModelName: "AdamModel", CreatedAt: time.Unix(200, 0).UTC()})
	seedRun(t, s, RunRecord{RunID: "run-zebra", ModelName: "ZebraModel", CreatedAt: time.Unix(300, 0).UTC()})

	seedTiePair(t, s, "func:Adam", "run-adam", 3, 1000)
	// Zebra's collisions are the LATEST rows, so the old running max reported it.
	seedTiePair(t, s, "func:Zebra", "run-zebra", 3, 5000)

	report, err := s.ModelFrictionMatrix(200)
	if err != nil {
		t.Fatalf("ModelFrictionMatrix: %v", err)
	}

	var firstCross *ModelFrictionEdge
	for i := range report.Edges {
		if report.Edges[i].AuthorModel != report.Edges[i].OverwriterModel {
			firstCross = &report.Edges[i]
			break
		}
	}
	if firstCross == nil {
		t.Fatalf("fixture produced no cross-agent edge: %+v", report.Edges)
	}
	// Guard the fixture itself: it must be a real tie, or this proves nothing.
	tied := 0
	for _, e := range report.Edges {
		if e.AuthorModel != e.OverwriterModel && e.ConflictCount == firstCross.ConflictCount {
			tied++
		}
	}
	if tied < 2 {
		t.Fatalf("fixture is not a tie (%d cross edges at count %d): %+v", tied, firstCross.ConflictCount, report.Edges)
	}
	if firstCross.OverwriterModel != "AdamModel" {
		t.Fatalf("Edges ordering regressed: first cross-agent overwriter = %q, want AdamModel", firstCross.OverwriterModel)
	}

	want := fmt.Sprintf("%s ➔ AdamModel (%d collisions)", author, firstCross.ConflictCount)
	if report.TopFrictionPair != want {
		t.Errorf("TopFrictionPair = %q, want %q (must agree with Edges[0]; the row-order running max answers %s ➔ ZebraModel)",
			report.TopFrictionPair, want, author)
	}

	// Stability is not correctness: re-run to show the agreed answer is also
	// reproducible, which is what cd51960's own test checked and why it passed
	// while the value was still chosen by row order.
	for i := 0; i < 5; i++ {
		again, err := s.ModelFrictionMatrix(200)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if again.TopFrictionPair != want {
			t.Fatalf("call %d: TopFrictionPair = %q, want %q", i, again.TopFrictionPair, want)
		}
	}
}

// TestModelFrictionMatrix_SentinelPairMustNotWinHeadline pins the second half of
// the same contract: "unknown" is not a model.
//
// The friction query builds both models with COALESCE(r.model_name,'unknown')
// over a LEFT JOIN, so any event whose agent_runs row is missing or pruned
// yields the literal sentinel -- a non-NULL string that survives the
// "author_model IS NOT NULL" filter and lands in the edge map. IsCrossAgent
// already refuses to call such a pair friction, but the TopFrictionPair summary
// re-implemented only the self-thrash half of that rule, so an unattributed
// chain could outrank every genuine pair and take the dashboard's "Highest
// friction pair" slot while the same report counted those collisions as
// non-friction.
func TestModelFrictionMatrix_SentinelPairMustNotWinHeadline(t *testing.T) {
	s := openTestStore(t)
	author := "SeedAuthor"
	seedRun(t, s, RunRecord{RunID: "run-author", ModelName: author, CreatedAt: time.Unix(100, 0).UTC()})
	seedRun(t, s, RunRecord{RunID: "run-adam", ModelName: "AdamModel", CreatedAt: time.Unix(200, 0).UTC()})
	// run-ghost is deliberately NEVER registered: that is how "unknown" is born.

	seedTiePair(t, s, "func:Real", "run-adam", 2, 1000)   // genuine friction, fewer
	seedTiePair(t, s, "func:Ghost", "run-ghost", 4, 5000) // unattributed, more

	report, err := s.ModelFrictionMatrix(200)
	if err != nil {
		t.Fatalf("ModelFrictionMatrix: %v", err)
	}

	// Premise, independent of the headline.
	var ghost, real *ModelFrictionEdge
	for i := range report.Edges {
		switch report.Edges[i].OverwriterModel {
		case "unknown":
			ghost = &report.Edges[i]
		case "AdamModel":
			real = &report.Edges[i]
		}
	}
	if ghost == nil || ghost.ConflictCount != 4 {
		t.Fatalf("premise broken: unattributed edge = %+v, want count 4", ghost)
	}
	if real == nil || real.ConflictCount != 2 {
		t.Fatalf("premise broken: AdamModel edge = %+v, want count 2", real)
	}

	// The headline must name the pair the report itself treats as friction.
	want := author + " ➔ AdamModel (2 collisions)"
	if report.TopFrictionPair != want {
		t.Errorf("TopFrictionPair = %q, want %q; naming the sentinel misattributes friction to a model that does not exist and displaces the real pair",
			report.TopFrictionPair, want)
	}

	// Surgical scope: only the summary changed. The unattributed collisions are
	// still recorded in the matrix and still counted by TotalCollisions, and the
	// ratio still sits strictly between the two bounds -- > 0 because the Adam
	// pair is friction, < 100 because the sentinel pair is not.
	if report.TotalCollisions != 6 {
		t.Errorf("TotalCollisions = %d, want 6 (2 attributed + 4 unattributed): the matrix must keep recording them",
			report.TotalCollisions)
	}
	if report.CrossAgentRatio <= 0 || report.CrossAgentRatio >= 100 {
		t.Errorf("CrossAgentRatio = %.1f, want strictly between 0 and 100", report.CrossAgentRatio)
	}
}

// TestModelFrictionMatrix_SentinelOnlyStoreHasNoHeadline covers the degenerate
// branch: when the only chains are unattributed there is no friction pair to
// name, so the summary stays empty. The dashboard already renders an empty
// value as "No collisions yet" for exactly this state; inventing a pair would be
// the bug.
func TestModelFrictionMatrix_SentinelOnlyStoreHasNoHeadline(t *testing.T) {
	s := openTestStore(t)
	seedRun(t, s, RunRecord{RunID: "run-author", ModelName: "SeedAuthor", CreatedAt: time.Unix(100, 0).UTC()})
	seedTiePair(t, s, "func:Ghost", "run-ghost", 3, 1000)

	report, err := s.ModelFrictionMatrix(200)
	if err != nil {
		t.Fatalf("ModelFrictionMatrix: %v", err)
	}
	if len(report.Edges) == 0 || report.TotalCollisions == 0 {
		t.Fatalf("premise broken: edges=%d collisions=%d", len(report.Edges), report.TotalCollisions)
	}
	if report.CrossAgentRatio != 0 {
		t.Fatalf("premise broken: CrossAgentRatio = %.1f, want 0 for an unattributed-only store", report.CrossAgentRatio)
	}
	if report.TopFrictionPair != "" {
		t.Errorf("TopFrictionPair = %q for a store with no attributable pair; want empty", report.TopFrictionPair)
	}
}

// seedTiePair builds one cross-agent chain: n collisions on signature sig, each
// an ADDED by the author run followed by a MODIFIED by the overwriter run, so the
// window author of every MODIFIED row is the author model. Times are spaced
// 10s apart from `base` seconds so distinct pairs never share an event_time.
func seedTiePair(t *testing.T, s *Store, sig, overwriterRun string, n int, base int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		addedAt := time.Unix(base+int64(10*i), 0).UTC()
		modAt := addedAt.Add(5 * time.Second)
		seedEvent(t, s, fmt.Sprintf("ev-%s-add-%d", sig, i), "run-author", sig, "ADDED", addedAt)
		seedEvent(t, s, fmt.Sprintf("ev-%s-mod-%d", sig, i), overwriterRun, sig, "MODIFIED", modAt)
	}
}
