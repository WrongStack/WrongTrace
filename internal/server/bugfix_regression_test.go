package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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
	var rdr io.Reader
	switch b := body.(type) {
	case io.Reader:
		rdr = b
	case nil:
		rdr = bytes.NewReader(nil)
	default:
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(data)
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

// TestAddProject_MalformedJSONNotMaskedByPathRequired is the regression test for the
// bug where AddProject's err != nil || req.Path == "" short-circuit misreported
// malformed JSON parse errors (wrong type, syntax error, oversize body) as "path is required".
// The client receives the wrong diagnostic and cannot self-correct.
func TestAddProject_MalformedJSONNotMaskedByPathRequired(t *testing.T) {
	withIsolatedProjectsHome(t)
	_, _, ts := newTestServer(t)

	// Genuinely malformed JSON: object with wrong types triggers json.Unmarshal error,
	// not the path check. decodeJSON must surface the parse error, not mask it.
	resp := projReq(t, ts, "POST", "/api/projects", []byte(`{"name": 123, "path": 456}`), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed JSON: status %d, want 400", resp.StatusCode)
	}
	var errBody map[string]string
	json.NewDecoder(resp.Body).Decode(&errBody)
	// Must describe the actual type error, not a missing-field error.
	if strings.Contains(errBody["error"], "json: cannot unmarshal") == false && strings.Contains(errBody["message"], "json: cannot unmarshal") == false {
		t.Fatalf("malformed JSON error was masked: got error=%q — should describe the name type error, not a missing-field error", errBody["error"])
	}
	// Should describe a JSON syntax / decode error.
	if errBody["error"] == "" && errBody["message"] == "" {
		t.Fatalf("error body missing: %+v", errBody)
	}

	// Valid JSON but wrong types — decode succeeds but Path is wrong type.
	type wrongTypeBody struct {
		Name int `json:"name"`
		Path int `json:"path"`
	}
	b, _ := json.Marshal(wrongTypeBody{Name: 123, Path: 456})
	resp = projReq(t, ts, "POST", "/api/projects", bytes.NewReader(b), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong-type JSON: status %d, want 400", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if strings.Contains(errBody["error"], "expected string for field 'name'") == false && strings.Contains(errBody["message"], "expected string for field 'name'") == false {
		t.Fatalf("wrong-type JSON error was masked: got error=%q", errBody["error"])
	}

	// Empty body — decode fails with "EOF".
	resp = projReq(t, ts, "POST", "/api/projects", bytes.NewReader(nil), nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty body: status %d, want 400", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if strings.Contains(errBody["error"], "invalid request body") == false && strings.Contains(errBody["message"], "invalid request body") == false {
		t.Fatalf("empty body error was masked: got error=%q", errBody["error"])
	}

	// Valid JSON, empty string path — should report "path is required".
	resp = projReq(t, ts, "POST", "/api/projects", map[string]string{"name": "ok", "path": ""}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty path: status %d, want 400", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody["error"] != "path is required" && errBody["message"] != "path is required" {
		t.Fatalf("empty path: got error=%q, want 'path is required'", errBody["error"])
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

// TestLockFileRejectsOverflowingTTL pins the round-51 contract: the HTTP
// lock endpoint (POST /api/guardrail/lock, handlers.go LockFile) must
// validate the 24h TTL cap BEFORE multiplying client integers into
// time.Duration nanoseconds. Pre-fix, ttl_seconds=18446744074 overflowed
// int64 ns into a ~0.29s lock and ttl_minutes=153722868 wrapped negative
// into the engine's silent 15-minute default — both answered 200 "locked".
// Mirrors the IPC transport cap (internal/ipc/socket.go lock_file dispatch)
// and the MCP lock_file tool. Boundary: exactly 24h is accepted.
func TestLockFileRejectsOverflowingTTL(t *testing.T) {
	eng := core.NewEngine(core.Config{RepoName: "ttl-proof"})
	h := Handlers{Engine: eng}
	now := time.Now()

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantTTL    time.Duration // 0 for rejections
	}{
		{"ttl_seconds overflow wraps to instant lock", `{"path":"x.go","reason":"p","ttl_seconds":18446744074}`, http.StatusBadRequest, 0},
		{"ttl_minutes overflow wraps negative", `{"path":"x.go","reason":"p","ttl_minutes":153722868}`, http.StatusBadRequest, 0},
		{"ttl string beyond 24h", `{"path":"x.go","reason":"p","ttl":"25h"}`, http.StatusBadRequest, 0},
		{"boundary exactly 24h accepted", `{"path":"x.go","reason":"p","ttl_seconds":86400}`, http.StatusOK, 24 * time.Hour},
		{"honest 60s lock accepted", `{"path":"x.go","reason":"p","ttl_seconds":60}`, http.StatusOK, time.Minute},
		{"invalid ttl string abc", `{"path":"x.go","reason":"p","ttl":"abc"}`, http.StatusBadRequest, 0},
		{"negative ttl string -1h", `{"path":"x.go","reason":"p","ttl":"-1h"}`, http.StatusBadRequest, 0},
		{"zero ttl string 0s", `{"path":"x.go","reason":"p","ttl":"0s"}`, http.StatusBadRequest, 0},
		{"honest ttl string 1h", `{"path":"x.go","reason":"p","ttl":"1h"}`, http.StatusOK, time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/guardrail/lock", bytes.NewReader([]byte(tc.body)))
			rec := httptest.NewRecorder()
			h.LockFile(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("HTTP %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantTTL > 0 {
				var resp struct {
					ExpiresAt time.Time `json:"expires_at"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				got := resp.ExpiresAt.Sub(now)
				if got < tc.wantTTL-time.Minute || got > tc.wantTTL+time.Minute {
					t.Fatalf("expires_at drift: got %s, want ≈%s", got, tc.wantTTL)
				}
			}
		})
	}
}

// TestAddProject_RejectsOversizedBodyNamingTheLimit pins that a project
// registration body larger than the handler's 10 MiB cap is REJECTED with a
// message that names the server's limit, rather than being truncated and
// blamed on the sender.
//
// AddProject read io.ReadAll(io.LimitReader(r.Body, 10<<20)) and never
// compared the length, so an over-cap body lost its tail. encoding/json then
// failed on the truncated prefix and the endpoint answered
//
//	400 {"error":"invalid request body: unexpected end of JSON input"}
//
// for a well-formed payload — the server's own limit reported as the
// sender's malformed JSON. Every other untrusted-body read in this package
// detects overflow (decodeJSON's http.MaxBytesReader, IngestOTLPTraces'
// limit+1 probe — see otlp_body_limit_test.go, which pins the same contract
// for the profiler mounts); AddProject was the last outlier.
//
// Status stays 400 by design: a sender whose body fits is untouched, so this
// is a diagnostic fix, not a contract change. The exactly-at-cap row is the
// off-by-one guard for the "+1" read: a body of exactly the cap is
// legitimate data and must not be reported as too large.
func TestAddProject_RejectsOversizedBodyNamingTheLimit(t *testing.T) {
	withIsolatedProjectsHome(t)
	_, _, ts := newTestServer(t)

	// addProjectBody builds a well-formed {"blob","name","path"} body whose
	// total length is exactly total bytes; the pad rides in an ignored field.
	addProjectBody := func(t *testing.T, dir string, total int) string {
		t.Helper()
		fields := map[string]string{"name": "proj", "path": dir, "blob": ""}
		base, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("marshal base body: %v", err)
		}
		pad := total - len(base)
		if pad < 0 {
			t.Fatalf("SETUP: base body is %d bytes, already over the %d-byte target", len(base), total)
		}
		fields["blob"] = strings.Repeat("a", pad)
		padded, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("marshal padded body: %v", err)
		}
		if len(padded) != total {
			t.Fatalf("SETUP: built %d bytes, want exactly %d", len(padded), total)
		}
		return string(padded)
	}

	const cap = 10 << 20
	dir := t.TempDir()

	t.Run("oversized_rejected_by_name", func(t *testing.T) {
		body := addProjectBody(t, dir, cap+1024) // one KiB over the cap
		resp := projReq(t, ts, http.MethodPost, "/api/projects", strings.NewReader(body), nil)
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read response body: %v", err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("FAIL: status = %d, want 400", resp.StatusCode)
		}
		low := strings.ToLower(string(b))
		if !strings.Contains(low, "too large") || !strings.Contains(low, "exceeds") {
			t.Errorf("FAIL: response does not name the server's body limit: %.300s", b)
		}
		if strings.Contains(string(b), "unexpected end of JSON input") {
			t.Errorf("FAIL: server-side truncation still surfaced as malformed sender JSON: %.300s", b)
		}
		if strings.Contains(low, "unmarshal") {
			t.Errorf("FAIL: parse error reported for a body the server refused to read whole: %.300s", b)
		}
	})

	t.Run("exactly_at_cap_is_accepted", func(t *testing.T) {
		body := addProjectBody(t, dir, cap)
		resp := projReq(t, ts, http.MethodPost, "/api/projects", strings.NewReader(body), nil)
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read response body: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("FAIL: body of exactly the cap rejected -- off-by-one in the overflow check: status=%d body=%.200s", resp.StatusCode, b)
		}
		if strings.Contains(string(b), "too large") {
			t.Errorf("FAIL: exactly-at-cap body reported as too large: %.300s", b)
		}
	})

	t.Run("controls", func(t *testing.T) {
		// Control (unchanged by the fix): a small body missing path is a
		// plain validation 400, not a size rejection.
		resp := projReq(t, ts, http.MethodPost, "/api/projects", map[string]string{"name": "n"}, nil)
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "path is required") {
			t.Errorf("CONTROL BROKEN (not this round's bug): missing-path status=%d body=%.200s", resp.StatusCode, b)
		}
		// Control: a small valid body still registers the project.
		resp = projReq(t, ts, http.MethodPost, "/api/projects", map[string]string{"name": "small", "path": dir}, nil)
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("CONTROL BROKEN (not this round's bug): valid small body rejected: status=%d body=%.200s", resp.StatusCode, b)
		}
	})
}
