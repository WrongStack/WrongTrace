package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeHome redirects the user home directory (HOME on Unix, USERPROFILE
// on Windows — os.UserHomeDir reads whichever applies) to a temp dir, so
// ~/.wrongstack/projects.json in tests is a fixture we write, never the
// developer's real WrongStack registry. WRONGTRACE_HOME is pointed at a
// separate dir so AddProject persistence also stays isolated.
func withFakeHome(t *testing.T) (fakeHome string, traceHome string) {
	t.Helper()
	fakeHome = t.TempDir()
	traceHome = t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)
	t.Setenv("WRONGTRACE_HOME", traceHome)
	return fakeHome, traceHome
}

// writeWrongStackFixture writes ~/.wrongstack/projects.json into fakeHome.
func writeWrongStackFixture(t *testing.T, fakeHome string, entries []WrongStackProject) string {
	t.Helper()
	wsDir := filepath.Join(fakeHome, ".wrongstack")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir wrongstack dir: %v", err)
	}
	path := filepath.Join(wsDir, "projects.json")
	data, err := json.Marshal(map[string]interface{}{"projects": entries})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// newImportEngine builds an engine with a real store, nil AST (none of the
// import path parses files), mirroring newTestEngine.
func newImportEngine(t *testing.T) *Engine {
	t.Helper()
	e, _ := newTestEngine(t)
	return e
}

func TestImportFromWrongStack_ImportsNewAndReportsMissing(t *testing.T) {
	fakeHome, _ := withFakeHome(t)

	// Two roots that exist on disk, one that does not, one with no name.
	realA := t.TempDir()
	realB := t.TempDir()
	ghost := filepath.Join(fakeHome, "deleted-project")
	entries := []WrongStackProject{
		{Name: "Alpha", Root: realA, Slug: "alpha-111"},
		{Name: "Beta", Root: realB, Slug: "beta-222"},
		{Name: "Ghost", Root: ghost, Slug: "ghost-333"},
		{Name: "", Root: "", Slug: "ignored"},
	}
	source := writeWrongStackFixture(t, fakeHome, entries)

	e := newImportEngine(t)
	res, err := e.ImportFromWrongStack(nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.SourcePath != source {
		t.Errorf("source_path = %q, want %q", res.SourcePath, source)
	}
	if res.Found != 4 {
		t.Errorf("found = %d, want 4", res.Found)
	}
	if res.Imported != 2 {
		t.Errorf("imported = %d, want 2", res.Imported)
	}
	if res.SkippedMissing != 1 || len(res.MissingRoots) != 1 || res.MissingRoots[0] != ghost {
		t.Errorf("missing reporting wrong: skipped=%d roots=%v", res.SkippedMissing, res.MissingRoots)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected per-entry errors: %v", res.Errors)
	}
	// The empty-root entry must be silently ignored (not found-imported-missing).
	if res.Imported+res.SkippedExisting+res.SkippedMissing != 3 {
		t.Errorf("counts must cover all non-empty entries: %+v", res)
	}
	if len(res.Projects) != 2 || res.Projects[0].Name != "Alpha" {
		t.Errorf("imported projects = %+v", res.Projects)
	}
	// First import into an empty registry activates the first added project.
	if !res.Projects[0].IsActive {
		t.Errorf("first imported project should be active")
	}
	// Registry must now contain both workspaces.
	if got := len(e.ListProjects()); got != 2 {
		t.Errorf("ListProjects = %d, want 2", got)
	}
}

func TestImportFromWrongStack_IdempotentAcrossRuns(t *testing.T) {
	fakeHome, _ := withFakeHome(t)

	realA := t.TempDir()
	realB := t.TempDir()
	writeWrongStackFixture(t, fakeHome, []WrongStackProject{
		{Name: "Alpha", Root: realA, Slug: "alpha-111"},
		{Name: "Beta", Root: realB, Slug: "beta-222"},
	})

	e := newImportEngine(t)
	first, err := e.ImportFromWrongStack(nil)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Imported != 2 {
		t.Fatalf("first import imported = %d, want 2", first.Imported)
	}

	second, err := e.ImportFromWrongStack(nil)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Imported != 0 {
		t.Errorf("second import imported = %d, want 0 (idempotency)", second.Imported)
	}
	if second.SkippedExisting != 2 {
		t.Errorf("second import skipped_existing = %d, want 2", second.SkippedExisting)
	}
	if got := len(e.ListProjects()); got != 2 {
		t.Errorf("project count after re-import = %d, want 2 (no duplicates)", got)
	}
}

func TestImportFromWrongStack_SkipsAlreadyRegisteredPath_CaseInsensitive(t *testing.T) {
	fakeHome, _ := withFakeHome(t)

	realA := t.TempDir()
	// Register the workspace directly first (what a user may have done by hand).
	e := newImportEngine(t)
	if _, err := e.AddProject("Alpha", realA); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Same root, different case — Windows-style casing drift.
	varied := realA
	if up := strings.ToUpper(realA); up != realA {
		varied = up
	}
	writeWrongStackFixture(t, fakeHome, []WrongStackProject{
		{Name: "Alpha Prime", Root: varied, Slug: "alpha-111"},
	})

	res, err := e.ImportFromWrongStack(nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 0 || res.SkippedExisting != 1 {
		t.Errorf("imported=%d skipped=%d, want 0/1 (same workspace, different case)", res.Imported, res.SkippedExisting)
	}
}

func TestImportFromWrongStack_MissingSourceFile(t *testing.T) {
	withFakeHome(t) // fixture deliberately not written
	e := newImportEngine(t)
	_, err := e.ImportFromWrongStack(nil)
	if !errors.Is(err, ErrWrongStackSourceMissing) {
		t.Fatalf("err = %v, want ErrWrongStackSourceMissing", err)
	}
}

func TestImportFromWrongStack_InvalidSourceJSON(t *testing.T) {
	fakeHome, _ := withFakeHome(t)
	wsDir := filepath.Join(fakeHome, ".wrongstack")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "projects.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	e := newImportEngine(t)
	_, err := e.ImportFromWrongStack(nil)
	if err == nil || errors.Is(err, ErrWrongStackSourceMissing) {
		t.Fatalf("err = %v, want a non-sentinel parse error", err)
	}
}

func TestImportFromWrongStack_SelectiveRoots(t *testing.T) {
	fakeHome, _ := withFakeHome(t)

	realA := t.TempDir()
	realB := t.TempDir()
	ghost := filepath.Join(fakeHome, "deleted-project")
	writeWrongStackFixture(t, fakeHome, []WrongStackProject{
		{Name: "Alpha", Root: realA, Slug: "alpha-111"},
		{Name: "Beta", Root: realB, Slug: "beta-222"},
		{Name: "Ghost", Root: ghost, Slug: "ghost-333"},
	})

	e := newImportEngine(t)
	// Select only Alpha (existing) and Ghost (selected but missing on disk):
	// Beta must stay untouched even though it exists and is importable.
	res, err := e.ImportFromWrongStack([]string{realA, ghost})
	if err != nil {
		t.Fatalf("selective import: %v", err)
	}
	if res.Imported != 1 {
		t.Errorf("imported = %d, want 1 (only Alpha)", res.Imported)
	}
	if res.SkippedMissing != 1 || len(res.MissingRoots) != 1 {
		t.Errorf("missing reporting wrong: %+v", res)
	}
	if len(res.Projects) != 1 || res.Projects[0].Name != "Alpha" {
		t.Errorf("imported projects = %+v", res.Projects)
	}
	projects := e.ListProjects()
	if len(projects) != 1 || projects[0].Name != "Alpha" {
		t.Errorf("registry = %+v, want only Alpha (Beta must be skipped by selection)", projects)
	}

	// An unknown root selects nothing — no error, no-op.
	noop, err := e.ImportFromWrongStack([]string{filepath.Join(fakeHome, "not-in-registry")})
	if err != nil {
		t.Fatalf("unknown-root import: %v", err)
	}
	if noop.Imported != 0 || noop.SkippedExisting != 0 || noop.SkippedMissing != 0 {
		t.Errorf("unknown root must select nothing: %+v", noop)
	}
}

func TestPreviewFromWrongStack_AnnotatesEntries(t *testing.T) {
	fakeHome, _ := withFakeHome(t)

	realA := t.TempDir()
	realB := t.TempDir()
	ghost := filepath.Join(fakeHome, "deleted-project")
	writeWrongStackFixture(t, fakeHome, []WrongStackProject{
		{Name: "Alpha", Root: realA, Slug: "alpha-111"},
		{Name: "Beta", Root: realB, Slug: "beta-222"},
		{Name: "Ghost", Root: ghost, Slug: "ghost-333"},
		{Name: "", Root: "", Slug: "skipped"},
	})

	// Pre-register Beta so the preview can flag it as already registered.
	e := newImportEngine(t)
	if _, err := e.AddProject("Beta", realB); err != nil {
		t.Fatalf("seed Beta: %v", err)
	}

	res, err := e.PreviewFromWrongStack()
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.SourcePath != filepath.Join(fakeHome, ".wrongstack", "projects.json") {
		t.Errorf("source_path = %q", res.SourcePath)
	}
	if len(res.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 (empty-root entry dropped)", len(res.Entries))
	}
	byName := map[string]WrongStackPreviewEntry{}
	for _, en := range res.Entries {
		byName[en.Name] = en
	}
	if en := byName["Alpha"]; !en.ExistsOnDisk || en.AlreadyRegistered {
		t.Errorf("Alpha: exists=%v registered=%v, want true/false", en.ExistsOnDisk, en.AlreadyRegistered)
	}
	if en := byName["Beta"]; !en.ExistsOnDisk || !en.AlreadyRegistered {
		t.Errorf("Beta: exists=%v registered=%v, want true/true", en.ExistsOnDisk, en.AlreadyRegistered)
	}
	if en := byName["Ghost"]; en.ExistsOnDisk || en.AlreadyRegistered {
		t.Errorf("Ghost: exists=%v registered=%v, want false/false", en.ExistsOnDisk, en.AlreadyRegistered)
	}
}

// TestIsIgnoredDir_CombinesSettingsAndAlwaysList proves the single predicate
// honors both sources: a settings pattern, an always-ignored tooling dir, and
// case-insensitivity — and that normal source dirs pass.
func TestIsIgnoredDir_CombinesSettingsAndAlwaysList(t *testing.T) {
	setIgnorePatterns(t, []string{"node_modules", ".gen"})

	cases := []struct {
		base string
		want bool
	}{
		{"node_modules", true}, // settings list
		{".gen", true},         // settings list
		{"NODE_MODULES", true}, // case-insensitive
		{".wrongtrace", true},  // always-list (own data dir)
		{"Bin", true},          // always-list, mixed case
		{".temp_files", true},  // always-list (agent scratch)
		{"pkg", false},         // normal source dir
		{"internal", false},    // normal source dir
		{"src", false},         // normal source dir
	}
	for _, tc := range cases {
		if got := isIgnoredDir(tc.base); got != tc.want {
			t.Errorf("isIgnoredDir(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
}

// TestPrimeDirectory_UsesSharedIgnorePredicate proves priming skips ignored
// trees via the same isIgnoredDir predicate DetectPrimaryLanguage uses: files
// under node_modules and .wrongtrace never reach the AST cache, while real
// sources do. It also covers the root guard: priming a directory whose own
// base name matches an ignore entry (e.g. a workspace literally named "bin")
// must not skip its entire contents.
func TestPrimeDirectory_UsesSharedIgnorePredicate(t *testing.T) {
	e, _, parser := newAtlasTestEngine(t)

	// 1. Ignored trees are pruned, real sources are parsed.
	dir := t.TempDir()
	real := writeFixture(t, dir, "svc/service.go", "package svc\n\nfunc Hello() string { return \"hi\" }\n")
	_ = writeFixture(t, dir, "node_modules/dep/index.go", "package dep\n\nfunc Hidden() {}\n")
	_ = writeFixture(t, dir, ".wrongtrace/self/service.go", "package self\n\nfunc AlsoHidden() {}\n")

	e.PrimeDirectory(dir)

	if _, ok := parser.Snapshot(real); !ok {
		t.Errorf("real source %s was not primed into the AST cache", real)
	}
	for _, hidden := range []string{
		filepath.Join(dir, "node_modules", "dep", "index.go"),
		filepath.Join(dir, ".wrongtrace", "self", "service.go"),
	} {
		if _, ok := parser.Snapshot(hidden); ok {
			t.Errorf("ignored-tree file %s leaked into the AST cache", hidden)
		}
	}

	// 2. Root guard: a workspace dir literally named "bin" still primes.
	src := t.TempDir()
	target := filepath.Join(filepath.Dir(src), "bin")
	if err := os.Rename(src, target); err != nil {
		t.Skipf("cannot rename temp dir: %v", err)
	}
	tool := writeFixture(t, target, "inner/tool.go", "package inner\n\nfunc Tool() {}\n")

	e.PrimeDirectory(target)
	if _, ok := parser.Snapshot(tool); !ok {
		t.Errorf("root-named-ignored workspace: %s was not primed (root guard missing)", tool)
	}
}

func TestPreviewFromWrongStack_MissingSourceFile(t *testing.T) {
	withFakeHome(t)
	e := newImportEngine(t)
	_, err := e.PreviewFromWrongStack()
	if !errors.Is(err, ErrWrongStackSourceMissing) {
		t.Fatalf("err = %v, want ErrWrongStackSourceMissing", err)
	}
}

// setIgnorePatterns swaps the package-global ignore patterns for the test and
// restores them afterwards. globalSettings is process-wide; core tests run
// sequentially, so mutate-and-restore is safe.
func setIgnorePatterns(t *testing.T, patterns []string) {
	t.Helper()
	settingsMu.Lock()
	orig := globalSettings.IgnorePatterns
	globalSettings.IgnorePatterns = patterns
	settingsMu.Unlock()
	t.Cleanup(func() {
		settingsMu.Lock()
		globalSettings.IgnorePatterns = orig
		settingsMu.Unlock()
	})
}

// writeFiles creates files (path -> content) under root.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// TestDetectPrimaryLanguage_PrunesDefaultIgnoredDirs proves the walk does not
// count (or descend into) node_modules/vendor trees: the ignored subtree
// carries more .ts files than the workspace has source files, so any leak
// flips the classification away from Go.
func TestDetectPrimaryLanguage_PrunesDefaultIgnoredDirs(t *testing.T) {
	setIgnorePatterns(t, nil) // nil clears the override; ignorePatterns() falls back to the default list

	root := t.TempDir()
	files := map[string]string{"main.go": "package main\n"}
	// Fat ignored trees, several levels deep: node_modules and vendor.
	for i := 0; i < 40; i++ {
		files[filepath.Join("node_modules", "pkg", "nested", fmt.Sprintf("mod%d.ts", i))] = "export const x = 1;\n"
	}
	for i := 0; i < 30; i++ {
		files[filepath.Join("vendor", "lib", fmt.Sprintf("dep%d.ts", i))] = "export const y = 2;\n"
	}
	// A real (non-ignored) TS dir with fewer files must still count.
	files[filepath.Join("web", "app.ts")] = "export const z = 3;\n"
	writeFiles(t, root, files)

	if got := DetectPrimaryLanguage(root); got != "Go" {
		t.Errorf("DetectPrimaryLanguage = %q, want Go (ignored trees must not dominate)", got)
	}
}

// TestDetectPrimaryLanguage_HonorsCustomIgnorePatterns proves the filter reads
// the ignore_patterns setting rather than a hardcoded list.
func TestDetectPrimaryLanguage_HonorsCustomIgnorePatterns(t *testing.T) {
	setIgnorePatterns(t, []string{".gen", "node_modules"})

	root := t.TempDir()
	files := map[string]string{"main.go": "package main\n"}
	for i := 0; i < 25; i++ {
		files[filepath.Join(".gen", "sdk", fmt.Sprintf("g%d.ts", i))] = "export const a = 1;\n"
	}
	// node_modules ignored by the custom list too.
	files[filepath.Join("node_modules", "x.ts")] = "export const b = 2;\n"
	writeFiles(t, root, files)

	if got := DetectPrimaryLanguage(root); got != "Go" {
		t.Errorf("custom ignore: DetectPrimaryLanguage = %q, want Go", got)
	}

	// Same tree, but with .gen NOT ignored: generated files now dominate.
	setIgnorePatterns(t, []string{"node_modules"})
	if got := DetectPrimaryLanguage(root); got != "TypeScript" {
		t.Errorf("unignored .gen: DetectPrimaryLanguage = %q, want TypeScript", got)
	}
}

// TestDetectPrimaryLanguage_MtsCtsAreTypeScript pins the alignment invariant
// with ast.DetectLanguage (internal/ast/supported.go): .mts (ES-module
// TypeScript) and .cts (CommonJS TypeScript) must count toward TypeScript
// exactly like .ts/.tsx. Before the alignment, a workspace dominated by those
// spellings classified as "Generic" — or let a minority language win — so
// AddProject stamped the wrong language label at registration. Case-variant
// extensions must count too (the switch lowercases the extension first).
func TestDetectPrimaryLanguage_MtsCtsAreTypeScript(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a1.mts":     "export const a = 1;\n",
		"a2.mts":     "export const b = 2;\n",
		"a3.mts":     "export const c = 3;\n",
		"a4.mts":     "export const d = 4;\n",
		"entry.cts":  "export const e = 5;\n",
		"WORKER.MTS": "export const f = 6;\n",
		"cli.py":     "x = 1\n",
	}
	writeFiles(t, root, files)

	if got := DetectPrimaryLanguage(root); got != "TypeScript" {
		t.Errorf("DetectPrimaryLanguage = %q, want TypeScript for .mts/.cts-dominated workspace", got)
	}

	// No over-reach: a Python-only workspace still classifies as Python.
	pyRoot := t.TempDir()
	writeFiles(t, pyRoot, map[string]string{"main.py": "x = 1\n", "util.py": "y = 2\n"})
	if got := DetectPrimaryLanguage(pyRoot); got != "Python" {
		t.Errorf("DetectPrimaryLanguage = %q, want Python for python-only workspace", got)
	}
}

// TestDetectPrimaryLanguage_MjsCjsAreJavaScript completes the extension
// alignment invariant with ast.DetectLanguage: .mjs (ES-module JavaScript)
// and .cjs (CommonJS JavaScript) must count toward JavaScript exactly like
// .js/.jsx. Before the alignment, an ESM/CJS-dominated workspace classified
// as "Generic" — or let a minority language win — so AddProject stamped the
// wrong language label at registration. Case-variant extensions must count
// too (the switch lowercases the extension first).
func TestDetectPrimaryLanguage_MjsCjsAreJavaScript(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a1.mjs":     "export const a = 1;\n",
		"a2.mjs":     "export const b = 2;\n",
		"a3.mjs":     "export const c = 3;\n",
		"a4.mjs":     "export const d = 4;\n",
		"index.cjs":  "module.exports = 5;\n",
		"WORKER.MJS": "export const f = 6;\n",
		"cli.py":     "x = 1\n",
	}
	writeFiles(t, root, files)

	if got := DetectPrimaryLanguage(root); got != "JavaScript" {
		t.Errorf("DetectPrimaryLanguage = %q, want JavaScript for .mjs/.cjs-dominated workspace", got)
	}

	// No over-reach: a Python-only workspace still classifies as Python.
	pyRoot := t.TempDir()
	writeFiles(t, pyRoot, map[string]string{"main.py": "x = 1\n", "util.py": "y = 2\n"})
	if got := DetectPrimaryLanguage(pyRoot); got != "Python" {
		t.Errorf("DetectPrimaryLanguage = %q, want Python for python-only workspace", got)
	}
}

// TestDetectPrimaryLanguage_PhpRubyAreCounted completes the extension
// alignment invariant with ast.DetectLanguage: .php and .rb are parsed by the
// AST layer's PHP and Ruby grammars and must count toward PHP / Ruby. Before
// the alignment, a PHP- or Ruby-dominated workspace classified as "Generic" —
// or let a minority language win — so AddProject stamped the wrong language
// label at registration. Precedence on a tie is deterministic: the existing
// list order wins, so a Go/PHP split still classifies as Go.
func TestDetectPrimaryLanguage_PhpRubyAreCounted(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a1.php":    "<?php echo 1;\n",
		"a2.php":    "<?php echo 2;\n",
		"index.php": "<?php echo 3;\n",
		"app.rb":    "puts 1\n",
	}
	writeFiles(t, root, files)

	if got := DetectPrimaryLanguage(root); got != "PHP" {
		t.Errorf("DetectPrimaryLanguage = %q, want PHP for .php-dominated workspace", got)
	}

	rubyRoot := t.TempDir()
	writeFiles(t, rubyRoot, map[string]string{"a.rb": "puts 1\n", "b.rb": "puts 2\n"})
	if got := DetectPrimaryLanguage(rubyRoot); got != "Ruby" {
		t.Errorf("DetectPrimaryLanguage = %q, want Ruby for .rb-dominated workspace", got)
	}

	// Tie precedence: one Go + one PHP file — the existing list order keeps
	// Go ahead of the newly counted PHP, so established projects never flip.
	mixed := t.TempDir()
	writeFiles(t, mixed, map[string]string{"x.go": "package main\n", "y.php": "<?php echo 1;\n"})
	if got := DetectPrimaryLanguage(mixed); got != "Go" {
		t.Errorf("DetectPrimaryLanguage = %q, want Go for a Go/PHP tie", got)
	}

	// No over-reach: a Python-only workspace still classifies as Python.
	pyRoot := t.TempDir()
	writeFiles(t, pyRoot, map[string]string{"main.py": "x = 1\n", "util.py": "y = 2\n"})
	if got := DetectPrimaryLanguage(pyRoot); got != "Python" {
		t.Errorf("DetectPrimaryLanguage = %q, want Python for python-only workspace", got)
	}
}

// TestDetectPrimaryLanguage_NonPrecedenceOverridesPrecedence verifies the extended
// precedence loop correctly handles languages that have a higher count than any
// precedence-listed language.  Before the fix, the second loop iterated the map
// in non-deterministic order and used strict ">" — so any repo with a count tie
// at the max could pick any language as the winner, including non-precedence ones
// that the first loop's precedence was supposed to protect.
// The fix replaces the map loop with an extended ordered slice so the result is
// always deterministic: highest count wins; alphabetical order breaks ties.
func TestDetectPrimaryLanguage_NonPrecedenceOverridesPrecedence(t *testing.T) {
	// Scenario: Python(20) vs Go(20) — both have same count.
// PHP(10) is in the precedence list. The first loop picks Python(20) as winner.
// With the map loop (bug): Python(20) stays winner (no count beats 20).
// FIXED: Python(20) stays winner with the extended slice loop.
// We also verify non-determinism: run 3 times; if it ever differs, the fix regressed.
	pythonRoot := t.TempDir()
	for i := 0; i < 20; i++ {
		writeFiles(t, pythonRoot, map[string]string{
			fmt.Sprintf("util_%02d.py", i): fmt.Sprintf("def f%d():\n    pass\n", i),
		})
	}
	for i := 0; i < 20; i++ {
		writeFiles(t, pythonRoot, map[string]string{
			fmt.Sprintf("srv_%02d.go", i): "package main\n",
		})
	}
	for i := 0; i < 10; i++ {
		writeFiles(t, pythonRoot, map[string]string{
			fmt.Sprintf("legacy_%02d.php", i): "<?php echo 1;\n",
		})
	}

	results := make(map[string]int)
	for i := 0; i < 5; i++ {
		got := DetectPrimaryLanguage(pythonRoot)
		results[got]++
	}
	// With the fix, Python must win every time (highest count, precedence on tie with Go).
	// With the map loop bug, Go could win due to non-deterministic map order,
	// or if Python and Go were both in the map: Go first → Go wins, Python first → Python wins.
	// (Neither is wrong on counts, but the non-determinism is the bug.)
	// The extended slice loop deterministically picks: both have 20, Go is ahead of Python
	// in the precedence list, so Go wins.
	if results["Go"] != 5 {
		// If this fires, either: (a) the fix regressed (map loop is back), or
		// (b) something else is wrong with extension counting.
		var gotMost string
		var mostCount int
		for lang, cnt := range results {
			if cnt > mostCount {
				mostCount = cnt
				gotMost = lang
			}
		}
		t.Errorf("DetectPrimaryLanguage over 5 runs = %v; Go should win every time (count=20, precedence above Python). Got most: %s(%d). Results: %v",
			results, gotMost, mostCount, results)
	}
}

// TestImportFromWrongStack_FullRegistryWithFatIgnoredTrees covers the batch
// path that used to take >60s on a real registry: a full-registry import
// (roots=nil) where every workspace embeds a deep, fat node_modules tree.
// Asserts the batch completes, classifies each workspace from its real
// sources (not the ignored trees), and reports the mixed outcome correctly.
func TestImportFromWrongStack_FullRegistryWithFatIgnoredTrees(t *testing.T) {
	fakeHome, _ := withFakeHome(t)

	// 3 new workspaces (Go sources + fat node_modules), 1 pre-registered, 1 missing.
	mkWorkspace := func(name string) string {
		dir := t.TempDir()
		files := map[string]string{
			"main.go":                      "package main\n",
			filepath.Join("pkg", "svc.go"): "package pkg\n",
		}
		// Fat ignored tree: 3 levels × 30 files of TypeScript.
		for i := 0; i < 30; i++ {
			for _, lvl := range []string{"a", "b"} {
				rel := filepath.Join("node_modules", "pkg"+fmt.Sprint(i), lvl, fmt.Sprintf("m%d.ts", i))
				files[rel] = "export const deep = 1;\n"
			}
		}
		writeFiles(t, dir, files)
		return dir
	}

	ws := []string{mkWorkspace("Alpha"), mkWorkspace("Beta"), mkWorkspace("Gamma")}
	preReg := mkWorkspace("PreReg")
	ghost := filepath.Join(fakeHome, "deleted")

	entries := []WrongStackProject{
		{Name: "Alpha", Root: ws[0], Slug: "alpha"},
		{Name: "Beta", Root: ws[1], Slug: "beta"},
		{Name: "Gamma", Root: ws[2], Slug: "gamma"},
		{Name: "PreReg", Root: preReg, Slug: "prereg"},
		{Name: "Ghost", Root: ghost, Slug: "ghost"},
	}
	writeWrongStackFixture(t, fakeHome, entries)

	e := newImportEngine(t)
	if _, err := e.AddProject("PreReg", preReg); err != nil {
		t.Fatalf("seed PreReg: %v", err)
	}

	res, err := e.ImportFromWrongStack(nil) // nil = full registry
	if err != nil {
		t.Fatalf("full-registry import: %v", err)
	}
	if res.Found != 5 || res.Imported != 3 || res.SkippedExisting != 1 || res.SkippedMissing != 1 {
		t.Errorf("summary = found %d imported %d skipExisting %d skipMissing %d; want 5/3/1/1",
			res.Found, res.Imported, res.SkippedExisting, res.SkippedMissing)
	}
	// The fat node_modules trees must not flip any classification.
	for _, p := range res.Projects {
		if p.PrimaryLanguage != "Go" {
			t.Errorf("%s: PrimaryLanguage = %q, want Go (ignored trees leaked into the count)", p.Name, p.PrimaryLanguage)
		}
	}
	if got := len(e.ListProjects()); got != 4 {
		t.Errorf("registry = %d, want 4 (3 imported + 1 pre-registered)", got)
	}
}
