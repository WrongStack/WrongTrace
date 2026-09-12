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

// TestRecentEventsFiltered_AbsoluteCallerSubjectStaysRaw pins the round-90
// fix: the fourth arm of RecentEventsFiltered's filePath clause binds the
// CALLER path as the LIKE SUBJECT ("? LIKE '%' || <quoted stored path>
// ESCAPE '\'"), and ESCAPE processing affects only the pattern side, so the
// arm-4 value must stay RAW. Binding escapeLike(normSlash) there (the
// round-90 bug) made the injected backslashes literal subject characters:
// an absolute caller path containing '_' or '%' — the only arm that matches
// a caller path LONGER than the stored row — found nothing, so external
// IPC/MCP/HTTP callers passing workspace-absolute paths got zero events for
// exactly the everyday underscore-bearing file names.
func TestRecentEventsFiltered_AbsoluteCallerSubjectStaysRaw(t *testing.T) {
	st := openMetaStore(t)
	seedMetaEvent(t, st, "a1", underscorePath, "function:x.go::Fn")
	seedMetaEvent(t, st, "a2", "assets/100%.png", "function:x.go::Fn")

	// Absolute caller whose stored row is the relative suffix, '_'-bearing.
	events, err := st.RecentFileEvents("/repo/root/"+underscorePath, 50)
	if err != nil {
		t.Fatalf("RecentFileEvents: %v", err)
	}
	if len(events) != 1 || events[0].FilePath != underscorePath {
		t.Errorf("absolute '_' caller rows = %d %v, want exactly [%q]",
			len(events), eventFilePaths(events), underscorePath)
	}

	// Same mechanics with '%' in the caller path.
	events, err = st.RecentFileEvents("/srv/assets/100%.png", 50)
	if err != nil {
		t.Fatalf("RecentFileEvents: %v", err)
	}
	if len(events) != 1 || events[0].FilePath != "assets/100%.png" {
		t.Errorf("absolute '%%' caller rows = %d %v, want exactly [assets/100%%.png]",
			len(events), eventFilePaths(events))
	}

	// Isolation: the dash sibling stays out — the PATTERN side keeps its
	// quoting, so a raw subject must not turn '_' into a wildcard.
	events, err = st.RecentFileEvents("/repo/root/internal/svc/file-read.go", 50)
	if err != nil {
		t.Fatalf("RecentFileEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("dash sibling leaked: rows = %d %v, want 0", len(events), eventFilePaths(events))
	}
}

// eventFilePaths formats rows for assertion messages only.
func eventFilePaths(events []EventRecord) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.FilePath)
	}
	return out
}

// TestFileHealth_CaseInsensitiveExactSurvivesMetacharacters pins the round-91
// fix: arm 3 of FileHealth's path clause is a plain EQUALITY arm
// (LOWER(REPLACE(file_path,'\','/')) = ?), so its value must be the plain
// lowercased path. Binding escapeLike(normSlash) there (the round-91 bug)
// corrupted the value with literal backslashes and left the case mixed, so
// the ONLY arm that matches a casing-divergent EXACT query returned zero
// churn for every '_'/'%'-bearing or mixed-case path — the guardrail read
// health 100 for a file that actually churned. The underscore-free casing
// case stayed green because escapeLike is a no-op there, which is why the
// round-41 casing test (parser.go) did not catch it.
func TestFileHealth_CaseInsensitiveExactSurvivesMetacharacters(t *testing.T) {
	st := openMetaStore(t)
	seedMetaEvent(t, st, "fh1", `D:\Codebox\PROJECTS\WrongTrace\internal\svc\file_read.go`, "function:x.go::Fn")
	seedMetaEvent(t, st, "fh2", `D:\Codebox\PROJECTS\WrongTrace\internal\ast\parser.go`, "function:x.go::Fn")

	// casing-divergent exact, '_'-bearing: arm 3 is the only matching arm.
	h, err := st.FileHealth(`d:/codebox/projects/wrongtrace/internal/svc/file_read.go`)
	if err != nil {
		t.Fatalf("FileHealth: %v", err)
	}
	if h.RecentThrashingCount != 1 || h.HealthScore != 92 {
		t.Errorf("'_' recased exact: count=%d health=%d, want 1/92 (arm-3 equality value must stay plain+lowered)",
			h.RecentThrashingCount, h.HealthScore)
	}

	// mixed-case exact, underscore-free: pins the LOWERED half of the value.
	h, err = st.FileHealth(`D:/Codebox/Projects/WrongTrace/internal/ast/Parser.go`)
	if err != nil {
		t.Fatalf("FileHealth: %v", err)
	}
	if h.RecentThrashingCount != 1 || h.HealthScore != 92 {
		t.Errorf("mixed-case exact: count=%d health=%d, want 1/92 (arm-3 value must be lowered)",
			h.RecentThrashingCount, h.HealthScore)
	}

	// same-casing control (arm 2) stays green.
	h, err = st.FileHealth(`D:\Codebox\PROJECTS\WrongTrace\internal\svc\file_read.go`)
	if err != nil {
		t.Fatalf("FileHealth: %v", err)
	}
	if h.RecentThrashingCount != 1 || h.HealthScore != 92 {
		t.Errorf("same-casing control: count=%d health=%d, want 1/92", h.RecentThrashingCount, h.HealthScore)
	}

	// dash sibling must stay out before AND after the fix.
	h, err = st.FileHealth(`d:/codebox/projects/wrongtrace/internal/svc/file-read.go`)
	if err != nil {
		t.Fatalf("FileHealth: %v", err)
	}
	if h.RecentThrashingCount != 0 {
		t.Errorf("dash sibling leaked: count=%d, want 0", h.RecentThrashingCount)
	}
}

// TestSymbolHistory_AbsoluteCallerSubjectStaysRaw pins the round-91 fix:
// SymbolHistory's filePath clause is RecentEventsFiltered's five-arm clause
// copied verbatim, and arm 4 (? LIKE '%' || <quoted stored path> ESCAPE '\')
// binds the CALLER path as the LIKE SUBJECT, which must stay raw. The
// round-90 fix repaired only the RecentEventsFiltered copy — SymbolHistory
// still returned zero rows for '_'/'%'-bearing absolute caller paths
// (external IPC/MCP/HTTP callers passing workspace-absolute paths).
func TestSymbolHistory_AbsoluteCallerSubjectStaysRaw(t *testing.T) {
	st := openMetaStore(t)
	seedMetaEvent(t, st, "sh1", underscorePath, "function:x.go::Fn")
	seedMetaEvent(t, st, "sh2", underscorePath, "function:x.go::Do_One")

	// absolute '_' caller: arm 4 is the only arm that can match.
	recs, err := st.SymbolHistory("/repo/root/"+underscorePath, "", 50)
	if err != nil {
		t.Fatalf("SymbolHistory: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("absolute '_' caller rows = %d, want 2 (arm-4 subject must stay raw)", len(recs))
	}
	for _, r := range recs {
		if r.FilePath != underscorePath {
			t.Errorf("SymbolHistory returned %q, want only %q", r.FilePath, underscorePath)
		}
	}

	// dash sibling absolute query must stay out before AND after the fix.
	recs, err = st.SymbolHistory("/repo/root/internal/svc/file-read.go", "", 50)
	if err != nil {
		t.Fatalf("SymbolHistory: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("dash sibling leaked: rows = %d, want 0", len(recs))
	}

	// signature-contains control: the sig clause is unchanged by the fix.
	recs, err = st.SymbolHistory(underscorePath, "function:x.go::Do_One", 50)
	if err != nil {
		t.Fatalf("SymbolHistory: %v", err)
	}
	if len(recs) != 1 || recs[0].Signature != "function:x.go::Do_One" {
		t.Errorf("sig control rows = %d, want exactly the Do_One row", len(recs))
	}
}

// TestFileModelActivity_AbsoluteCallerSubjectStaysRaw pins the round-92 fix:
// FileModelActivity's read and write queries each carry a copy of the
// five-arm filePath clause, and arm 4 (? LIKE '%' || <quoted stored path>
// ESCAPE '\') binds the CALLER path as the LIKE SUBJECT, which must stay
// raw. The round-90/91 fixes repaired the RecentEventsFiltered, FileHealth,
// and SymbolHistory copies — FileModelActivity's two copies still returned
// zero rows for '_'/'%'-bearing absolute caller paths, hiding the file's
// entire per-model read/write telemetry. AllFileModelActivity has no path
// filter at all (global aggregates), so it carries no LIKE arm to fix.
func TestFileModelActivity_AbsoluteCallerSubjectStaysRaw(t *testing.T) {
	st := openMetaStore(t)
	seedRead(t, st, "fm1", underscorePath, "model-a", 0.25)
	seedMetaEvent(t, st, "fm2", underscorePath, "function:x.go::Fn")
	if err := st.UpsertRun(RunRecord{
		RunID: "fm-run", TaskID: "t1", AgentName: "agent",
		ModelName: "model-w", Provider: "prov",
	}); err != nil {
		t.Fatalf("UpsertRun: %v", err)
	}
	if err := st.InsertEvent(EventRecord{
		EventID: "fm3", RunID: "fm-run", RepoName: "meta", FilePath: underscorePath,
		Signature: "function:x.go::Fn", NodeType: "function",
		Action: "MODIFIED", BodyHash: "hash", LOC: 5,
		OccurredAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	// absolute '_' caller: arm 4 is the only arm that can match, on BOTH queries.
	acts, err := st.FileModelActivity("/repo/root/" + underscorePath)
	if err != nil {
		t.Fatalf("FileModelActivity: %v", err)
	}
	var reads, writes *ModelActivitySummary
	for i := range acts {
		switch acts[i].ModelName {
		case "model-a":
			reads = &acts[i]
		case "model-w":
			writes = &acts[i]
		}
	}
	if reads == nil || reads.ReadCount != 1 {
		t.Errorf("absolute '_' caller: read side for model-a missing (rows=%d) — arm-4 subject must stay raw", len(acts))
	}
	if writes == nil || writes.WriteEvents != 1 {
		t.Errorf("absolute '_' caller: write side for model-w missing (rows=%d) — arm-4 subject must stay raw", len(acts))
	}

	// dash sibling absolute query must stay out before AND after the fix.
	acts, err = st.FileModelActivity("/repo/root/internal/svc/file-read.go")
	if err != nil {
		t.Fatalf("FileModelActivity: %v", err)
	}
	if len(acts) != 0 {
		t.Errorf("dash sibling leaked: %d summaries, want 0", len(acts))
	}

	// exact-caller control (arm 2) stays green: model-a (read) + model-w
	// (attributed write) + 'unknown' (fm2 has no RunID, and the write query's
	// LEFT JOIN + COALESCE legitimately surfaces it as the sentinel).
	acts, err = st.FileModelActivity(underscorePath)
	if err != nil {
		t.Fatalf("FileModelActivity: %v", err)
	}
	if len(acts) != 3 {
		t.Errorf("exact caller rows = %d, want 3 (model-a + model-w + 'unknown')", len(acts))
	}
}
