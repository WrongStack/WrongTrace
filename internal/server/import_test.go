package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/core"
)

// withFakeWrongStackHome isolates both home dirs the import touches:
// HOME/USERPROFILE (where ~/.wrongstack/projects.json is read) and
// WRONGTRACE_HOME (where WrongTrace persists projects.json + per-project
// DBs). Must run before newTestServer: engine construction loads the
// projects index from WRONGTRACE_HOME.
func withFakeWrongStackHome(t *testing.T) string {
	t.Helper()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)
	withIsolatedProjectsHome(t)
	return fakeHome
}

// writeWrongStackRegistry writes a projects.json fixture into fakeHome.
func writeWrongStackRegistry(t *testing.T, fakeHome string, raw string) {
	t.Helper()
	wsDir := filepath.Join(fakeHome, ".wrongstack")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir wrongstack: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "projects.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write wrongstack projects.json: %v", err)
	}
}

// postImport fires POST /api/projects/import/wrongstack and decodes the body.
func postImport(t *testing.T, ts *httptest.Server, body interface{}, out interface{}) *http.Response {
	t.Helper()
	return projReq(t, ts, http.MethodPost, "/api/projects/import/wrongstack", body, out)
}

// TestPreviewFromWrongStack_ListsEligibleWorkspaces covers the GET preview:
// every registry entry with its already-registered / on-disk annotation, so
// the dashboard can render the pick-list before committing to an import.
func TestPreviewFromWrongStack_ListsEligibleWorkspaces(t *testing.T) {
	fakeHome := withFakeWrongStackHome(t)
	dirA := t.TempDir()
	writeWrongStackRegistry(t, fakeHome, `{"projects":[
		{"name":"Alpha","root":`+quoteJSON(dirA)+`,"slug":"alpha-111"},
		{"name":"Ghost","root":`+quoteJSON(filepath.Join(fakeHome, "gone"))+`,"slug":"ghost-999"}
	]}`)
	_, _, ts := newTestServer(t)

	// Pre-register Alpha through the API so the preview flags it.
	var created core.ProjectProfile
	if resp := projReq(t, ts, http.MethodPost, "/api/projects", map[string]string{"name": "Alpha", "path": dirA}, &created); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed project: status %d", resp.StatusCode)
	}

	var preview core.PreviewFromWrongStackResult
	resp := projReq(t, ts, http.MethodGet, "/api/projects/import/wrongstack", nil, &preview)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, want 200", resp.StatusCode)
	}
	if len(preview.Entries) != 2 {
		t.Fatalf("preview entries = %d, want 2", len(preview.Entries))
	}
	for _, en := range preview.Entries {
		switch en.Name {
		case "Alpha":
			if !en.AlreadyRegistered || !en.ExistsOnDisk {
				t.Errorf("Alpha: registered=%v on_disk=%v, want true/true", en.AlreadyRegistered, en.ExistsOnDisk)
			}
		case "Ghost":
			if en.AlreadyRegistered || en.ExistsOnDisk {
				t.Errorf("Ghost: registered=%v on_disk=%v, want false/false", en.AlreadyRegistered, en.ExistsOnDisk)
			}
		}
	}
}

// TestImportFromWrongStack_SelectiveRoots covers the choose-what-to-import
// flow: only the roots listed in {"roots":[...]} are imported; the rest of
// the registry stays untouched.
func TestImportFromWrongStack_SelectiveRoots(t *testing.T) {
	fakeHome := withFakeWrongStackHome(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeWrongStackRegistry(t, fakeHome, `{"projects":[
		{"name":"Alpha","root":`+quoteJSON(dirA)+`,"slug":"alpha-111"},
		{"name":"Beta","root":`+quoteJSON(dirB)+`,"slug":"beta-222"}
	]}`)
	_, _, ts := newTestServer(t)

	var res core.ImportFromWrongStackResult
	resp := postImport(t, ts, map[string]interface{}{"roots": []string{dirA}}, &res)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("selective import status = %d, want 200", resp.StatusCode)
	}
	if res.Imported != 1 {
		t.Fatalf("imported = %d, want 1 (only Alpha)", res.Imported)
	}
	if len(res.Projects) != 1 || res.Projects[0].Name != "Alpha" {
		t.Fatalf("imported projects = %+v, want only Alpha", res.Projects)
	}
	var listed []core.ProjectProfile
	if resp := projReq(t, ts, http.MethodGet, "/api/projects", nil, &listed); resp.StatusCode != http.StatusOK {
		t.Fatalf("list projects: %d", resp.StatusCode)
	}
	if len(listed) != 1 || listed[0].Name != "Alpha" {
		t.Fatalf("registry = %+v, want only Alpha (Beta must be skipped)", listed)
	}
}

// TestImportFromWrongStack_HappyPathImportedAndIdempotent covers the route
// end-to-end: fixture import registers new workspaces, a second call reports
// them as already-registered, and /api/projects reflects the new entries.
func TestImportFromWrongStack_HappyPathImportedAndIdempotent(t *testing.T) {
	fakeHome := withFakeWrongStackHome(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeWrongStackRegistry(t, fakeHome, `{"projects":[
		{"name":"Alpha","root":`+quoteJSON(dirA)+`,"slug":"alpha-111","projectId":"proj_1"},
		{"name":"Beta","root":`+quoteJSON(dirB)+`,"slug":"beta-222","lastSeen":"2026-08-22T18:00:00Z"}
	]}`)
	_, _, ts := newTestServer(t)

	var first core.ImportFromWrongStackResult
	resp := postImport(t, ts, map[string]string{}, &first)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first import status = %d, want 200", resp.StatusCode)
	}
	if first.Imported != 2 {
		t.Fatalf("first import imported = %d, want 2", first.Imported)
	}
	if len(first.Projects) != 2 {
		t.Fatalf("first import projects = %d, want 2", len(first.Projects))
	}

	// The registry endpoint must now list the imported workspaces.
	var listed []core.ProjectProfile
	if resp := projReq(t, ts, http.MethodGet, "/api/projects", nil, &listed); resp.StatusCode != http.StatusOK {
		t.Fatalf("list projects: %d", resp.StatusCode)
	}
	if len(listed) != 2 {
		t.Fatalf("GET /api/projects = %d entries, want 2", len(listed))
	}

	var second core.ImportFromWrongStackResult
	resp = postImport(t, ts, map[string]string{}, &second)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second import status = %d, want 200", resp.StatusCode)
	}
	if second.Imported != 0 || second.SkippedExisting != 2 {
		t.Fatalf("second import imported=%d skipped=%d, want 0/2 (idempotent)", second.Imported, second.SkippedExisting)
	}
}

// TestImportFromWrongStack_MissingSourceIs404 verifies the sentinel mapping:
// when ~/.wrongstack/projects.json does not exist the endpoint answers 404
// with a structured error instead of an empty success.
func TestImportFromWrongStack_MissingSourceIs404(t *testing.T) {
	withFakeWrongStackHome(t) // no fixture written
	_, _, ts := newTestServer(t)

	var body map[string]interface{}
	resp := postImport(t, ts, map[string]string{}, &body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if body["error"] == nil {
		t.Fatalf("error field missing from body: %v", body)
	}
}

// TestImportFromWrongStack_InvalidSourceIs422 verifies malformed JSON maps
// to 422 rather than being silently treated as an empty registry.
func TestImportFromWrongStack_InvalidSourceIs422(t *testing.T) {
	fakeHome := withFakeWrongStackHome(t)
	writeWrongStackRegistry(t, fakeHome, `not json`)
	_, _, ts := newTestServer(t)

	var body map[string]interface{}
	resp := postImport(t, ts, map[string]string{}, &body)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if body["error"] == nil {
		t.Fatalf("error field missing from body: %v", body)
	}
}

// quoteJSON marshals s into a JSON string literal (handles backslashes in
// Windows paths, which raw string concatenation would leave unescaped).
func quoteJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
