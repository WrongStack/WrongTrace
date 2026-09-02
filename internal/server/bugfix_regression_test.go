package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// withIsolatedProjectsHome points WRONGTRACE_HOME at a temp dir so project
// persistence (projects.json + per-project DBs) never touches the real
// ~/.wrongtrace during tests.
func withIsolatedProjectsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", home)
	return home
}

// projReq issues a JSON request against ts and decodes the response body
// into out (when non-nil).
func projReq(t *testing.T, ts *httptest.Server, method, url string, body interface{}, out interface{}) *http.Response {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+url, rdr)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s: %v", method, url, err)
		}
	}
	return resp
}

// TestUpdateProject_URLParamIsAuthoritative is the regression test for the
// bug where UpdateProject demanded id in the JSON body while its only caller
// (SettingsView handleUpdateProjectFields) PUTs to /api/projects/{id} with a
// body of just name/description/*_logs_path — so every project edit failed
// with 400 "project id is required" and the route param was never read.
func TestUpdateProject_URLParamIsAuthoritative(t *testing.T) {
	withIsolatedProjectsHome(t)
	_, _, ts := newTestServer(t)

	dir := t.TempDir()
	var created core.ProjectProfile
	resp := projReq(t, ts, "POST", "/api/projects", map[string]string{"name": "alpha", "path": dir}, &created)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create project: status %d", resp.StatusCode)
	}
	if created.ID == "" {
		t.Fatal("created project has no id")
	}

	// The exact shape the dashboard sends: no id in the body.
	var updated core.ProjectProfile
	resp = projReq(t, ts, "PUT", "/api/projects/"+created.ID, map[string]string{
		"name":        "renamed",
		"description": "updated description",
	}, &updated)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit without body id: status %d (want 200 — this was the 400 bug)", resp.StatusCode)
	}
	if updated.Name != "renamed" {
		t.Errorf("name = %q, want renamed", updated.Name)
	}
	if updated.Description != "updated description" {
		t.Errorf("description = %q, want updated", updated.Description)
	}

	// Body id that disagrees with the URL is a conflict, not a silent edit.
	resp = projReq(t, ts, "PUT", "/api/projects/"+created.ID, map[string]string{
		"id":   "proj-different",
		"name": "hijack",
	}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("body/URL id mismatch: status %d, want 409", resp.StatusCode)
	}

	// Unknown project id is 404, consistent with GetProject/RemoveProject.
	resp = projReq(t, ts, "PUT", "/api/projects/proj-nosuch", map[string]string{"name": "x"}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project: status %d, want 404", resp.StatusCode)
	}
}

// TestClearStale_BodyDaysAndValidation is the regression test for the
// Storage-tab prune path: the dashboard POSTs {"days":30} as a JSON body,
// but the handler only read the ?days= query param (so the body was silently
// ignored) and rewrote invalid values to 30 instead of rejecting them.
func TestClearStale_BodyDaysAndValidation(t *testing.T) {
	_, store, ts := newTestServer(t)

	// Seed one stale event (old timestamp).
	old := time.Now().UTC().AddDate(0, 0, -90)
	if err := store.InsertEvent(db.EventRecord{
		EventID: "stale-1", RepoName: "t", FilePath: "a.go",
		Signature: "function:a.go::Old", NodeType: "function", Action: "ADDED",
		BodyHash: "h1", LOC: 1, OccurredAt: old,
	}); err != nil {
		t.Fatalf("seed stale: %v", err)
	}

	// Body form (what the dashboard sends) is honored.
	var res map[string]interface{}
	resp := projReq(t, ts, "POST", "/api/settings/clear-stale", map[string]int{"days": 30}, &res)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("body days: status %d, want 200", resp.StatusCode)
	}
	if got := res["days"].(float64); got != 30 {
		t.Errorf("days = %v, want 30", got)
	}
	if del := res["deleted"].(float64); del != 1 {
		t.Errorf("deleted = %v, want 1 (the seeded stale event)", del)
	}

	// Query form (documented REST) still works.
	resp = projReq(t, ts, "POST", "/api/settings/clear-stale?days=7", nil, &res)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("query days: status %d, want 200", resp.StatusCode)
	}

	// Invalid values are rejected, not silently rewritten to 30.
	resp = projReq(t, ts, "POST", "/api/settings/clear-stale?days=abc", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("days=abc: status %d, want 400", resp.StatusCode)
	}
	resp = projReq(t, ts, "POST", "/api/settings/clear-stale?days=-5", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("days=-5: status %d, want 400", resp.StatusCode)
	}
	resp = projReq(t, ts, "POST", "/api/settings/clear-stale", map[string]int{"days": 0}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("body days=0: status %d, want 400", resp.StatusCode)
	}
}

// TestUpdateSettings_PartialPostKeepsWebhooks is the regression test for the
// settings wipe: UpdateSettings assigned the three webhook URLs
// unconditionally, so any partial POST (e.g. just debounce_ms) zeroed
// configured integrations.
func TestUpdateSettings_PartialPostKeepsWebhooks(t *testing.T) {
	_, _, ts := newTestServer(t)

	// Configure a webhook.
	var after core.AppSettings
	resp := projReq(t, ts, "POST", "/api/settings", map[string]interface{}{
		"slack_webhook_url": "https://hooks.example/slack/xxx",
	}, &after)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set webhook: status %d", resp.StatusCode)
	}
	if after.SlackWebhookURL == "" {
		t.Fatal("webhook not stored by full-set POST")
	}

	// Partial update must not clear it.
	resp = projReq(t, ts, "POST", "/api/settings", map[string]interface{}{
		"debounce_ms": 500,
	}, &after)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("partial post: status %d", resp.StatusCode)
	}
	if after.SlackWebhookURL != "https://hooks.example/slack/xxx" {
		t.Errorf("slack webhook = %q after partial post, want preserved", after.SlackWebhookURL)
	}
	if after.DebounceMs != 500 {
		t.Errorf("debounce_ms = %d, want 500", after.DebounceMs)
	}
}

// atlasPrefixFakeEngine serves one crafted AtlasSnapshot so the handler's
// ?prefix= filter can be pinned without priming a real workspace.
type atlasPrefixFakeEngine struct {
	failingEngine
	snap core.AtlasSnapshot
}

func (e *atlasPrefixFakeEngine) Atlas(...string) (core.AtlasSnapshot, error) {
	return e.snap, nil
}

// TestAtlasHandler_PrefixFilterIsBoundaryCorrect pins the handler-level half
// of the sibling-prefix rule: ?prefix=api must keep the api package and
// everything under it (api/nested), and must drop the sibling api-v2 even
// though "api-v2" starts with the string "api". The filter used a bare
// strings.HasPrefix, so the sibling package and its files bled into every
// prefix query — the same leak fixed inside core.Atlas with pathIsWithin
// (TestAtlas_SiblingPrefixProjectIsolation).
func TestAtlasHandler_PrefixFilterIsBoundaryCorrect(t *testing.T) {
	snap := core.AtlasSnapshot{
		Repo: "ws",
		Packages: []core.AtlasPackage{
			{Path: "api", Name: "api", Files: []core.AtlasFile{{Path: "api/a.go", Name: "a.go"}}},
			{Path: "api-v2", Name: "apiv2", Files: []core.AtlasFile{{Path: "api-v2/b.go", Name: "b.go"}}},
			{Path: "api/nested", Name: "nested", Files: []core.AtlasFile{{Path: "api/nested/c.go", Name: "c.go"}}},
		},
	}
	h := &Handlers{Engine: &atlasPrefixFakeEngine{snap: snap}}

	// prefix=api: api and api/nested stay, api-v2 is dropped whole.
	rec := httptest.NewRecorder()
	h.Atlas(rec, httptest.NewRequest(http.MethodGet, "/api/atlas?prefix=api", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("prefix=api: status %d", rec.Code)
	}
	var got core.AtlasSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TotalPackages != 2 {
		t.Fatalf("prefix=api: TotalPackages=%d, want 2 — the api-v2 sibling leaked", got.TotalPackages)
	}
	seen := map[string]int{}
	for _, p := range got.Packages {
		seen[p.Path]++
		if p.Path == "api-v2" {
			t.Errorf("prefix=api: leaked sibling package api-v2")
		}
		for _, f := range p.Files {
			if filepath.Base(f.Path) == "b.go" {
				t.Errorf("prefix=api: leaked sibling file %q", f.Path)
			}
		}
	}
	if seen["api"] != 1 || seen["api/nested"] != 1 {
		t.Errorf("prefix=api: kept packages %v, want api and api/nested", seen)
	}

	// prefix=api-v2: only the v2 sibling, nothing from api.
	rec = httptest.NewRecorder()
	h.Atlas(rec, httptest.NewRequest(http.MethodGet, "/api/atlas?prefix=api-v2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("prefix=api-v2: status %d", rec.Code)
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode v2: %v", err)
	}
	if got.TotalPackages != 1 || len(got.Packages) != 1 || got.Packages[0].Path != "api-v2" {
		t.Errorf("prefix=api-v2: got %v, want only api-v2", got.Packages)
	}

	// No prefix: nothing is filtered.
	rec = httptest.NewRecorder()
	h.Atlas(rec, httptest.NewRequest(http.MethodGet, "/api/atlas", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no prefix: status %d", rec.Code)
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode no-prefix: %v", err)
	}
	if got.TotalPackages != 3 {
		t.Errorf("no prefix: TotalPackages=%d, want 3 (unfiltered)", got.TotalPackages)
	}
}
