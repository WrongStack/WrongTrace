package db

import (
	"path/filepath"
	"reflect"
	"strings"
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

// TestGetFileReadStats_AbsoluteCallerResolvesRelativeStoredPath pins the
// read-side counterpart to the round-90/91/92 subject-stays-raw family.
// readPathClause (GetFileReadStats + GetFileReadHeatmap) had only three arms —
// raw equality, normalized equality, and "stored ends with /caller" — so it
// resolved a caller path with NO MORE components than the stored row and
// nothing else. The stored read-event path is the agent transcript's own
// tool-call argument and is normally workspace-RELATIVE, while the caller
// arrives with code_node_events' file_path, which the watcher records
// ABSOLUTE. That combination matched nothing, so FileReadDetails rendered a
// file with real read telemetry as having none.
//
// The new fourth arm binds the caller path as the LIKE SUBJECT, so it must stay
// RAW, while the stored path moves to the pattern side and is quoted in SQL —
// the same shape pathMatchClause has carried since round 90.
func TestGetFileReadStats_AbsoluteCallerResolvesRelativeStoredPath(t *testing.T) {
	st := openMetaStore(t)
	// Relative stored rows, exactly as a transcript records them, plus the
	// '_'/'-' sibling that must not merge in.
	seedRead(t, st, "rp1", underscorePath, "model-mine", 0.50)
	seedRead(t, st, "rp2", dashPath, "model-theirs", 9.00)

	for _, caller := range []string{
		"/repo/root/" + underscorePath,
		`D:\Codebox\PROJECTS\WrongTrace\internal\svc\file_read.go`,
	} {
		stats, err := st.GetFileReadStats(caller)
		if err != nil {
			t.Fatalf("GetFileReadStats(%q): %v", caller, err)
		}
		if stats.TotalReads != 1 || stats.TotalCostUSD != 0.50 {
			t.Errorf("GetFileReadStats(%q) = %d reads, cost %v; want 1/0.5 — "+
				"an absolute caller must resolve the relative stored row (arm-4 subject stays raw)",
				caller, stats.TotalReads, stats.TotalCostUSD)
		}
		if got := stats.ModelBreakdown["model-mine"]; got != 1 {
			t.Errorf("GetFileReadStats(%q) ModelBreakdown = %v, want model-mine=1 only",
				caller, stats.ModelBreakdown)
		}

		heat, err := st.GetFileReadHeatmap(caller)
		if err != nil {
			t.Fatalf("GetFileReadHeatmap(%q): %v", caller, err)
		}
		if len(heat) != 1 || heat[0].ReadCount != 1 {
			t.Errorf("GetFileReadHeatmap(%q) = %d slices, want 1 (same clause, same divergence)",
				caller, len(heat))
		}
	}

	// Isolation on the new arm: the dash sibling must not answer the
	// underscore query, in either direction. The pattern side is quoted in SQL,
	// so a raw subject must not turn '_' into a wildcard here either.
	for _, caller := range []string{"/repo/root/" + dashPath, dashPath} {
		stats, err := st.GetFileReadStats(caller)
		if err != nil {
			t.Fatalf("GetFileReadStats(%q): %v", caller, err)
		}
		if stats.TotalReads != 1 || stats.TotalCostUSD != 9.00 {
			t.Errorf("dash sibling query %q = %d reads, cost %v; want exactly its own 1/9.0",
				caller, stats.TotalReads, stats.TotalCostUSD)
		}
	}

	// Opposite direction must not regress: a relative caller against a stored
	// ABSOLUTE row still resolves through the pre-existing third arm.
	seedRead(t, st, "rp3", "/srv/work/internal/svc/other.go", "model-abs", 0.25)
	stats, err := st.GetFileReadStats("internal/svc/other.go")
	if err != nil {
		t.Fatalf("GetFileReadStats: %v", err)
	}
	if stats.TotalReads != 1 || stats.TotalCostUSD != 0.25 {
		t.Errorf("relative caller vs stored absolute = %d reads, cost %v; want 1/0.25",
			stats.TotalReads, stats.TotalCostUSD)
	}
}

// TestPerFileClauses_CallerLongerResolvesAbsoluteStoredPath pins the round-87
// fix. The caller-longer LIKE arm builds its pattern as "'%/' || <stored>".
// Watcher-recorded file_path values are ABSOLUTE, so for an absolute stored row
// that concatenation doubled the separator ("%//repo/root/...") and a caller
// carrying merely more leading components could never satisfy it: the whole
// caller-longer direction was dead for the stored spelling the daemon produces
// most often, in BOTH clause families. Both now LTRIM the stored path's own
// leading '/' after metacharacter quoting.
//
// The trim must not dissolve the '/' boundary anchor -- that anchor is what
// stops "myfile_read.go" from answering a query for "file_read.go" -- and it
// must not over-trim into a false negative either, so both directions are
// asserted, not just the happy one.
func TestPerFileClauses_CallerLongerResolvesAbsoluteStoredPath(t *testing.T) {
	const abs = "/repo/root/internal/svc/file_read.go"
	const rel = "internal/svc/file_read.go"
	const longer = "/other/prefix" + abs

	// seed builds a store holding exactly one event + one read row at path,
	// with the event attributed to a run so FileModelActivity's write side
	// resolves it as its own model rather than the 'unknown' sentinel.
	seed := func(t *testing.T, path string) *Store {
		t.Helper()
		st := openMetaStore(t)
		if err := st.UpsertRun(RunRecord{
			RunID: "a87", TaskID: "t", AgentName: "agent", ModelName: "model-write", Provider: "prov",
		}); err != nil {
			t.Fatalf("UpsertRun: %v", err)
		}
		if err := st.InsertEvent(EventRecord{
			EventID: "a87e", RunID: "a87", RepoName: "meta", FilePath: path,
			Signature: "function:file_read.go::Fn", NodeType: "function",
			Action: "MODIFIED", BodyHash: "hash", LOC: 5,
			OccurredAt: time.Now().UTC().Add(-time.Minute),
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
		seedRead(t, st, "a87r", path, "model-read", 0.5)
		return st
	}

	// Every per-file reader, on both clause families, must resolve the
	// absolute-stored row from a longer caller.
	for _, tc := range []struct {
		name string
		call func(*Store) (int, string)
	}{
		{"RecentFileEvents", func(s *Store) (int, string) {
			r, err := s.RecentFileEvents(longer, 50)
			if err != nil {
				return -1, err.Error()
			}
			return len(r), "rows"
		}},
		{"SymbolHistory", func(s *Store) (int, string) {
			r, err := s.SymbolHistory(longer, "", 50)
			if err != nil {
				return -1, err.Error()
			}
			return len(r), "rows"
		}},
		{"FileModelActivity", func(s *Store) (int, string) {
			r, err := s.FileModelActivity(longer)
			if err != nil {
				return -1, err.Error()
			}
			return len(r), "summaries"
		}},
		{"GetFileReadStats", func(s *Store) (int, string) {
			r, err := s.GetFileReadStats(longer)
			if err != nil {
				return -1, err.Error()
			}
			return r.TotalReads, "reads"
		}},
		{"GetFileReadHeatmap", func(s *Store) (int, string) {
			r, err := s.GetFileReadHeatmap(longer)
			if err != nil {
				return -1, err.Error()
			}
			return len(r), "slices"
		}},
	} {
		t.Run("absolute-stored/"+tc.name, func(t *testing.T) {
			st := seed(t, abs)
			// FileModelActivity reports one summary per model: the read-side
			// model and the attributed write-side model.
			want := 1
			if tc.name == "FileModelActivity" {
				want = 2
			}
			got, unit := tc.call(st)
			if got != want {
				t.Errorf("%s(caller=%q) = %d %s, want %d — the '/'-anchored arm "+
					"doubles the separator when the stored path is absolute, so the "+
					"caller-longer direction matches nothing", tc.name, longer, got, unit, want)
			}
		})
	}

	// The relative-stored case was already working and must not regress: the
	// LTRIM is a no-op there, so the pattern must be byte-identical to before.
	t.Run("relative-stored-unchanged", func(t *testing.T) {
		st := seed(t, rel)
		if r, err := st.RecentFileEvents("/repo/root/"+rel, 50); err != nil || len(r) != 1 {
			t.Errorf("RecentFileEvents = %d rows err=%v, want 1 (relative stored + longer caller)", len(r), err)
		}
		s, err := st.GetFileReadStats("/repo/root/" + rel)
		if err != nil || s.TotalReads != 1 {
			t.Errorf("GetFileReadStats = %d reads err=%v, want 1 (relative stored + longer caller)", s.TotalReads, err)
		}
	})

	// Anchor survives: a tail glued directly onto the preceding text with no
	// separator is a DIFFERENT file. A naive "'%' || stored" fix would absorb
	// it, silently merging another file's history.
	t.Run("anchor-rejects-unseparated-tail", func(t *testing.T) {
		st := seed(t, abs)
		for _, bad := range []string{
			"/other/prefixmy" + abs[1:],                         // "myrepo", no '/' before the tail
			"/other/prefix/repo/root/internal/svc/file-read.go", // the '_' -> '-' sibling
		} {
			r, err := st.RecentFileEvents(bad, 50)
			if err != nil {
				t.Fatalf("RecentFileEvents(%q): %v", bad, err)
			}
			if len(r) != 0 {
				t.Errorf("caller %q matched %d row(s), want 0 — the '/' boundary "+
					"anchor was lost, so an unrelated path is absorbed", bad, len(r))
			}
		}
	})

	// No over-trim: a caller that really does end with the stored tail at a '/'
	// boundary is a legitimate suffix match and must still resolve.
	t.Run("genuine-boundary-suffix-resolves", func(t *testing.T) {
		st := seed(t, abs)
		genuine := "/other/prefixmy" + abs // ".../prefixmy/repo/root/..."
		r, err := st.RecentFileEvents(genuine, 50)
		if err != nil {
			t.Fatalf("RecentFileEvents: %v", err)
		}
		if len(r) != 1 {
			t.Errorf("caller %q = %d rows, want 1 — LTRIM must not over-trim into "+
				"a false negative", genuine, len(r))
		}
	})

	// '_' stays literal on the newly enabled direction, and the shorter-caller
	// arm is untouched by the change.
	t.Run("metacharacter-and-shorter-caller-intact", func(t *testing.T) {
		st := seed(t, "/repo/root/internal/svc/file-read.go")
		r, err := st.RecentFileEvents("/other/prefix/repo/root/internal/svc/file-read.go", 50)
		if err != nil {
			t.Fatalf("RecentFileEvents: %v", err)
		}
		if len(r) != 1 || r[0].FilePath != "/repo/root/internal/svc/file-read.go" {
			t.Errorf("underscore isolation on the longer-caller arm: %d rows %v, want exactly its own row",
				len(r), eventFilePaths(r))
		}
	})

	t.Run("shorter-caller-unchanged", func(t *testing.T) {
		st := seed(t, abs)
		r, err := st.RecentFileEvents("file_read.go", 50)
		if err != nil || len(r) != 1 {
			t.Errorf("shorter caller vs absolute stored = %d rows err=%v, want 1", len(r), err)
		}
	})
}

// TestFileHealth_ResolvesAgentSuppliedPathSpellings pins the round-88 fix.
// FileHealth's caller path is the most agent-supplied of the per-file queries --
// guardrails.go, mcp/server.go, ipc/socket.go and the HTTP handler all pass a
// path the AGENT chose -- and it was missing two things every sibling clause
// already had:
//
//   - TrimPrefix("./"), so an agent writing "./pkg/x.go" for the stored
//     "pkg/x.go" matched nothing;
//   - any caller-LONGER arm at all, so an absolute caller against a relative
//     stored row matched nothing.
//
// Both failures are silent and share one shape: the guardrail reported
// RecentThrashingCount=0 and HealthScore=100, i.e. "safe to modify", for a file
// that had churned inside the window. Rounds 41 and 91 fixed FileHealth's
// CASING; neither touched arm coverage, so this is a distinct root cause.
func TestFileHealth_ResolvesAgentSuppliedPathSpellings(t *testing.T) {
	const rel = "internal/svc/file_read.go"
	const relDash = "internal/svc/file-read.go"
	const abs = "/repo/root/internal/svc/file_read.go"
	const absDash = "/repo/root/internal/svc/file-read.go"

	// oneEdit asserts the caller resolved the single stored row: churn counted
	// and the score penalized (100 - 8 for a single edit).
	oneEdit := func(t *testing.T, st *Store, caller string) (int, int) {
		t.Helper()
		h, err := st.FileHealth(caller)
		if err != nil {
			t.Fatalf("FileHealth(%q): %v", caller, err)
		}
		return h.RecentThrashingCount, h.HealthScore
	}

	// The "./"-prefixed relative path is what an agent actually sends, and it
	// must resolve against either stored spelling.
	t.Run("dot-slash-agent-caller", func(t *testing.T) {
		for _, stored := range []string{rel, abs} {
			st := openMetaStore(t)
			seedMetaEvent(t, st, "d1", stored, "function:file_read.go::Fn")
			calls, score := oneEdit(t, st, "./"+rel)
			if calls != 1 || score != 92 {
				t.Errorf("caller %q against stored %q = %d churn / health %d, want 1/92 — "+
					"FileHealth never trimmed the './' prefix its siblings trim, so the "+
					"agent's own spelling of the file read as a healthy 100",
					"./"+rel, stored, calls, score)
			}
		}
	})

	// The caller-longer direction, for both the '_' and '-' spellings.
	t.Run("caller-longer", func(t *testing.T) {
		for _, tc := range []struct{ stored, caller string }{
			{rel, "/other/prefix" + abs},
			{relDash, "/other/prefix" + absDash},
		} {
			st := openMetaStore(t)
			seedMetaEvent(t, st, "d2", tc.stored, "function:file_read.go::Fn")
			calls, score := oneEdit(t, st, tc.caller)
			if calls != 1 || score != 92 {
				t.Errorf("caller %q against stored %q = %d churn / health %d, want 1/92 — "+
					"every FileHealth arm required the caller to be no longer than the "+
					"stored row, so this direction was missing entirely",
					tc.caller, tc.stored, calls, score)
			}
		}
	})

	// Controls that must keep passing.
	t.Run("control-exact-caller", func(t *testing.T) {
		for _, stored := range []string{rel, abs} {
			st := openMetaStore(t)
			seedMetaEvent(t, st, "c1", stored, "function:file_read.go::Fn")
			if calls, score := oneEdit(t, st, stored); calls != 1 || score != 92 {
				t.Errorf("exact caller %q = %d/%d, want 1/92", stored, calls, score)
			}
		}
	})

	// Agent-supplied CASING divergence -- the case the LOWER arms exist for.
	// Must be a casing variant of the SAME path, or the control proves nothing.
	t.Run("control-casing-divergence", func(t *testing.T) {
		st := openMetaStore(t)
		seedMetaEvent(t, st, "c2", abs, "function:file_read.go::Fn")
		upper := strings.ToUpper(abs)
		if calls, _ := oneEdit(t, st, upper); calls != 1 {
			t.Errorf("case variant %q = %d churn, want 1", upper, calls)
		}
	})

	t.Run("control-shorter-caller", func(t *testing.T) {
		st := openMetaStore(t)
		seedMetaEvent(t, st, "c3", abs, "function:file_read.go::Fn")
		if calls, _ := oneEdit(t, st, "file_read.go"); calls != 1 {
			t.Errorf("shorter caller = %d churn, want 1", calls)
		}
	})

	// '_' must stay literal: no sibling may answer across the new arm.
	t.Run("control-metacharacter-isolation", func(t *testing.T) {
		for _, tc := range []struct{ stored, caller string }{
			{relDash, "./" + rel},            // './' caller must not see the dash row
			{rel, "./" + relDash},            // dash caller must not see the underscore row
			{absDash, "/other/prefix" + abs}, // longer caller must not see the dash row
		} {
			st := openMetaStore(t)
			seedMetaEvent(t, st, "c4", tc.stored, "function:file_read.go::Fn")
			if calls, _ := oneEdit(t, st, tc.caller); calls != 0 {
				t.Errorf("caller %q matched the stored %q row (%d churn, want 0) — "+
					"'_' is being treated as a wildcard", tc.caller, tc.stored, calls)
			}
		}
	})

	// The '/' boundary anchor must survive: a tail glued on with no separator is
	// a DIFFERENT file, and a naive unanchored fix would merge its churn into
	// this file's health score.
	t.Run("control-anchor-rejects-unseparated-tail", func(t *testing.T) {
		st := openMetaStore(t)
		seedMetaEvent(t, st, "c5", rel, "function:file_read.go::Fn")
		glued := "/other/prefixmy" + rel // "prefixmyinternal", no '/' before the tail
		if calls, _ := oneEdit(t, st, glued); calls != 0 {
			t.Errorf("caller %q = %d churn, want 0 — the '/' boundary anchor was lost", glued, calls)
		}
	})

	// No over-trim: a caller genuinely ending at a '/' boundary is a legitimate
	// suffix and must still resolve. Note the explicit separator -- it is what
	// makes this the opposite of the glued case above.
	t.Run("control-genuine-boundary-suffix-resolves", func(t *testing.T) {
		st := openMetaStore(t)
		seedMetaEvent(t, st, "c6", rel, "function:file_read.go::Fn")
		genuine := "/other/prefixmy/" + rel
		if calls, _ := oneEdit(t, st, genuine); calls != 1 {
			t.Errorf("caller %q = %d churn, want 1 — LTRIM over-trimmed into a miss", genuine, calls)
		}
	})

	// The clause's own 24h window must still exclude stale churn.
	t.Run("control-24h-window", func(t *testing.T) {
		st := openMetaStore(t)
		if err := st.InsertEvent(EventRecord{
			EventID: "c7", RepoName: "meta", FilePath: rel,
			Signature: "function:file_read.go::Fn", NodeType: "function",
			Action: "MODIFIED", BodyHash: "hash", LOC: 5,
			OccurredAt: time.Now().UTC().Add(-48 * time.Hour),
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
		if calls, score := oneEdit(t, st, rel); calls != 0 || score != 100 {
			t.Errorf("stale churn = %d/%d, want 0/100 — the 24h window must survive the new arm",
				calls, score)
		}
	})
}

// TestPerFileClauses_DriveRootedStoredResolvesLongerCaller pins the round-89
// fix. A Windows drive root ("D:/...") is a root marker the leading-slash trim
// added in rounds 87/88 does not touch, so the caller-longer arm built the
// pattern "%/D:/repo/...". No caller could satisfy it, because the caller's own
// drive letter also sits at position 0 rather than after a separator. On Windows
// -- where the watcher records every stored path drive-rooted -- that left the
// caller-longer direction dead for the platform's normal spelling, in all three
// clause families.
//
// The stored value now reduces through the same CASE that strips the drive, so a
// drive-rooted row reaches the component-suffix form every other stored path
// already has. All three families now build this arm from one helper,
// callerLongerArm, precisely so the boundary rule cannot drift between them
// again (rounds 85 and 87 each hand-mirrored it and inherited the other's flaw).
func TestPerFileClauses_DriveRootedStoredResolvesLongerCaller(t *testing.T) {
	// Exactly what the Windows watcher records.
	const win = `D:\repo\root\internal\svc\file_read.go`
	const winSlash = "D:/repo/root/internal/svc/file_read.go"
	const winDash = `D:\repo\root\internal\svc\file-read.go`
	const deeper = "D:/other/prefix/repo/root/internal/svc/file_read.go"
	const rel = "internal/svc/file_read.go"
	const absPosix = "/repo/root/internal/svc/file_read.go"

	// seed builds a store with exactly one event + one read row at path, the
	// event attributed to a run so FileModelActivity's write side resolves it as
	// its own model rather than the 'unknown' sentinel.
	seed := func(t *testing.T, path string) *Store {
		t.Helper()
		st := openMetaStore(t)
		if err := st.UpsertRun(RunRecord{
			RunID: "a89", TaskID: "t", AgentName: "agent", ModelName: "model-write", Provider: "prov",
		}); err != nil {
			t.Fatalf("UpsertRun: %v", err)
		}
		if err := st.InsertEvent(EventRecord{
			EventID: "a89e", RunID: "a89", RepoName: "meta", FilePath: path,
			Signature: "function:file_read.go::Fn", NodeType: "function",
			Action: "MODIFIED", BodyHash: "hash", LOC: 5,
			OccurredAt: time.Now().UTC().Add(-time.Minute),
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
		seedRead(t, st, "a89r", path, "model-read", 0.5)
		return st
	}

	// Every per-file reader, in all three clause families, must resolve the
	// drive-rooted row from a deeper caller on the same drive.
	for _, tc := range []struct {
		name string
		call func(*Store) (int, string)
	}{
		{"RecentFileEvents", func(s *Store) (int, string) {
			r, err := s.RecentFileEvents(deeper, 50)
			if err != nil {
				return -1, err.Error()
			}
			return len(r), "rows"
		}},
		{"SymbolHistory", func(s *Store) (int, string) {
			r, err := s.SymbolHistory(deeper, "", 50)
			if err != nil {
				return -1, err.Error()
			}
			return len(r), "rows"
		}},
		{"FileModelActivity", func(s *Store) (int, string) {
			r, err := s.FileModelActivity(deeper)
			if err != nil {
				return -1, err.Error()
			}
			return len(r), "summaries"
		}},
		{"GetFileReadStats", func(s *Store) (int, string) {
			r, err := s.GetFileReadStats(deeper)
			if err != nil {
				return -1, err.Error()
			}
			return r.TotalReads, "reads"
		}},
		{"GetFileReadHeatmap", func(s *Store) (int, string) {
			r, err := s.GetFileReadHeatmap(deeper)
			if err != nil {
				return -1, err.Error()
			}
			return len(r), "slices"
		}},
		{"FileHealth", func(s *Store) (int, string) {
			r, err := s.FileHealth(deeper)
			if err != nil {
				return -1, err.Error()
			}
			return r.HealthScore, "health"
		}},
	} {
		t.Run("drive-stored/"+tc.name, func(t *testing.T) {
			st := seed(t, win)
			want := 1
			if tc.name == "FileModelActivity" {
				want = 2 // read-side model + attributed write-side model
			}
			if tc.name == "FileHealth" {
				want = 92 // one edit: 100 - 8, the score the guardrail consumes
			}
			got, unit := tc.call(st)
			if got != want {
				t.Errorf("%s: drive-rooted stored %q + deeper caller %q = %d %s, want %d — "+
					"the arm trimmed only leading slashes, so the pattern became "+
					"\"%%/D:/repo/...\" and a caller whose drive letter also sits at "+
					"position 0 could never match it", tc.name, win, deeper, got, unit, want)
			}
		})
	}

	// The fix must not be tied to one stored shape or one file name.
	t.Run("variants", func(t *testing.T) {
		for _, tc := range []struct{ stored, caller string }{
			{winSlash, deeper},
			{winDash, "D:/other/prefix/repo/root/internal/svc/file-read.go"},
		} {
			st := seed(t, tc.stored)
			r, err := st.RecentFileEvents(tc.caller, 50)
			if err != nil {
				t.Fatalf("RecentFileEvents: %v", err)
			}
			if len(r) != 1 {
				t.Errorf("stored %q + caller %q = %d rows, want 1", tc.stored, tc.caller, len(r))
			}
		}
	})

	// Controls.
	t.Run("control-non-drive-stored-unaffected", func(t *testing.T) {
		// The drive strip must be a no-op here. Note the explicit '/': without
		// it the caller GLUES onto the tail and matches nothing.
		for _, tc := range []struct{ stored, caller string }{
			{rel, "/other/prefix/" + rel},
			{absPosix, "/other/prefix" + absPosix}, // supplies its own leading '/'
		} {
			st := seed(t, tc.stored)
			if r, err := st.RecentFileEvents(tc.caller, 50); err != nil || len(r) != 1 {
				t.Errorf("non-drive stored %q = %d rows err=%v, want 1", tc.stored, len(r), err)
			}
			if s, err := st.GetFileReadStats(tc.caller); err != nil || s.TotalReads != 1 {
				t.Errorf("read stats for non-drive stored %q = %d reads err=%v, want 1",
					tc.stored, s.TotalReads, err)
			}
		}
	})

	t.Run("control-exact-and-shorter-caller", func(t *testing.T) {
		st := seed(t, win)
		for _, caller := range []string{win, "file_read.go"} {
			r, err := st.RecentFileEvents(caller, 50)
			if err != nil || len(r) != 1 {
				t.Errorf("caller %q = %d rows err=%v, want 1", caller, len(r), err)
			}
		}
		if h, err := st.FileHealth("file_read.go"); err != nil || h.HealthScore != 92 {
			t.Errorf("FileHealth shorter caller = %d err=%v, want 92", h.HealthScore, err)
		}
	})

	t.Run("control-anchor-rejects-unseparated-tail", func(t *testing.T) {
		st := seed(t, win)
		// No '/' before "repo" -- a different file. A naive unanchored fix
		// would merge another file's telemetry into this one.
		glued := "D:/other/prefixmyrepo/root/internal/svc/file_read.go"
		r, err := st.RecentFileEvents(glued, 50)
		if err != nil {
			t.Fatalf("RecentFileEvents: %v", err)
		}
		if len(r) != 0 {
			t.Errorf("caller %q = %d rows, want 0 — the '/' boundary anchor was lost", glued, len(r))
		}
	})

	t.Run("control-metacharacter-isolation", func(t *testing.T) {
		st := seed(t, winDash)
		// '_' must stay literal on the newly-enabled direction.
		caller := "D:/other/prefix/repo/root/internal/svc/file_read.go"
		r, err := st.RecentFileEvents(caller, 50)
		if err != nil {
			t.Fatalf("RecentFileEvents: %v", err)
		}
		if len(r) != 0 {
			t.Errorf("underscore caller %q matched the stored dash row (%d rows, want 0) — "+
				"'_' is a wildcard on the pattern side", caller, len(r))
		}
	})

	t.Run("control-cross-drive-suffix-matches", func(t *testing.T) {
		// Pin the consequence explicitly rather than leaving it implicit.
		// Stripping the drive root means a DIFFERENT drive carrying the same
		// component suffix now matches. That is the per-file readers' existing
		// policy made uniform — a stored "internal/x.go" already answers an
		// unrelated "/elsewhere/internal/x.go", because these clauses match on
		// path-COMPONENT suffix and have never compared roots.
		st := seed(t, win)
		other := "E:/other/repo/root/internal/svc/file_read.go"
		r, err := st.RecentFileEvents(other, 50)
		if err != nil {
			t.Fatalf("RecentFileEvents: %v", err)
		}
		if len(r) != 1 {
			t.Errorf("cross-drive caller %q = %d rows, want 1 — these readers match on "+
				"component suffix and never compare roots", other, len(r))
		}
	})
}

// TestRecentEventsFiltered_SameSecondTiesAreDeterministic pins the round-93
// fix: fmtDBTime stores event_time at SECOND granularity, so same-second
// rows tie under `ORDER BY e.event_time DESC` and SQLite returns them in
// physical (rowid) order — the feed order then depended on insertion order,
// not on the data (mirrored insertion into two identical stores produced
// reversed feeds; the same artifact was observed directly in round 90).
// event_time DESC stays the primary key and event_id DESC breaks ties — the
// same remedy the friction query's LAG window received in round 25.
func TestRecentEventsFiltered_SameSecondTiesAreDeterministic(t *testing.T) {
	dir := t.TempDir()
	st1, err := Open(filepath.Join(dir, "one.db"))
	if err != nil {
		t.Fatalf("open one: %v", err)
	}
	defer st1.Close()
	st2, err := Open(filepath.Join(dir, "two.db"))
	if err != nil {
		t.Fatalf("open two: %v", err)
	}
	defer st2.Close()
	if err := st1.Migrate(); err != nil {
		t.Fatalf("migrate one: %v", err)
	}
	if err := st2.Migrate(); err != nil {
		t.Fatalf("migrate two: %v", err)
	}

	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	ids := []string{"ev-1", "ev-2", "ev-3", "ev-4", "ev-5"}
	at := func(i int) time.Time { return base.Add(time.Duration(i) * 100 * time.Millisecond) }
	seed := func(st *Store, id string, at2 time.Time) {
		if err := st.InsertEvent(EventRecord{
			EventID: id, RepoName: "meta", FilePath: underscorePath,
			Signature: "function:x.go::Fn", NodeType: "function",
			Action: "MODIFIED", BodyHash: "hash", LOC: 5,
			OccurredAt: at2,
		}); err != nil {
			t.Fatalf("InsertEvent %s: %v", id, err)
		}
	}

	// Identical logical rows, opposite physical insertion orders.
	for i, id := range ids {
		seed(st1, id, at(i))
	}
	seed(st1, "ev-0", base.Add(2*time.Second))
	seed(st2, "ev-0", base.Add(2*time.Second))
	for i := len(ids) - 1; i >= 0; i-- {
		seed(st2, ids[i], at(i))
	}

	order := func(st *Store) []string {
		t.Helper()
		evs, err := st.RecentEventsFiltered(50, "", "", time.Time{})
		if err != nil {
			t.Fatalf("RecentEventsFiltered: %v", err)
		}
		out := make([]string, 0, len(evs))
		for _, e := range evs {
			out = append(out, e.EventID)
		}
		return out
	}

	ord1 := order(st1)
	ord2 := order(st2)
	if len(ord1) != len(ids)+1 || len(ord2) != len(ids)+1 {
		t.Fatalf("rows = %d/%d, want %d each", len(ord1), len(ord2), len(ids)+1)
	}
	if !reflect.DeepEqual(ord1, ord2) {
		t.Errorf("identical logical data, mirrored insertion order: orders differ\none: %v\ntwo: %v", ord1, ord2)
	}
	if ord1[0] != "ev-0" {
		t.Errorf("first row = %q, want ev-0 (event_time DESC must dominate the tiebreak)", ord1[0])
	}
}

// TestSymbolHistory_SameSecondTiesAreDeterministic pins the round-94 fix,
// the last member of the round-93 tie class: SymbolHistory's four query
// variants ordered by bare e.event_time, which fmtDBTime stores at SECOND
// granularity, so same-second rows tied and SQLite returned them in physical
// (rowid) order — the revision timeline depended on insertion order, not on
// the data. event_id now breaks ties in the same direction as the time
// column (ASC variants chronological, DESC variant reverse), the same remedy
// RecentEventsFiltered received in round 93.
func TestSymbolHistory_SameSecondTiesAreDeterministic(t *testing.T) {
	dir := t.TempDir()
	st1, err := Open(filepath.Join(dir, "one.db"))
	if err != nil {
		t.Fatalf("open one: %v", err)
	}
	defer st1.Close()
	st2, err := Open(filepath.Join(dir, "two.db"))
	if err != nil {
		t.Fatalf("open two: %v", err)
	}
	defer st2.Close()
	if err := st1.Migrate(); err != nil {
		t.Fatalf("migrate one: %v", err)
	}
	if err := st2.Migrate(); err != nil {
		t.Fatalf("migrate two: %v", err)
	}

	const (
		path = "internal/svc/sym.go"
		sig  = "func:sym.go::Handle"
	)
	ids := []string{"ev-aaa1", "ev-bbb2", "ev-ccc3"}    // lexical == pinned ASC tie order
	at := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC) // one shared second
	seed := func(st *Store, id string) {
		t.Helper()
		if err := st.InsertEvent(EventRecord{
			EventID: id, RepoName: "meta", FilePath: path,
			Signature: sig, NodeType: "function",
			Action: "MODIFIED", BodyHash: "hash", LOC: 5,
			OccurredAt: at,
		}); err != nil {
			t.Fatalf("InsertEvent %s: %v", id, err)
		}
	}

	// Identical logical rows, opposite physical insertion orders.
	for _, id := range []string{"ev-ccc3", "ev-aaa1", "ev-bbb2"} {
		seed(st1, id)
	}
	for _, id := range []string{"ev-bbb2", "ev-ccc3", "ev-aaa1"} {
		seed(st2, id)
	}

	order := func(st *Store, filePathArg, sigArg string) []string {
		t.Helper()
		recs, err := st.SymbolHistory(filePathArg, sigArg, 50)
		if err != nil {
			t.Fatalf("SymbolHistory(%q,%q): %v", filePathArg, sigArg, err)
		}
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.EventID)
		}
		if len(out) != len(ids) {
			t.Fatalf("SymbolHistory(%q,%q) rows = %d, want %d", filePathArg, sigArg, len(out), len(ids))
		}
		return out
	}

	asc := []string{"ev-aaa1", "ev-bbb2", "ev-ccc3"}
	desc := []string{"ev-ccc3", "ev-bbb2", "ev-aaa1"}
	for _, tc := range []struct {
		variant       string
		filePath, sig string
		want          []string
	}{
		{"file+signature", path, sig, asc},
		{"file-only", path, "", asc},
		{"signature-only", "", sig, asc},
		{"global", "", "", desc},
	} {
		t.Run(tc.variant, func(t *testing.T) {
			ord1 := order(st1, tc.filePath, tc.sig)
			ord2 := order(st2, tc.filePath, tc.sig)
			if !reflect.DeepEqual(ord1, ord2) {
				t.Errorf("identical logical data, mirrored insertion order: orders differ\none: %v\ntwo: %v", ord1, ord2)
			}
			if !reflect.DeepEqual(ord1, tc.want) {
				t.Errorf("tie order = %v, want %v (event_id must break same-second ties in the time column's direction)", ord1, tc.want)
			}
		})
	}
}

// TestRecentTraces_SameSecondTiesAreDeterministic pins the round-95 fix,
// another member of the round-93 tie class: RecentTraces ordered by bare
// `timestamp`, which InsertTrace writes at SECOND granularity (fmtDBTime /
// CURRENT_TIMESTAMP fallback), so same-second rows tied and SQLite returned
// them in physical (rowid) order — the runtime-traces feed depended on
// insertion order, not on the data. Ties are routine here: a whole OTLP span
// batch is inserted synchronously inside one wall-clock second. trace_id now
// breaks ties DESC, the same direction as the time column (the rounds
// 25/93/94 remedy).
func TestRecentTraces_SameSecondTiesAreDeterministic(t *testing.T) {
	dir := t.TempDir()
	st1, err := Open(filepath.Join(dir, "one.db"))
	if err != nil {
		t.Fatalf("open one: %v", err)
	}
	defer st1.Close()
	st2, err := Open(filepath.Join(dir, "two.db"))
	if err != nil {
		t.Fatalf("open two: %v", err)
	}
	defer st2.Close()
	if err := st1.Migrate(); err != nil {
		t.Fatalf("migrate one: %v", err)
	}
	if err := st2.Migrate(); err != nil {
		t.Fatalf("migrate two: %v", err)
	}

	ids := []string{"tr-aaa1", "tr-bbb2", "tr-ccc3"}    // lexical == pinned tie order
	at := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC) // one shared second
	seed := func(st *Store, id string) {
		t.Helper()
		if err := st.InsertTrace(RuntimeTraceRecord{
			TraceID:       id,
			ServiceName:   "meta",
			NodeSignature: "func:proof.go::Handle",
			DurationMs:    1.5,
			StatusCode:    200,
			ProfilerType:  "custom",
			MetadataJSON:  "{}",
			Timestamp:     at,
		}); err != nil {
			t.Fatalf("InsertTrace %s: %v", id, err)
		}
	}

	// Identical logical rows, opposite physical insertion orders.
	for _, id := range []string{"tr-ccc3", "tr-aaa1", "tr-bbb2"} {
		seed(st1, id)
	}
	for _, id := range []string{"tr-bbb2", "tr-ccc3", "tr-aaa1"} {
		seed(st2, id)
	}

	order := func(st *Store) []string {
		t.Helper()
		recs, err := st.RecentTraces(50)
		if err != nil {
			t.Fatalf("RecentTraces: %v", err)
		}
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.TraceID)
		}
		if len(out) != len(ids) {
			t.Fatalf("RecentTraces rows = %d, want %d", len(out), len(ids))
		}
		return out
	}

	want := []string{"tr-ccc3", "tr-bbb2", "tr-aaa1"} // timestamp DESC, trace_id DESC
	ord1, ord2 := order(st1), order(st2)
	if !reflect.DeepEqual(ord1, ord2) {
		t.Errorf("identical logical data, mirrored insertion order: orders differ\none: %v\ntwo: %v", ord1, ord2)
	}
	if !reflect.DeepEqual(ord1, want) {
		t.Errorf("tie order = %v, want %v (trace_id must break same-second ties in the time column's direction)", ord1, want)
	}
}

// TestGetRecentFileReads_SameSecondTiesAreDeterministic pins the round-97
// fix, the fifth member of the round-93 tie class: GetRecentFileReads (both
// variants) ordered by bare read_time, which InsertReadEvent writes at
// SECOND granularity (fmtDBTime / CURRENT_TIMESTAMP fallback), so
// same-second reads tied and SQLite returned them in physical (rowid) order
// — the recent-reads feed depended on insertion order, not on the data.
// read_id now breaks ties DESC, the same direction as the time column (the
// rounds 25/93/94/95 remedy).
func TestGetRecentFileReads_SameSecondTiesAreDeterministic(t *testing.T) {
	dir := t.TempDir()
	st1, err := Open(filepath.Join(dir, "one.db"))
	if err != nil {
		t.Fatalf("open one: %v", err)
	}
	defer st1.Close()
	st2, err := Open(filepath.Join(dir, "two.db"))
	if err != nil {
		t.Fatalf("open two: %v", err)
	}
	defer st2.Close()
	if err := st1.Migrate(); err != nil {
		t.Fatalf("migrate one: %v", err)
	}
	if err := st2.Migrate(); err != nil {
		t.Fatalf("migrate two: %v", err)
	}

	ids := []string{"rd-aaa1", "rd-bbb2", "rd-ccc3"}    // lexical == pinned tie order
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC) // one shared second
	seed := func(st *Store, id string) {
		t.Helper()
		if err := st.InsertReadEvent(FileReadRecord{
			ReadID: id, RepoName: "meta", FilePath: "internal/svc/reads.go",
			AgentName: "agent", ModelName: "model-a", Provider: "prov",
			ToolName: "read_file", LinesReadCount: 10,
			ReadTime: at,
		}); err != nil {
			t.Fatalf("InsertReadEvent %s: %v", id, err)
		}
	}

	// Identical logical rows, opposite physical insertion orders.
	for _, id := range []string{"rd-ccc3", "rd-aaa1", "rd-bbb2"} {
		seed(st1, id)
	}
	for _, id := range []string{"rd-bbb2", "rd-ccc3", "rd-aaa1"} {
		seed(st2, id)
	}

	order := func(st *Store, repo string) []string {
		t.Helper()
		recs, err := st.GetRecentFileReads(50, repo)
		if err != nil {
			t.Fatalf("GetRecentFileReads: %v", err)
		}
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.ReadID)
		}
		if len(out) != len(ids) {
			t.Fatalf("GetRecentFileReads rows = %d, want %d", len(out), len(ids))
		}
		return out
	}

	want := []string{"rd-ccc3", "rd-bbb2", "rd-aaa1"} // read_time DESC, read_id DESC
	for _, tc := range []struct{ name, repo string }{
		{"no-repo", ""},
		{"repo-filtered", "meta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ord1 := order(st1, tc.repo)
			ord2 := order(st2, tc.repo)
			if !reflect.DeepEqual(ord1, ord2) {
				t.Errorf("identical logical data, mirrored insertion order: orders differ\none: %v\ntwo: %v", ord1, ord2)
			}
			if !reflect.DeepEqual(ord1, want) {
				t.Errorf("tie order = %v, want %v (read_id must break same-second ties in the time column's direction)", ord1, want)
			}
		})
	}
}

// TestGetFileReadStats_RecentReads_SameSecondTiesAreDeterministic pins the
// round-97 fix for GetFileReadStats's recent-reads section: same-second
// reads tied under bare `ORDER BY read_time DESC` (read_time is stored at
// SECOND granularity), so the per-file timeline depended on insertion
// order. read_id breaks ties DESC like every other feed in this package.
func TestGetFileReadStats_RecentReads_SameSecondTiesAreDeterministic(t *testing.T) {
	dir := t.TempDir()
	st1, err := Open(filepath.Join(dir, "one.db"))
	if err != nil {
		t.Fatalf("open one: %v", err)
	}
	defer st1.Close()
	st2, err := Open(filepath.Join(dir, "two.db"))
	if err != nil {
		t.Fatalf("open two: %v", err)
	}
	defer st2.Close()
	if err := st1.Migrate(); err != nil {
		t.Fatalf("migrate one: %v", err)
	}
	if err := st2.Migrate(); err != nil {
		t.Fatalf("migrate two: %v", err)
	}

	const readsPath = "internal/svc/reads.go"
	ids := []string{"rd-aaa1", "rd-bbb2", "rd-ccc3"}
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	seed := func(st *Store, id string) {
		t.Helper()
		if err := st.InsertReadEvent(FileReadRecord{
			ReadID: id, RepoName: "meta", FilePath: readsPath,
			AgentName: "agent", ModelName: "model-a", Provider: "prov",
			ToolName: "read_file", LinesReadCount: 10,
			ReadTime: at,
		}); err != nil {
			t.Fatalf("InsertReadEvent %s: %v", id, err)
		}
	}
	for _, id := range []string{"rd-ccc3", "rd-aaa1", "rd-bbb2"} {
		seed(st1, id)
	}
	for _, id := range []string{"rd-bbb2", "rd-ccc3", "rd-aaa1"} {
		seed(st2, id)
	}

	order := func(st *Store) []string {
		t.Helper()
		fs, err := st.GetFileReadStats(readsPath)
		if err != nil {
			t.Fatalf("GetFileReadStats: %v", err)
		}
		out := make([]string, 0, len(fs.RecentReads))
		for _, r := range fs.RecentReads {
			out = append(out, r.ReadID)
		}
		if len(out) != len(ids) {
			t.Fatalf("RecentReads rows = %d, want %d", len(out), len(ids))
		}
		return out
	}

	want := []string{"rd-ccc3", "rd-bbb2", "rd-aaa1"}
	ord1, ord2 := order(st1), order(st2)
	if !reflect.DeepEqual(ord1, ord2) {
		t.Errorf("identical logical data, mirrored insertion order: orders differ\none: %v\ntwo: %v", ord1, ord2)
	}
	if !reflect.DeepEqual(ord1, want) {
		t.Errorf("tie order = %v, want %v (read_id must break same-second ties in the time column's direction)", ord1, want)
	}
}

// TestModelFrictionMatrix_SameSecondTiesAreDeterministic pins the round-97
// fix for the friction report's outer ORDER BY. The LAG windows were
// already deterministic (event_time, event_id — round 25), but the outer
// SELECT ordered by bare event_time, so same-second collisions came back in
// whatever order the query plan produced (observed: the CTE's
// window-materialization order) and WHICH rows survive LIMIT followed that
// plan-dependent order too. event_id now breaks ties DESC, matching the
// report's reverse-chronological direction.
func TestModelFrictionMatrix_SameSecondTiesAreDeterministic(t *testing.T) {
	dir := t.TempDir()
	st1, err := Open(filepath.Join(dir, "one.db"))
	if err != nil {
		t.Fatalf("open one: %v", err)
	}
	defer st1.Close()
	st2, err := Open(filepath.Join(dir, "two.db"))
	if err != nil {
		t.Fatalf("open two: %v", err)
	}
	defer st2.Close()
	if err := st1.Migrate(); err != nil {
		t.Fatalf("migrate one: %v", err)
	}
	if err := st2.Migrate(); err != nil {
		t.Fatalf("migrate two: %v", err)
	}

	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	runs := map[string]string{"run-a": "model-a", "run-b": "model-b"}
	modelOf := func(id string) string {
		if id == "ev-bbb2" {
			return runs["run-b"]
		}
		return runs["run-a"]
	}
	runOf := func(id string) string {
		if id == "ev-bbb2" {
			return "run-b"
		}
		return "run-a"
	}
	seed := func(st *Store, id string) {
		t.Helper()
		if err := st.UpsertRun(RunRecord{
			RunID: runOf(id), AgentName: "agent-" + runOf(id),
			ModelName: modelOf(id), Provider: "prov",
		}); err != nil {
			t.Fatalf("UpsertRun %s: %v", id, err)
		}
		if err := st.InsertEvent(EventRecord{
			EventID: id, RunID: runOf(id), RepoName: "meta",
			FilePath: "internal/svc/fr.go", Signature: "func:fr.go::Handle",
			NodeType: "function", Action: "MODIFIED", BodyHash: "hash", LOC: 5,
			AttributionConfidence: 1.0,
			OccurredAt:            at,
		}); err != nil {
			t.Fatalf("InsertEvent %s: %v", id, err)
		}
	}
	for _, id := range []string{"ev-ccc3", "ev-aaa1", "ev-bbb2"} {
		seed(st1, id)
	}
	for _, id := range []string{"ev-bbb2", "ev-ccc3", "ev-aaa1"} {
		seed(st2, id)
	}

	order := func(st *Store) []string {
		t.Helper()
		rep, err := st.ModelFrictionMatrix(50)
		if err != nil {
			t.Fatalf("ModelFrictionMatrix: %v", err)
		}
		out := make([]string, 0, len(rep.RecentCollisions))
		for _, ev := range rep.RecentCollisions {
			out = append(out, ev.EventID)
		}
		// The partition's first row has no LAG author and is filtered out;
		// exactly two collisions must survive in both stores.
		if len(out) != 2 {
			t.Fatalf("collisions = %d, want 2", len(out))
		}
		return out
	}

	want := []string{"ev-ccc3", "ev-bbb2"} // event_time DESC, event_id DESC
	ord1, ord2 := order(st1), order(st2)
	if !reflect.DeepEqual(ord1, ord2) {
		t.Errorf("identical logical data, mirrored insertion order: orders differ\none: %v\ntwo: %v", ord1, ord2)
	}
	if !reflect.DeepEqual(ord1, want) {
		t.Errorf("tie order = %v, want %v (event_id must break same-second ties in the time column's direction)", ord1, want)
	}
}
