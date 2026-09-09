package db

import (
	"path/filepath"
	"testing"
	"time"
)

// Regression tests for LIKE-metacharacter quoting in per-file queries.
//
// These queries build SQL LIKE patterns out of caller-supplied path/signature
// text. In a LIKE pattern "_" matches ANY single character and "%" any run, so
// without quoting the metacharacters a query for one file silently aggregates
// rows belonging to a DIFFERENT file -- e.g. asking for "file_read.go" also
// matched a sibling named "file-read.go", and "100%.png" matched every
// "100<anything>.png". Costs, read counts, model breakdowns and heatmaps were
// all wrong, with no error to indicate it.
//
// Both directions are covered: the caller value used as a pattern, and the
// stored path used as a pattern against the caller value.
//
// Only the exported surface is used, on purpose: the assertions must hold
// independently of the helper that implements the quoting.

func openMetaStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "likemeta.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedRead(t *testing.T, st *Store, id, path, model string, cost float64) {
	t.Helper()
	if err := st.InsertReadEvent(FileReadRecord{
		ReadID: id, RepoName: "meta", FilePath: path, AgentName: "agent",
		ModelName: model, Provider: "prov", ToolName: "read_file",
		StartLine: 1, EndLine: 10, LinesReadCount: 10, CostUSD: cost,
		ReadTime: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("InsertReadEvent %s: %v", id, err)
	}
}

func seedMetaEvent(t *testing.T, st *Store, id, path, sig string) {
	t.Helper()
	if err := st.InsertEvent(EventRecord{
		EventID: id, RepoName: "meta", FilePath: path, Signature: sig,
		NodeType: "function", Action: "MODIFIED", BodyHash: "hash", LOC: 5,
		OccurredAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("InsertEvent %s: %v", id, err)
	}
}

const (
	underscorePath = "internal/svc/file_read.go"
	// Differs from underscorePath only where "_" sits, so an unquoted "_" in a
	// LIKE pattern reaches it.
	dashPath  = "internal/svc/file-read.go"
	unrelated = "internal/svc/zzzzzzz.go"
)

// TestFileReadStats_MetacharactersAreLiteral covers the aggregates, the model
// breakdown and the heatmap of a single file.
func TestFileReadStats_MetacharactersAreLiteral(t *testing.T) {
	st := openMetaStore(t)
	seedRead(t, st, "r1", underscorePath, "model-mine", 0.25)
	seedRead(t, st, "r2", dashPath, "model-theirs", 7.50)
	seedRead(t, st, "r3", unrelated, "model-unrelated", 9.00)

	stats, err := st.GetFileReadStats(underscorePath)
	if err != nil {
		t.Fatalf("GetFileReadStats: %v", err)
	}
	if stats.TotalReads != 1 {
		t.Errorf("TotalReads = %d, want 1 (sibling file leaked in)", stats.TotalReads)
	}
	if got, want := stats.TotalCostUSD, 0.25; got != want {
		t.Errorf("TotalCostUSD = %v, want %v (sibling cost leaked in)", got, want)
	}
	if len(stats.ModelBreakdown) != 1 || stats.ModelBreakdown["model-mine"] != 1 {
		t.Errorf("ModelBreakdown = %v, want only model-mine", stats.ModelBreakdown)
	}
	if len(stats.ProviderBreakdown) == 0 {
		t.Error("ProviderBreakdown is empty; the file's own provider must be counted")
	}
	if len(stats.RecentReads) != 1 || stats.RecentReads[0].FilePath != underscorePath {
		t.Errorf("RecentReads = %d rows %v, want the queried file only",
			len(stats.RecentReads), stats.RecentReads)
	}

	heat, err := st.GetFileReadHeatmap(underscorePath)
	if err != nil {
		t.Fatalf("GetFileReadHeatmap: %v", err)
	}
	if len(heat) != 1 {
		t.Errorf("heatmap rows = %d, want 1 (a wildcard matched another file)", len(heat))
	}
}

// TestFileReadStats_PercentIsLiteral: "%" in a real path is the multi-character
// wildcard, so without quoting one file's stats absorb a whole family.
func TestFileReadStats_PercentIsLiteral(t *testing.T) {
	st := openMetaStore(t)
	seedRead(t, st, "p1", "assets/100%.png", "model-a", 0.05)
	seedRead(t, st, "p2", "assets/100x.png", "model-b", 9.00)
	seedRead(t, st, "p3", "assets/100yyyy.png", "model-c", 9.00)

	stats, err := st.GetFileReadStats("assets/100%.png")
	if err != nil {
		t.Fatalf("GetFileReadStats: %v", err)
	}
	if stats.TotalReads != 1 {
		t.Errorf("TotalReads = %d, want 1; an unquoted %% matched every sibling", stats.TotalReads)
	}
}

// TestEventQueries_PathMetacharactersAreLiteral covers the three event-side
// readers that filter by path. FileHealth is included because its suffix arm is
// "LIKE '%/' || <caller>", which can only widen once a stored path carries a
// leading slash -- so that shape is seeded deliberately.
func TestEventQueries_PathMetacharactersAreLiteral(t *testing.T) {
	st := openMetaStore(t)
	seedMetaEvent(t, st, "e1", underscorePath, "function:x.go::Fn")
	seedMetaEvent(t, st, "e2", dashPath, "function:x.go::Fn")
	seedMetaEvent(t, st, "e3", "/abs/"+dashPath, "function:x.go::Fn")

	events, err := st.RecentEventsFiltered(50, "meta", underscorePath, time.Time{})
	if err != nil {
		t.Fatalf("RecentEventsFiltered: %v", err)
	}
	for _, e := range events {
		if e.FilePath != underscorePath {
			t.Errorf("RecentEventsFiltered returned %q, want only %q", e.FilePath, underscorePath)
		}
	}
	if len(events) != 1 {
		t.Errorf("RecentEventsFiltered rows = %d, want 1", len(events))
	}

	fh, err := st.FileHealth(underscorePath)
	if err != nil {
		t.Fatalf("FileHealth: %v", err)
	}
	if fh.RecentThrashingCount != 1 {
		t.Errorf("FileHealth.RecentThrashingCount = %d, want 1 (dash sibling counted)", fh.RecentThrashingCount)
	}

	sym, err := st.SymbolHistory(underscorePath, "", 50)
	if err != nil {
		t.Fatalf("SymbolHistory: %v", err)
	}
	for _, s := range sym {
		if s.FilePath != underscorePath {
			t.Errorf("SymbolHistory returned %q, want only %q", s.FilePath, underscorePath)
		}
	}
}

// TestSymbolHistory_SignatureUnderscoreIsLiteral: node signatures carry
// underscores constantly, and the signature clause also builds LIKE patterns.
func TestSymbolHistory_SignatureUnderscoreIsLiteral(t *testing.T) {
	st := openMetaStore(t)
	seedMetaEvent(t, st, "s1", "internal/svc/pkg.go", "function:x.go::Do_One")
	seedMetaEvent(t, st, "s2", "internal/svc/pkg.go", "function:x.go::DoXOne")

	got, err := st.SymbolHistory("internal/svc/pkg.go", "function:x.go::Do_One", 50)
	if err != nil {
		t.Fatalf("SymbolHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1; an unquoted _ in the signature matched DoXOne", len(got))
	}
	if got[0].Signature != "function:x.go::Do_One" {
		t.Errorf("Signature = %q, want the exact one asked for", got[0].Signature)
	}
}

// TestReverseDirection_StoredUnderscoreDoesNotMatchDashQuery covers the other
// side of the same class: the stored path is the LIKE pattern and the caller
// value is the subject, so a stored "file_read.go" must NOT answer a query for
// "file-read.go".
func TestReverseDirection_StoredUnderscoreDoesNotMatchDashQuery(t *testing.T) {
	st := openMetaStore(t)
	seedMetaEvent(t, st, "rev1", underscorePath, "function:x.go::Fn")
	seedRead(t, st, "rev2", underscorePath, "model-rev", 1.00)

	events, err := st.RecentEventsFiltered(50, "", dashPath, time.Time{})
	if err != nil {
		t.Fatalf("RecentEventsFiltered: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("RecentEventsFiltered(dash) rows = %d, want 0", len(events))
	}

	stats, err := st.GetFileReadStats(dashPath)
	if err != nil {
		t.Fatalf("GetFileReadStats: %v", err)
	}
	if stats.TotalReads != 0 {
		t.Errorf("GetFileReadStats(dash) reads = %d, want 0", stats.TotalReads)
	}
}

// TestMetacharacterQueries_KeepIntendedSuffixMatching is the positive control:
// the quoting must not narrow the deliberately fuzzy arms. A relative query is
// still expected to find the SAME file stored in absolute form.
func TestMetacharacterQueries_KeepIntendedSuffixMatching(t *testing.T) {
	st := openMetaStore(t)
	seedMetaEvent(t, st, "abs1", "/repo/root/"+underscorePath, "function:x.go::Fn")

	events, err := st.RecentEventsFiltered(50, "", underscorePath, time.Time{})
	if err != nil {
		t.Fatalf("RecentEventsFiltered: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("absolute form of the same file must still match; rows = %d, want 1", len(events))
	}

	fh, err := st.FileHealth(underscorePath)
	if err != nil {
		t.Fatalf("FileHealth: %v", err)
	}
	if fh.RecentThrashingCount != 1 {
		t.Errorf("RecentThrashingCount = %d, want 1 for the same file stored absolute", fh.RecentThrashingCount)
	}
}
