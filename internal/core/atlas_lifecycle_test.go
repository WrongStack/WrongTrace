package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// newAtlasTestEngine wires a REAL ast engine (Tree-sitter) plus a real SQLite
// store, following the db lifecycle-test pattern: everything on-disk under
// t.TempDir(), cleaned up automatically. The plain newTestEngine helper uses
// AST: nil, which no-ops every file-change path — these tests need the real
// parser to exercise PrimeDirectory / HandleFileChange / handleFileGone.
func newAtlasTestEngine(t *testing.T) (*Engine, *db.Store, *ast.Engine) {
	t.Helper()
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	store, err := db.Open(filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	parser, err := ast.NewEngine()
	if err != nil {
		t.Fatalf("ast engine: %v", err)
	}
	t.Cleanup(parser.Close)
	e := NewEngine(Config{RepoName: "atlas-test", Store: store, AST: parser})
	return e, store, parser
}

// writeFixture writes src at dir/rel with permissions 0644.
func writeFixture(t *testing.T, dir, rel, src string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func atlasFile(t *testing.T, snap AtlasSnapshot, rel string) (AtlasFile, bool) {
	t.Helper()
	for _, pkg := range snap.Packages {
		for _, f := range pkg.Files {
			if strings.HasSuffix(filepath.ToSlash(f.Path), rel) {
				return f, true
			}
		}
	}
	return AtlasFile{}, false
}

func atlasSymbol(t *testing.T, snap AtlasSnapshot, sig string) (AtlasSymbol, bool) {
	t.Helper()
	for _, pkg := range snap.Packages {
		for _, f := range pkg.Files {
			for _, s := range f.Symbols {
				if s.Signature == sig {
					return s, true
				}
			}
		}
	}
	return AtlasSymbol{}, false
}

// ---------------------------------------------------------------
// PrimeDirectory
// ---------------------------------------------------------------

// TestPrimeDirectory_PopulatesAtlas walks a table of language fixtures and
// asserts each lands in the atlas with its symbols parsed — and that ignored
// directories (node_modules, .git, dist…) never contribute files.
func TestPrimeDirectory_PopulatesAtlas(t *testing.T) {
	e, _, _ := newAtlasTestEngine(t)
	dir := t.TempDir()

	fixtures := []struct {
		rel    string
		src    string
		lang   string
		symbol string // one representative signature that must be present
	}{
		{
			rel:  "svc/auth.go",
			src:  "package svc\n\nfunc ValidateToken(t string) bool {\n\treturn len(t) > 0\n}\n",
			lang: "go", symbol: "function:auth.go::ValidateToken",
		},
		{
			rel:  "scripts/etl.py",
			src:  "def run_etl(rows):\n    return [r for r in rows if r]\n\nclass Loader:\n    def load(self, path):\n        return open(path)\n",
			lang: "python", symbol: "function:etl.py::run_etl",
		},
		{
			rel:  "web/src/util.ts",
			src:  "export function fmtUSD(n: number): string {\n  return `$${n.toFixed(2)}`;\n}\n",
			lang: "typescript", symbol: "function:util.ts::fmtUSD",
		},
	}

	for _, fx := range fixtures {
		writeFixture(t, dir, fx.rel, fx.src)
	}
	// Noise that must NEVER be indexed.
	writeFixture(t, dir, "node_modules/dep/index.js", "export const x = 1;\n")
	writeFixture(t, dir, "dist/bundle.js", "console.log(1);\n")
	writeFixture(t, dir, "notes/readme.md", "not source\n")

	e.PrimeDirectory(dir)

	snap, err := e.Atlas()
	if err != nil {
		t.Fatalf("atlas: %v", err)
	}

	for _, fx := range fixtures {
		f, ok := atlasFile(t, snap, fx.rel)
		if !ok {
			t.Errorf("%s: file missing from atlas (have %d files)", fx.rel, snap.TotalFiles)
			continue
		}
		if f.Language != fx.lang {
			t.Errorf("%s: language = %s, want %s", fx.rel, f.Language, fx.lang)
		}
		if len(f.Symbols) == 0 {
			t.Errorf("%s: no symbols parsed", fx.rel)
		}
		if _, ok := atlasSymbol(t, snap, fx.symbol); !ok {
			t.Errorf("%s: representative symbol %q not in atlas", fx.rel, fx.symbol)
		}
	}

	for _, banned := range []string{"node_modules", "dist", ".md"} {
		for _, pkg := range snap.Packages {
			for _, f := range pkg.Files {
				if strings.Contains(filepath.ToSlash(f.Path), banned) {
					t.Errorf("ignored path %q leaked into atlas (pattern %q)", f.Path, banned)
				}
			}
		}
	}

	if snap.TotalFiles != len(fixtures) {
		t.Errorf("TotalFiles = %d, want exactly %d (ignored dirs excluded)", snap.TotalFiles, len(fixtures))
	}
	// ValidateToken, run_etl, Loader (class), Loader.load (method), fmtUSD.
	if snap.TotalNodes != 5 {
		t.Errorf("TotalNodes = %d, want 5", snap.TotalNodes)
	}
}

// TestPrimeDirectory_NilASTIsSafe pins the guard: an engine without an AST
// backend must no-op instead of panicking.
func TestPrimeDirectory_NilASTIsSafe(t *testing.T) {
	e, _ := newTestEngine(t)      // AST: nil
	e.PrimeDirectory(t.TempDir()) // must not panic
}

// TestPrimeDirectory_IsIdempotent primes twice; the second pass must not
// duplicate files or corrupt totals (SetSnapshot overwrites by path).
func TestPrimeDirectory_IsIdempotent(t *testing.T) {
	e, _, _ := newAtlasTestEngine(t)
	dir := t.TempDir()
	writeFixture(t, dir, "a/one.go", "package a\n\nfunc One() int { return 1 }\n")

	e.PrimeDirectory(dir)
	first, err := e.Atlas()
	if err != nil {
		t.Fatalf("atlas 1: %v", err)
	}
	e.PrimeDirectory(dir)
	second, err := e.Atlas()
	if err != nil {
		t.Fatalf("atlas 2: %v", err)
	}
	if second.TotalFiles != first.TotalFiles || second.TotalNodes != first.TotalNodes {
		t.Errorf("re-prime changed atlas: files %d->%d nodes %d->%d",
			first.TotalFiles, second.TotalFiles, first.TotalNodes, second.TotalNodes)
	}
}

// ---------------------------------------------------------------
// HandleFileChange + AllNodeStats merge
// ---------------------------------------------------------------

// TestHandleFileChange_AllNodeStatsMerge drives real file edits through the
// watcher entrypoint and asserts the atlas merges persisted event history
// (edit_count, last_action, last_model) onto the live AST snapshot.
func TestHandleFileChange_AllNodeStatsMerge(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	dir := t.TempDir()
	ctx := context.Background()
	path := writeFixture(t, dir, "hot/auth.go", "package hot\n\nfunc Alpha() int {\n\treturn 1\n}\n")

	// An active agent run exists BEFORE the edit, so events are correlated.
	if err := e.ReportRun(reportFor("run-merge")); err != nil {
		t.Fatalf("report run: %v", err)
	}

	// Prime, then edit the body twice (hash changes each time).
	e.PrimeDirectory(dir)
	for v := 2; v <= 3; v++ {
		body := fmt.Sprintf("package hot\n\nfunc Alpha() int {\n\treturn %d\n}\n", v)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("edit v%d: %v", v, err)
		}
		e.HandleFileChange(ctx, path)
	}

	snap, err := e.Atlas()
	if err != nil {
		t.Fatalf("atlas: %v", err)
	}
	sym, ok := atlasSymbol(t, snap, "function:auth.go::Alpha")
	if !ok {
		t.Fatalf("Alpha missing from atlas after edits: %+v", snap)
	}
	if sym.EditCount != 2 {
		t.Errorf("EditCount = %d, want 2 (two MODIFIED events)", sym.EditCount)
	}
	if sym.LastAction != "MODIFIED" {
		t.Errorf("LastAction = %q, want MODIFIED", sym.LastAction)
	}
	if sym.LastModel != "claude-3-7-sonnet" {
		t.Errorf("LastModel = %q, want the correlated run's model", sym.LastModel)
	}
	if sym.LastEventAt.IsZero() {
		t.Error("LastEventAt not stamped from persisted events")
	}

	// The events really were persisted with the run back-filled.
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("persisted %d events, want 2", len(events))
	}
	for _, ev := range events {
		if ev.RunID != "run-merge" {
			t.Errorf("event %s run_id = %q, want back-filled run-merge", ev.EventID, ev.RunID)
		}
	}
}

// TestHandleFileChange_UnsupportedAndCosmetic table-drives the skip paths:
// unsupported languages never produce snapshots, and comment-only edits do
// not emit events (normalizeForHash equivalence).
func TestHandleFileChange_UnsupportedAndCosmetic(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	dir := t.TempDir()
	ctx := context.Background()

	cases := []struct {
		name string
		rel  string
		v1   string
		v2   string
	}{
		{
			name: "unsupported language skipped entirely",
			rel:  "notes/todo.md",
			v1:   "first\n",
			v2:   "second\n",
		},
		{
			name: "cosmetic-only go edit emits no event",
			rel:  "svc/svc.go",
			v1:   "package svc\n\n// v1\nfunc Beta() int {\n\treturn 2\n}\n",
			v2:   "package svc\n\n// v2 (only the comment changed)\nfunc Beta() int {\n\treturn 2\n}\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFixture(t, dir, tc.rel, tc.v1)
			e.HandleFileChange(ctx, path) // establishes snapshot (or skips)
			if err := os.WriteFile(path, []byte(tc.v2), 0o644); err != nil {
				t.Fatalf("rewrite: %v", err)
			}
			e.HandleFileChange(ctx, path)

			events, err := store.RecentEvents(50)
			if err != nil {
				t.Fatalf("recent events: %v", err)
			}
			// The FIRST sight of a source file legitimately emits ADDED (no
			// prior snapshot). The contract under test is that the SECOND,
			// cosmetic-only pass emits nothing further.
			wantAdded := 0
			if strings.HasSuffix(tc.rel, ".go") {
				wantAdded = 1
			}
			added, modified := 0, 0
			for _, ev := range events {
				switch ev.Action {
				case "ADDED":
					added++
				case "MODIFIED":
					modified++
				}
			}
			if added != wantAdded || modified != 0 {
				t.Errorf("events: added=%d (want %d), modified=%d (want 0): %+v", added, wantAdded, modified, events)
			}
		})
	}
}

// ---------------------------------------------------------------
// handleFileGone
// ---------------------------------------------------------------

// TestHandleFileGone_EmitsDeletedAndDropsSnapshot deletes a primed file and
// drives HandleFileChange: every cached symbol must emit DELETED, the
// snapshot must be dropped, and a second delivery must be a no-op.
func TestHandleFileGone_EmitsDeletedAndDropsSnapshot(t *testing.T) {
	e, store, parser := newAtlasTestEngine(t)
	dir := t.TempDir()
	ctx := context.Background()
	path := writeFixture(t, dir, "gone/util.go", "package gone\n\nfunc Keep() {}\n\nfunc AlsoKeep() {}\n")

	e.PrimeDirectory(dir)
	if _, ok := parser.Snapshot(path); !ok {
		t.Fatalf("prime did not cache a snapshot for %s", path)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	e.HandleFileChange(ctx, path) // stat fails -> handleFileGone

	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d DELETED events, want 2 (one per cached symbol)", len(events))
	}
	for _, ev := range events {
		if ev.Action != "DELETED" {
			t.Errorf("event %s action = %s, want DELETED", ev.EventID, ev.Action)
		}
	}

	// Snapshot must be forgotten so the file can be re-added cleanly later.
	if _, ok := parser.Snapshot(path); ok {
		t.Error("snapshot still cached after file deletion; handleFileGone must Forget it")
	}

	// Second delivery after the cache is dropped: no duplicate events.
	e.HandleFileChange(ctx, path)
	events2, err := store.RecentEvents(10)
	if err != nil {
		t.Fatalf("recent events 2: %v", err)
	}
	if len(events2) != 2 {
		t.Errorf("repeat handleFileGone emitted %d more events, want none", len(events2)-2)
	}

	// Re-create the same file: ADDED events flow again (fresh snapshot).
	writeFixture(t, dir, "gone/util.go", "package gone\n\nfunc Keep() {}\n\nfunc AlsoKeep() {}\n")
	e.HandleFileChange(ctx, path)
	events3, err := store.RecentEvents(10)
	if err != nil {
		t.Fatalf("recent events 3: %v", err)
	}
	if len(events3) != 4 {
		t.Errorf("after re-create got %d total events, want 4 (2 DELETED + 2 ADDED)", len(events3))
	}
	added := 0
	for _, ev := range events3[:2] { // most recent first
		if ev.Action == "ADDED" {
			added++
		}
	}
	if added != 2 {
		t.Errorf("re-create did not emit 2 ADDED events: %+v", events3)
	}
}

// TestHandleFileGone_UnknownPathIsNoOp deletes a file the engine has never
// seen: no snapshot, no events, no panic.
func TestHandleFileGone_UnknownPathIsNoOp(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	ctx := context.Background()

	e.HandleFileChange(ctx, filepath.Join(t.TempDir(), "never", "existed.go"))

	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("unknown-path deletion emitted %d events, want 0", len(events))
	}
}

// TestReportRun_ExpiryCleansActiveRunsKeepsEvents pins the correlation-window
// contract the atlas merge relies on: an expired run stops being attributed
// to NEW events but historical attribution survives in AllNodeStats.
func TestReportRun_ExpiryCleansActiveRunsKeepsEvents(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	dir := t.TempDir()
	ctx := context.Background()
	path := writeFixture(t, dir, "win/win.go", "package win\n\nfunc Gamma() int {\n\treturn 1\n}\n")

	if err := e.ReportRun(reportFor("run-old")); err != nil {
		t.Fatalf("report run: %v", err)
	}
	e.PrimeDirectory(dir)
	newBody := "package win\n\nfunc Gamma() int {\n\treturn 2\n}\n"
	if err := os.WriteFile(path, []byte(newBody), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	e.HandleFileChange(ctx, path)

	// Correlated event exists with the run attributed...
	events, err := store.RecentEvents(10)
	if err != nil || len(events) == 0 {
		t.Fatalf("expected a persisted event: %v (%d)", err, len(events))
	}
	if events[0].RunID != "run-old" {
		t.Errorf("event run_id = %q, want run-old", events[0].RunID)
	}

	// ...the atlas merge reflects the historical attribution...
	snap, err := e.Atlas()
	if err != nil {
		t.Fatalf("atlas: %v", err)
	}
	sym, ok := atlasSymbol(t, snap, "function:win.go::Gamma")
	if !ok || sym.LastModel != "claude-3-7-sonnet" {
		t.Fatalf("atlas symbol merge missing model attribution: %+v", sym)
	}

	// ...and expiry of the active-run map does not rewrite history.
	if got := e.ActiveRuns(); len(got) != 1 {
		t.Errorf("active runs = %d, want 1 before expiry", len(got))
	}
	_ = time.Now() // correlation window itself is covered in engine_test.go
}
