package core

import (
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// seedReadDerivedFixture builds the state the read-path cache bug needs: a code
// event in a DIFFERENT repo, plus an agent_runs row whose spend is only
// attributable to the target repo once a read event names it.
//
// The foreign code event matters: without it the repo-scoped queries take their
// "OR NOT EXISTS (a code event for another repo)" branch, every agent_runs row
// matches regardless of read events, and the whole fixture would be vacuous.
func seedReadDerivedFixture(t *testing.T, store *db.Store, filter string) {
	t.Helper()
	if err := store.InsertEvents([]db.EventRecord{{
		EventID:    "ev-foreign",
		RunID:      "run-foreign",
		RepoName:   "some-other-repo",
		FilePath:   "other/x.go",
		Signature:  "pkg.Foreign",
		NodeType:   "func",
		Action:     "ADDED",
		BodyHash:   "h",
		LOC:        3,
		StartLine:  1,
		EndLine:    3,
		OccurredAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("insert foreign code event: %v", err)
	}
	if err := store.UpsertRun(db.RunRecord{
		RunID:     "run-x",
		AgentName: "Agent",
		ModelName: "m-x",
		CostUSD:   5.00,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert run: %v", err)
	}
}

func hasModelRow(rows []db.ModelRow, name string) bool {
	for _, r := range rows {
		if r.Model == name {
			return true
		}
	}
	return false
}

// TestMetricsCache_RecordReadEventInvalidatesReadDerived pins the contract from
// internal/core/metrics.go: "every in-process write the snapshot is derived from
// bumps the generation" — for the read path specifically, which cannot use
// cacheGen because RecordReadEvent arrives dozens of times per agent turn.
//
// Overview and ModelComparison both build their repo-scoped run set from
// code_node_events UNION file_read_events, so a read event changes both. Before
// the fix Metrics() served the pre-write cached snapshot for the whole
// metricsCacheTTL.
func TestMetricsCache_RecordReadEventInvalidatesReadDerived(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	const filter = "read-cache-test"
	seedReadDerivedFixture(t, store, filter)

	before, err := e.Metrics(filter)
	if err != nil {
		t.Fatalf("metrics before: %v", err)
	}
	if hasModelRow(before.Models, "m-x") {
		t.Fatalf("precondition: m-x already visible before the read event; " +
			"the read event cannot be the deciding input, fixture is vacuous")
	}
	if before.Overview.TotalRuns != 0 {
		t.Fatalf("precondition: TotalRuns = %d, want 0", before.Overview.TotalRuns)
	}

	if err := e.RecordReadEvent(db.FileReadRecord{
		ReadID:         "read-1",
		RunID:          "run-x",
		RepoName:       filter,
		FilePath:       "x.go",
		AgentName:      "Agent",
		ModelName:      "m-x",
		ToolName:       "view_file",
		StartLine:      1,
		EndLine:        10,
		LinesReadCount: 10,
		PromptTokens:   100,
		ReadTime:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordReadEvent: %v", err)
	}

	after, err := e.Metrics(filter)
	if err != nil {
		t.Fatalf("metrics after: %v", err)
	}
	if !hasModelRow(after.Models, "m-x") {
		t.Errorf("Models after RecordReadEvent does not contain m-x: %+v; the "+
			"read-derived slice is still serving the pre-write cached snapshot", after.Models)
	}
	if after.Overview.TotalRuns != 1 {
		t.Errorf("Overview.TotalRuns after RecordReadEvent = %d, want 1 "+
			"(Overview is read-derived too)", after.Overview.TotalRuns)
	}
}

// TestMetricsCache_RecordReadEventInvalidatesModelRows covers the secondary
// branch the fix also had to touch: /api/metrics/models serves ModelRows
// directly and must not return the stale model list from the shared cache.
func TestMetricsCache_RecordReadEventInvalidatesModelRows(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	const filter = "read-cache-modelrows"
	seedReadDerivedFixture(t, store, filter)

	if _, err := e.Metrics(filter); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	rows, err := e.ModelRows(filter)
	if err != nil {
		t.Fatalf("ModelRows before: %v", err)
	}
	if hasModelRow(rows, "m-x") {
		t.Fatalf("precondition: m-x already visible before the read event")
	}

	if err := e.RecordReadEvent(db.FileReadRecord{
		ReadID:    "read-1",
		RunID:     "run-x",
		RepoName:  filter,
		FilePath:  "x.go",
		AgentName: "Agent",
		ModelName: "m-x",
		ReadTime:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordReadEvent: %v", err)
	}

	rows, err = e.ModelRows(filter)
	if err != nil {
		t.Fatalf("ModelRows after: %v", err)
	}
	if !hasModelRow(rows, "m-x") {
		t.Errorf("ModelRows after RecordReadEvent does not contain m-x: %+v", rows)
	}
}

// TestMetricsCache_ReadRefreshReusesCodeDerivedSlice is the negative-space
// guard: the whole point of the split is that a read event must NOT re-run the
// code-event queries. RecentEvents comes from code_node_events alone, so its
// backing array must be the identical slice across a read event. If a future
// change bumps cacheGen (or refreshes everything) on the read path, this fails.
func TestMetricsCache_ReadRefreshReusesCodeDerivedSlice(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	const filter = "read-cache-reuse"
	seedReadDerivedFixture(t, store, filter)

	// A code event inside the target repo so RecentEvents is non-empty and its
	// slice identity is observable.
	if err := store.InsertEvents([]db.EventRecord{{
		EventID:    "ev-mine",
		RunID:      "run-foreign",
		RepoName:   filter,
		FilePath:   "mine/y.go",
		Signature:  "pkg.Mine",
		NodeType:   "func",
		Action:     "ADDED",
		BodyHash:   "h",
		LOC:        2,
		StartLine:  1,
		EndLine:    2,
		OccurredAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("insert code event: %v", err)
	}

	first, err := e.Metrics(filter)
	if err != nil {
		t.Fatalf("metrics first: %v", err)
	}
	if len(first.RecentEvents) == 0 {
		t.Fatalf("precondition: RecentEvents is empty, slice identity is unobservable")
	}
	firstPtr := &first.RecentEvents[0]

	if err := e.RecordReadEvent(db.FileReadRecord{
		ReadID:    "read-1",
		RunID:     "run-x",
		RepoName:  filter,
		FilePath:  "x.go",
		AgentName: "Agent",
		ModelName: "m-x",
		ReadTime:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordReadEvent: %v", err)
	}

	second, err := e.Metrics(filter)
	if err != nil {
		t.Fatalf("metrics second: %v", err)
	}
	if len(second.RecentEvents) == 0 {
		t.Fatalf("RecentEvents became empty after the read event")
	}
	if &second.RecentEvents[0] != firstPtr {
		t.Errorf("RecentEvents was re-queried across a read event: the code-derived " +
			"part of the snapshot must be reused so the metrics cache is not defeated " +
			"during busy agent sessions")
	}
	if !hasModelRow(second.Models, "m-x") {
		t.Errorf("read-derived Models was not refreshed: %+v", second.Models)
	}
}

// TestMetricsCache_WithinReadDerivedTTLIsACacheHit is the second half of the
// trade-off: inside readDerivedTTL the entry must be served without re-running
// even the read-derived queries, otherwise the refresh would be as expensive as
// a full rebuild.
func TestMetricsCache_WithinReadDerivedTTLIsACacheHit(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	const filter = "read-cache-window"
	seedReadDerivedFixture(t, store, filter)

	if err := e.RecordReadEvent(db.FileReadRecord{
		ReadID:    "read-1",
		RunID:     "run-x",
		RepoName:  filter,
		FilePath:  "x.go",
		AgentName: "Agent",
		ModelName: "m-x",
		ReadTime:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordReadEvent: %v", err)
	}

	first, err := e.Metrics(filter)
	if err != nil {
		t.Fatalf("metrics first: %v", err)
	}
	if !hasModelRow(first.Models, "m-x") {
		t.Fatalf("precondition: m-x missing from the first snapshot")
	}
	modelsPtr := &first.Models[0]

	second, err := e.Metrics(filter)
	if err != nil {
		t.Fatalf("metrics second: %v", err)
	}
	if len(second.Models) == 0 {
		t.Fatalf("Models became empty")
	}
	if &second.Models[0] != modelsPtr {
		t.Errorf("Models was re-queried inside readDerivedTTL (%s); the refresh "+
			"must not fire on every call", readDerivedTTL)
	}
}
