package core

import (
	"encoding/json"
	"errors"
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

func TestPreviewFromWrongStack_MissingSourceFile(t *testing.T) {
	withFakeHome(t)
	e := newImportEngine(t)
	_, err := e.PreviewFromWrongStack()
	if !errors.Is(err, ErrWrongStackSourceMissing) {
		t.Fatalf("err = %v, want ErrWrongStackSourceMissing", err)
	}
}
