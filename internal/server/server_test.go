package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
	"github.com/wrongstack/wrongtrace/internal/models"
)

// newTestServer builds a Server around a REAL engine (real SQLite store in a
// temp dir) and exposes it over httptest. The chi router is the exact one
// production Start() serves.
func newTestServer(t *testing.T) (*core.Engine, *db.Store, *httptest.Server) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "srv.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	engine := core.NewEngine(core.Config{RepoName: "srv-test", Store: store})
	s := New(Config{Port: 0, Engine: engine})
	ts := httptest.NewServer(s.router)
	t.Cleanup(ts.Close)
	return engine, store, ts
}

func getJSON(t *testing.T, url string, out interface{}) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return resp
}

// seedHotFile inserts `n` events on one (file, signature) pair so the
// thrashing detector has a genuine hit. Timestamps are spaced 1s apart going
// back from `base`, making event_time ordering deterministic (SQLite stores
// second-granularity datetimes, so same-second seeds would tie arbitrarily).
func seedHotFile(t *testing.T, store *db.Store, n int, base time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := store.InsertEvent(db.EventRecord{
			EventID:    fmt.Sprintf("hot-%d", i),
			RepoName:   "srv-test",
			FilePath:   "src/hot.go",
			Signature:  "function:hot.go::Alpha",
			NodeType:   "function",
			Action:     "MODIFIED",
			BodyHash:   fmt.Sprintf("h%d", i),
			LOC:        2,
			OccurredAt: base.Add(time.Duration(-(n - i)) * time.Second),
		}); err != nil {
			t.Fatalf("seed hot event %d: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------
// REST handlers
// ---------------------------------------------------------------

func TestHealth(t *testing.T) {
	_, _, ts := newTestServer(t)
	var h map[string]interface{}
	resp := getJSON(t, ts.URL+"/api/health", &h)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if h["status"] != "ok" {
		t.Errorf("status field = %v, want ok", h["status"])
	}
	if h["repo"] != "srv-test" {
		t.Errorf("repo = %v, want srv-test", h["repo"])
	}
	if _, ok := h["ws_clients"].(float64); !ok {
		t.Errorf("ws_clients missing or not a number: %#v", h["ws_clients"])
	}
	if _, ok := h["timestamp"].(string); !ok {
		t.Error("timestamp missing")
	}
}

func TestOverview_EmptyStore(t *testing.T) {
	_, _, ts := newTestServer(t)
	var snap map[string]interface{}
	resp := getJSON(t, ts.URL+"/api/metrics/overview", &snap)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if snap["repo"] != "srv-test" {
		t.Errorf("repo = %v", snap["repo"])
	}
	ov, _ := snap["overview"].(map[string]interface{})
	if ov == nil {
		t.Fatal("overview object missing")
	}
	// db.Overview has no json tags, so keys serialize as Go field names.
	for _, k := range []string{"TotalRuns", "TotalEvents", "TotalCost", "UniqueModels"} {
		if _, ok := ov[k]; !ok {
			t.Errorf("overview.%s missing: %#v", k, ov)
		}
	}
}

func TestMetricsHandlers_WithSeededData(t *testing.T) {
	_, store, ts := newTestServer(t)

	// One run with spend + a thrashing hot file (4 edits) + a plain event.
	if err := store.UpsertRun(db.RunRecord{
		RunID: "r1", TaskID: "T1", AgentName: "t", ModelName: "claude-3-7-sonnet",
		Provider: "anthropic", CostUSD: 0.144, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	seedHotFile(t, store, 4, time.Now().UTC())
	if err := store.InsertEvent(db.EventRecord{
		EventID: "plain-1", RepoName: "srv-test", FilePath: "a.go",
		Signature: "function:a.go::One", NodeType: "function", Action: "ADDED",
		BodyHash: "x", LOC: 1, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed plain event: %v", err)
	}

	t.Run("overview aggregates", func(t *testing.T) {
		var snap struct {
			Overview struct {
				TotalRuns   int     `json:"TotalRuns"`
				TotalEvents int     `json:"TotalEvents"`
				TotalCost   float64 `json:"TotalCost"`
			} `json:"overview"`
		}
		getJSON(t, ts.URL+"/api/metrics/overview", &snap)
		if snap.Overview.TotalRuns != 1 || snap.Overview.TotalEvents != 5 {
			t.Errorf("overview = %+v, want runs=1 events=5", snap.Overview)
		}
		if snap.Overview.TotalCost != 0.144 {
			t.Errorf("TotalCost = %v, want 0.144", snap.Overview.TotalCost)
		}
	})

	t.Run("thrashing returns the hot node", func(t *testing.T) {
		var rows []map[string]interface{}
		getJSON(t, ts.URL+"/api/metrics/thrashing", &rows)
		if len(rows) != 1 {
			t.Fatalf("thrashing rows = %d, want 1: %#v", len(rows), rows)
		}
		if rows[0]["node_signature"] != "function:hot.go::Alpha" {
			t.Errorf("signature = %v", rows[0]["node_signature"])
		}
		if rows[0]["edit_count"] != float64(4) {
			t.Errorf("edit_count = %v, want 4", rows[0]["edit_count"])
		}
	})

	t.Run("models union includes spend and unattributed events", func(t *testing.T) {
		var models []map[string]interface{}
		getJSON(t, ts.URL+"/api/metrics/models", &models)
		// Two universe members: the reported run (spend, no events) and the
		// "unknown" model owning the un-attributed seeded events.
		if len(models) != 2 {
			t.Fatalf("models = %d, want 2: %#v", len(models), models)
		}
		byName := map[string]map[string]interface{}{}
		for _, m := range models {
			byName[m["model"].(string)] = m
		}
		sp := byName["claude-3-7-sonnet"]
		if sp == nil || sp["total_cost_usd"] != 0.144 || sp["run_count"] != float64(1) || sp["total_nodes"] != float64(0) {
			t.Errorf("spend-only model row = %#v", sp)
		}
		unk := byName["unknown"]
		if unk == nil || unk["total_nodes"] != float64(2) || unk["active_nodes"] != float64(2) || unk["survival_rate_pct"] != float64(100) {
			t.Errorf("unattributed-events model row = %#v", unk)
		}
	})

	t.Run("recent events ordered desc", func(t *testing.T) {
		var events []map[string]interface{}
		getJSON(t, ts.URL+"/api/metrics/recent", &events)
		if len(events) != 5 {
			t.Fatalf("recent = %d, want 5", len(events))
		}
		// Plain event seeded last (now) > hot events (now-4s..now-1s).
		if events[0]["node_signature"] != "function:a.go::One" {
			t.Errorf("first recent = %#v, want the newest seed (plain event)", events[0])
		}
		if events[1]["node_signature"] != "function:hot.go::Alpha" {
			t.Errorf("second recent = %#v, want hot event one second earlier", events[1])
		}
	})
}

func TestFileHealthEndpoint(t *testing.T) {
	_, store, ts := newTestServer(t)
	seedHotFile(t, store, 6, time.Now().UTC())

	t.Run("hot file is fragile", func(t *testing.T) {
		var fh struct {
			HealthScore int    `json:"health_score"`
			IsFragile   bool   `json:"is_fragile"`
			Count       int    `json:"recent_thrashing_count"`
			Warning     string `json:"warning"`
		}
		getJSON(t, ts.URL+"/api/file/health?path=src/hot.go", &fh)
		if fh.HealthScore != 52 || !fh.IsFragile || fh.Count != 6 || fh.Warning == "" {
			t.Errorf("file health = %+v, want score 52 fragile count 6", fh)
		}
	})

	t.Run("missing path is a 400", func(t *testing.T) {
		var body map[string]string
		resp := getJSON(t, ts.URL+"/api/file/health", &body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		if !strings.Contains(body["error"], "path is required") {
			t.Errorf("error = %q", body["error"])
		}
	})
}

func TestAPIUnknownRouteIsJSON404NotSPA(t *testing.T) {
	_, _, ts := newTestServer(t)
	resp := getJSON(t, ts.URL+"/api/no/such/route", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (API subtree must not fall through to SPA)", resp.StatusCode)
	}
}

// ---------------------------------------------------------------
// SPA fallback routing
// ---------------------------------------------------------------

func TestSPAFallback(t *testing.T) {
	_, _, ts := newTestServer(t)

	// The SPA contract is CONSISTENCY, not "the React shell": root and
	// unknown client routes must serve the SAME embedded index.html, and
	// real assets must be served verbatim. A fresh checkout embeds the
	// committed placeholder index (no <div id="root">) until `make
	// build-ui` runs, while a dev working tree may hold the placeholder
	// index alongside built assets — asserting React markers would fail in
	// exactly that mixed state.
	dist, err := WebDistFS()
	if err != nil {
		t.Fatalf("embedded dist unavailable: %v", err)
	}
	wantIndex, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	entries, err := fs.ReadDir(dist, "assets")
	if err != nil || len(entries) == 0 {
		t.Skipf("no built assets embedded (fs.ReadDir assets: %v) — run `make build-ui`", err)
	}
	asset := ""
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") {
			asset = "assets/" + e.Name()
			break
		}
	}
	if asset == "" {
		t.Skip("no .js asset in embedded dist")
	}

	t.Run("index served at root", func(t *testing.T) {
		resp, body := getText(t, ts.URL+"/")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if body != string(wantIndex) {
			t.Errorf("root document differs from the embedded index.html (%d vs %d bytes)", len(body), len(wantIndex))
		}
	})

	t.Run("real asset served verbatim", func(t *testing.T) {
		resp, body := getText(t, ts.URL+"/"+asset)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("asset %s: status = %d", asset, resp.StatusCode)
		}
		if body == string(wantIndex) {
			t.Errorf("asset request returned index.html instead of the asset")
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("asset content-type = %q", ct)
		}
	})

	t.Run("unknown client route falls back to index", func(t *testing.T) {
		for _, route := range []string{"/dashboard", "/models/leaderboard?tab=roi"} {
			resp, body := getText(t, ts.URL+route)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s: status = %d, want 200 (SPA fallback)", route, resp.StatusCode)
				continue
			}
			if body != string(wantIndex) {
				t.Errorf("%s: fallback did not serve the embedded index.html", route)
			}
		}
	})
}

func getText(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil || len(buf) > 65536 {
			break
		}
	}
	return resp, string(buf)
}

// ---------------------------------------------------------------
// /api/ws upgrade
// ---------------------------------------------------------------

// dialAPIWS upgrades against the real router via httptest.
func dialAPIWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return conn
}

func readFrame(t *testing.T, conn *websocket.Conn) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := conn.ReadJSON(&m); err != nil {
		t.Fatalf("read ws frame: %v", err)
	}
	return m
}

func TestWebSocket_UpgradeHelloAndLivePush(t *testing.T) {
	engine, _, ts := newTestServer(t)

	conn := dialAPIWS(t, ts)

	// 1) Greeting frame on connect.
	hello := readFrame(t, conn)
	if hello["type"] != "hello" {
		t.Fatalf("first frame type = %v, want hello: %#v", hello["type"], hello)
	}
	if hello["repo"] != "srv-test" {
		t.Errorf("hello repo = %v, want srv-test", hello["repo"])
	}

	// 2) The connection is registered with the hub (visible via /api/health).
	var h map[string]interface{}
	getJSON(t, ts.URL+"/api/health", &h)
	if h["ws_clients"] != float64(1) {
		t.Errorf("ws_clients = %v, want 1 while connected", h["ws_clients"])
	}

	// 3) A run report broadcast on the real engine reaches this client.
	if err := engine.ReportRun(ipc.TelemetryReport{
		RunID: "run-ws", TaskID: "T-1", AgentName: "Claude-Code",
		ModelName: "claude-3-7-sonnet", Provider: "anthropic", CostUSD: 0.5,
	}); err != nil {
		t.Fatalf("ReportRun: %v", err)
	}
	ev := readFrame(t, conn)
	if ev["type"] != "run_reported" {
		t.Fatalf("second frame type = %v, want run_reported: %#v", ev["type"], ev)
	}
	payload, _ := ev["payload"].(map[string]interface{})
	if payload["run_id"] != "run-ws" || payload["cost_usd"] != 0.5 {
		t.Errorf("run payload = %#v", payload)
	}

	// 4) After disconnect the hub no longer counts the client.
	_ = conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		getJSON(t, ts.URL+"/api/health", &h)
		if h["ws_clients"] == float64(0) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("ws_clients never returned to 0 after disconnect")
}

func TestWebSocket_PlainHTTPRequestDoesNotUpgrade(t *testing.T) {
	_, _, ts := newTestServer(t)
	resp := getJSON(t, ts.URL+"/api/ws", nil)
	// gorilla rejects the upgrade with 400 Bad Request; it must NOT hang,
	// panic, or serve SPA HTML.
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no Upgrade header)", resp.StatusCode)
	}
}

func TestAtlasEndpoint(t *testing.T) {
	_, _, ts := newTestServer(t)
	var atlas map[string]interface{}
	resp := getJSON(t, ts.URL+"/api/atlas", &atlas)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if atlas["repo"] != "srv-test" {
		t.Errorf("expected repo srv-test, got %v", atlas["repo"])
	}
	if _, ok := atlas["packages"]; !ok {
		t.Error("expected packages array in atlas response")
	}
}

func TestServerLifecycle(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer store.Close()
	_ = store.Migrate()

	engine := core.NewEngine(core.Config{RepoName: "lifecycle-test", Store: store})
	srv := New(Config{Port: 0, Engine: engine})

	if err := srv.Shutdown(nil); err != nil {
		t.Errorf("Shutdown on unstarted server returned error: %v", err)
	}
}

// catalogIDSeq keeps the custom model ID in TestModelCatalogEndpoints unique
// per invocation. models.Global is a process-wide registry and Registry.Upsert
// is idempotent on the ID it stores under, so a fixed ID grows the catalog only
// the first time it is posted; a later -count pass would then assert a growth
// that is mathematically impossible rather than testing the add path.
var catalogIDSeq atomic.Uint64

func TestModelCatalogEndpoints(t *testing.T) {
	_, _, ts := newTestServer(t)

	// 1. GET /api/models/catalog & GET /api/models/providers
	var catalog []map[string]interface{}
	resp := getJSON(t, ts.URL+"/api/models/catalog", &catalog)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, m := range catalog {
		if m["id"] == "" {
			t.Errorf("catalog entry missing id: %v", m)
		}
	}

	var providers []map[string]interface{}
	pResp := getJSON(t, ts.URL+"/api/models/providers", &providers)
	if pResp.StatusCode != http.StatusOK {
		t.Fatalf("providers status = %d, want 200", pResp.StatusCode)
	}

	// 2. POST /api/models/catalog (Add custom model)
	countBefore := len(catalog)
	customID := fmt.Sprintf("custom-deepseek-coder-%d", catalogIDSeq.Add(1))
	customBody := fmt.Sprintf(`{"id":%q,"name":"Custom DeepSeek Coder","provider":"Internal","input_price_per_m":0.1,"output_price_per_m":0.2,"context_window":64000}`, customID)
	postResp, err := http.Post(ts.URL+"/api/models/catalog", "application/json", strings.NewReader(customBody))
	if err != nil {
		t.Fatalf("POST /api/models/catalog: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d, want 200", postResp.StatusCode)
	}

	// 3. The custom model lands in the catalog as exactly one new entry
	// (models.Global is shared across this package's tests, so compare
	// against the pre-upsert count instead of an absolute number).
	var catalogAfter []map[string]interface{}
	getJSON(t, ts.URL+"/api/models/catalog", &catalogAfter)
	if len(catalogAfter) != countBefore+1 {
		t.Fatalf("expected catalog to grow by 1 (%d -> %d), got %d", countBefore, countBefore+1, len(catalogAfter))
	}
	found := false
	for _, m := range catalogAfter {
		if m["id"] == customID {
			found = true
			if m["is_custom"] != true {
				t.Errorf("custom model not marked is_custom: %v", m)
			}
		}
	}
	if !found {
		t.Errorf("%s missing from catalog after upsert", customID)
	}

	// 4. POST /api/models/calculate-cost
	calcBody := fmt.Sprintf(`{"model":%q,"prompt_tokens":1000000,"completion_tokens":1000000}`, customID)
	calcResp, err := http.Post(ts.URL+"/api/models/calculate-cost", "application/json", strings.NewReader(calcBody))
	if err != nil {
		t.Fatalf("POST /api/models/calculate-cost: %v", err)
	}
	defer calcResp.Body.Close()
	var calcResult map[string]interface{}
	if err := json.NewDecoder(calcResp.Body).Decode(&calcResult); err != nil {
		t.Fatalf("decode calculate cost: %v", err)
	}
	costVal, ok := calcResult["total_cost_usd"].(float64)
	if !ok || costVal < 0.299 || costVal > 0.301 {
		t.Errorf("expected ~$0.30 total cost, got %v", calcResult["total_cost_usd"])
	}
}

func TestProfilerEndpoints(t *testing.T) {
	_, _, ts := newTestServer(t)

	// 1. POST /api/profiler/ingest
	reportJSON := `{
		"service_name": "checkout-svc",
		"node_signature": "func:checkout.go::ProcessOrder",
		"file_path": "src/checkout.go",
		"duration_ms": 125.4,
		"cpu_usage_pct": 18.2,
		"memory_bytes": 2048576,
		"status_code": 200,
		"profiler_type": "pprof"
	}`
	resp, err := http.Post(ts.URL+"/api/profiler/ingest", "application/json", strings.NewReader(reportJSON))
	if err != nil {
		t.Fatalf("POST /api/profiler/ingest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("ingest status = %d, want 201", resp.StatusCode)
	}

	// 2. POST /v1/traces (OTLP)
	otlpJSON := `{
		"resourceSpans": [{
			"resource": {
				"attributes": [{"key": "service.name", "value": {"stringValue": "auth-svc"}}]
			},
			"scopeSpans": [{
				"spans": [{
					"traceId": "trace-9999",
					"spanId": "span-99",
					"name": "VerifyToken",
					"startTimeUnixNano": "1700000000000000000",
					"endTimeUnixNano": "1700000000025000000",
					"attributes": [
						{"key": "code.filepath", "value": {"stringValue": "src/auth.go"}},
						{"key": "code.function", "value": {"stringValue": "VerifyToken"}},
						{"key": "http.status_code", "value": {"intValue": "200"}}
					]
				}]
			}]
		}]
	}`
	otlpResp, err := http.Post(ts.URL+"/v1/traces", "application/json", strings.NewReader(otlpJSON))
	if err != nil {
		t.Fatalf("POST /v1/traces: %v", err)
	}
	defer otlpResp.Body.Close()
	if otlpResp.StatusCode != http.StatusOK {
		t.Fatalf("OTLP traces status = %d, want 200", otlpResp.StatusCode)
	}

	// 3. GET /api/profiler/traces
	var traces []map[string]interface{}
	getResp := getJSON(t, ts.URL+"/api/profiler/traces", &traces)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("traces status = %d, want 200", getResp.StatusCode)
	}
	if len(traces) != 2 {
		t.Errorf("expected 2 traces, got %d", len(traces))
	}

	// 4. GET /api/profiler/hotspots
	var hotspots []map[string]interface{}
	hResp := getJSON(t, ts.URL+"/api/profiler/hotspots", &hotspots)
	if hResp.StatusCode != http.StatusOK {
		t.Fatalf("hotspots status = %d, want 200", hResp.StatusCode)
	}
	if len(hotspots) == 0 {
		t.Error("expected hotspots, got none")
	}

	// 5. GET /api/profiler/overview
	var ov map[string]interface{}
	ovResp := getJSON(t, ts.URL+"/api/profiler/overview", &ov)
	if ovResp.StatusCode != http.StatusOK {
		t.Fatalf("overview status = %d, want 200", ovResp.StatusCode)
	}
	if total, ok := ov["total_traces"].(float64); !ok || total != 2 {
		t.Errorf("expected total_traces = 2, got %v", ov["total_traces"])
	}
}

func TestServer_FileReadEndpoints(t *testing.T) {
	engine, store, ts := newTestServer(t)
	now := time.Now().UTC()

	// Seed read events
	err := store.InsertReadEvent(db.FileReadRecord{
		ReadID:         "read-t1",
		RunID:          "run-101",
		RepoName:       engine.Repo(),
		FilePath:       "internal/core/engine.go",
		AgentName:      "Antigravity",
		ModelName:      "gemini-3.7-flash",
		Provider:       "google",
		ToolName:       "view_file",
		StartLine:      1,
		EndLine:        100,
		LinesReadCount: 100,
		PromptTokens:   1200,
		CostUSD:        0.002,
		ReadTime:       now,
	})
	if err != nil {
		t.Fatalf("insert read failed: %v", err)
	}

	// 1. GET /api/reads/recent
	var recentReads []db.FileReadRecord
	resp := getJSON(t, ts.URL+"/api/reads/recent", &recentReads)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/reads/recent = %d", resp.StatusCode)
	}
	if len(recentReads) != 1 || recentReads[0].ReadID != "read-t1" {
		t.Errorf("unexpected recent reads: %+v", recentReads)
	}

	// 2. GET /api/files/reads?path=internal/core/engine.go
	var stats db.FileReadStats
	resp = getJSON(t, ts.URL+"/api/files/reads?path=internal/core/engine.go", &stats)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/files/reads = %d", resp.StatusCode)
	}
	if stats.TotalReads != 1 || stats.TotalLinesRead != 100 {
		t.Errorf("unexpected file read stats: %+v", stats)
	}

	// 3. GET /api/files/heatmap?path=internal/core/engine.go
	var heatmap []db.LineReadHeatmap
	resp = getJSON(t, ts.URL+"/api/files/heatmap?path=internal/core/engine.go", &heatmap)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/files/heatmap = %d", resp.StatusCode)
	}
	if len(heatmap) != 1 || heatmap[0].StartLine != 1 || heatmap[0].EndLine != 100 {
		t.Errorf("unexpected heatmap: %+v", heatmap)
	}
}

func TestServer_FrictionAndAtlasEndpoints(t *testing.T) {
	_, _, ts := newTestServer(t)

	// 1. GET /api/metrics/friction
	var friction db.InterAgentFrictionReport
	resp := getJSON(t, ts.URL+"/api/metrics/friction", &friction)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/metrics/friction = %d", resp.StatusCode)
	}

	// 2. GET /api/metrics/cross-thrash
	var crossThrash db.InterAgentFrictionReport
	resp = getJSON(t, ts.URL+"/api/metrics/cross-thrash", &crossThrash)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/metrics/cross-thrash = %d", resp.StatusCode)
	}

	// 3. GET /api/atlas
	var atlas core.AtlasSnapshot
	resp = getJSON(t, ts.URL+"/api/atlas", &atlas)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/atlas = %d", resp.StatusCode)
	}

	// 4. GET /api/models/catalog
	var catalog []models.ModelInfo
	resp = getJSON(t, ts.URL+"/api/models/catalog", &catalog)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/models/catalog = %d", resp.StatusCode)
	}
	if len(catalog) == 0 {
		t.Errorf("expected models in catalog")
	}

	// 5. GET /api/projects
	var projects []core.ProjectProfile
	resp = getJSON(t, ts.URL+"/api/projects", &projects)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/projects = %d", resp.StatusCode)
	}

	// 6. GET /api/settings
	var settings core.AppSettings
	resp = getJSON(t, ts.URL+"/api/settings", &settings)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/settings = %d", resp.StatusCode)
	}

	// 7. GET /api/metrics/recent with limit and query
	var recentEvs []db.EventRecord
	resp = getJSON(t, ts.URL+"/api/metrics/recent?limit=10&repo=srv-test", &recentEvs)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/metrics/recent = %d", resp.StatusCode)
	}

	// 8. GET /api/symbols/history
	var history []db.SymbolHistoryRecord
	resp = getJSON(t, ts.URL+"/api/symbols/history?signature=function:hot.go::Alpha", &history)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/symbols/history = %d", resp.StatusCode)
	}

	// 9. GET /api/files/activity
	var activity []db.ModelActivitySummary
	resp = getJSON(t, ts.URL+"/api/files/activity?file_path=src/hot.go", &activity)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/files/activity = %d", resp.StatusCode)
	}

	// 10. POST /api/settings/vacuum
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/settings/vacuum", nil)
	vacResp, err := http.DefaultClient.Do(req)
	if err != nil || vacResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/settings/vacuum failed: %v", err)
	}
	_ = vacResp.Body.Close()
}

func TestServer_Enhancements_WrongStackReport(t *testing.T) {
	engine, store, ts := newTestServer(t)

	// 1. GET /api/health returns ok:true and status:ok
	var healthResp map[string]interface{}
	resp := getJSON(t, ts.URL+"/api/health", &healthResp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/health = %d", resp.StatusCode)
	}
	if healthResp["ok"] != true || healthResp["status"] != "ok" {
		t.Errorf("health resp missing ok:true: %+v", healthResp)
	}

	// 2. POST /api/telemetry (CRITICAL missing endpoint)
	telemetryBody := `{
		"run_id": "run-ws-402",
		"task_id": "TASK-12",
		"agent_name": "WrongStack",
		"model_name": "claude-3-7-sonnet",
		"provider": "anthropic",
		"prompt_tokens": 12000,
		"completion_tokens": 850,
		"cost_usd": 0.048,
		"intent": "Refactor auth middleware"
	}`
	postResp, err := http.Post(ts.URL+"/api/telemetry", "application/json", strings.NewReader(telemetryBody))
	if err != nil {
		t.Fatalf("POST /api/telemetry: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/telemetry status = %d, want 200", postResp.StatusCode)
	}
	var telemResult map[string]interface{}
	if err := json.NewDecoder(postResp.Body).Decode(&telemResult); err != nil {
		t.Fatalf("decode telemetry result: %v", err)
	}
	if telemResult["ok"] != true || telemResult["event_id"] != "run-ws-402" {
		t.Errorf("telemetry result wrong: %+v", telemResult)
	}

	// Seed code mutation event correlated with the run
	now := time.Now().UTC()
	err = store.InsertEvent(db.EventRecord{
		EventID:    "ev-corr-1",
		RunID:      "run-ws-402",
		RepoName:   "srv-test",
		FilePath:   "internal/server/server.go",
		Signature:  "function:server.go::New",
		NodeType:   "function",
		Action:     "MODIFIED",
		BodyHash:   "hash-new",
		LOC:        10,
		OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	// 3. GET /api/events/recent with since, limit, repo and author_model
	var recentEvents []db.EventRecord
	getJSON(t, ts.URL+"/api/events/recent?limit=50&repo=srv-test", &recentEvents)
	if len(recentEvents) == 0 {
		t.Fatalf("GET /api/events/recent returned 0 events")
	}
	foundCorr := false
	for _, ev := range recentEvents {
		if ev.EventID == "ev-corr-1" {
			foundCorr = true
			if ev.AuthorModel != "claude-3-7-sonnet" {
				t.Errorf("expected author_model claude-3-7-sonnet, got %q", ev.AuthorModel)
			}
			if ev.Timestamp.IsZero() {
				t.Errorf("expected timestamp populated on event")
			}
		}
	}
	if !foundCorr {
		t.Errorf("ev-corr-1 missing from /api/events/recent")
	}

	// 4. GET /api/cross-thrash
	var crossReport db.InterAgentFrictionReport
	getJSON(t, ts.URL+"/api/cross-thrash", &crossReport)
	// Must return 200 and valid report struct

	// 5. POST /api/guardrail/lock with owner and TTL
	lockBody := `{
		"path": "internal/server/server.go",
		"reason": "WrongStack refactor in progress",
		"owner": "WrongStack (claude-3-7-sonnet)",
		"owner_run_id": "run-ws-402",
		"ttl_minutes": 15
	}`
	lockResp, err := http.Post(ts.URL+"/api/guardrail/lock", "application/json", strings.NewReader(lockBody))
	if err != nil {
		t.Fatalf("POST /api/guardrail/lock: %v", err)
	}
	defer lockResp.Body.Close()
	if lockResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/guardrail/lock status = %d, want 200", lockResp.StatusCode)
	}
	var lockResult map[string]interface{}
	_ = json.NewDecoder(lockResp.Body).Decode(&lockResult)
	if lockResult["ok"] != true || lockResult["status"] != "locked" || lockResult["owner"] != "WrongStack (claude-3-7-sonnet)" {
		t.Errorf("lock result wrong: %+v", lockResult)
	}
	if lockResult["expires_at"] == nil {
		t.Errorf("lock result missing expires_at TTL timestamp")
	}

	// 6. GET /api/guardrail/locks lists active locks
	var activeLocks []core.LockInfo
	getJSON(t, ts.URL+"/api/guardrail/locks", &activeLocks)
	if len(activeLocks) != 1 || activeLocks[0].Owner != "WrongStack (claude-3-7-sonnet)" {
		t.Errorf("expected 1 active lock, got: %+v", activeLocks)
	}

	// 7. GET /api/file/health returns lock status
	var fileHealthResp struct {
		FilePath             string `json:"file_path"`
		HealthScore          int    `json:"health_score"`
		IsFragile            bool   `json:"is_fragile"`
		RecentThrashingCount int    `json:"recent_thrashing_count"`
		IsLocked             bool   `json:"is_locked"`
		LockReason           string `json:"lock_reason"`
		LockOwner            string `json:"lock_owner"`
	}
	getJSON(t, ts.URL+"/api/file/health?path=internal/server/server.go", &fileHealthResp)
	if !fileHealthResp.IsLocked || fileHealthResp.LockOwner != "WrongStack (claude-3-7-sonnet)" {
		t.Errorf("file health missing lock details: %+v", fileHealthResp)
	}

	// 8. GET /api/symbol/history with free-form signature like "New()" or "New"
	var symbolHist []db.SymbolHistoryRecord
	getJSON(t, ts.URL+"/api/symbol/history?path=internal/server/server.go&signature=New()", &symbolHist)
	if len(symbolHist) == 0 {
		t.Errorf("symbol history with 'New()' query returned empty list")
	}

	// GET /api/symbol/history with path only
	var pathHist []db.SymbolHistoryRecord
	getJSON(t, ts.URL+"/api/symbol/history?path=internal/server/server.go", &pathHist)
	if len(pathHist) == 0 {
		t.Errorf("symbol history with path only returned empty list")
	}

	// 9. GET /api/atlas with summary=true, prefix, pagination
	var summaryAtlas core.AtlasSnapshot
	getJSON(t, ts.URL+"/api/atlas?summary=true&limit=10", &summaryAtlas)
	if len(summaryAtlas.Packages) > 0 && len(summaryAtlas.Packages[0].Files) > 0 {
		if len(summaryAtlas.Packages[0].Files[0].Symbols) != 0 {
			t.Errorf("expected 0 symbols in summary mode, got %d", len(summaryAtlas.Packages[0].Files[0].Symbols))
		}
	}

	// 10. GET /api/files/activity without path returns 200 with all activity
	var allAct []db.ModelActivitySummary
	actResp := getJSON(t, ts.URL+"/api/files/activity", &allAct)
	if actResp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/files/activity without path = %d, want 200", actResp.StatusCode)
	}

	// 11. POST /api/guardrail/lock on locked file returns 409 Conflict when locked by different owner
	_ = engine.LockFileWithOptions("internal/server/server.go", "first lock", "Agent1", "run-1", 10*time.Minute)
	conflictBody := `{"path":"internal/server/server.go","owner":"Agent2","reason":"attempting lock"}`
	confResp, err := http.Post(ts.URL+"/api/guardrail/lock", "application/json", strings.NewReader(conflictBody))
	if err != nil {
		t.Fatalf("POST conflict lock: %v", err)
	}
	defer confResp.Body.Close()
	if confResp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 Conflict when file locked by another owner, got %d", confResp.StatusCode)
	}
	var confJson map[string]interface{}
	_ = json.NewDecoder(confResp.Body).Decode(&confJson)
	if confJson["status"] != "conflict" || confJson["owner"] != "Agent1" {
		t.Errorf("conflict payload unexpected: %+v", confJson)
	}

	// 12. GET /api/nonexistent returns JSON 404
	errResp, err := http.Get(ts.URL + "/api/nonexistent/endpoint")
	if err != nil {
		t.Fatalf("GET nonexistent: %v", err)
	}
	defer errResp.Body.Close()
	if errResp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", errResp.StatusCode)
	}
	var errBody map[string]interface{}
	_ = json.NewDecoder(errResp.Body).Decode(&errBody)
	if errBody["error"] == nil || errBody["message"] == nil {
		t.Errorf("expected JSON error structure, got: %+v", errBody)
	}

	// 13. GET /api/events/recent?since=...
	var sinceEvents []db.EventRecord
	getJSON(t, ts.URL+"/api/events/recent?since=2026-08-24T00:00:00Z", &sinceEvents)
	if len(sinceEvents) == 0 {
		t.Errorf("expected events with since filter, got 0")
	}

	// 14. GET /api/ipc/traffic returns recorded IPC calls
	engine.RecordIPCTraffic(ipc.IPCTrafficRecord{
		ID:         "ipc-test-1",
		Method:     "telemetry/file_health",
		Params:     map[string]interface{}{"file_path": "internal/server/server.go"},
		Result:     map[string]interface{}{"health_score": 92},
		DurationMs: 0.85,
		Timestamp:  time.Now().UTC(),
	})
	var ipcTraffic []ipc.IPCTrafficRecord
	getJSON(t, ts.URL+"/api/ipc/traffic?detail=false", &ipcTraffic)
	if len(ipcTraffic) == 0 || ipcTraffic[0].Method != "telemetry/file_health" {
		t.Errorf("expected recorded IPC traffic, got: %+v", ipcTraffic)
	}
	if ipcTraffic[0].Result != nil {
		t.Errorf("IPC summary unexpectedly retained result: %+v", ipcTraffic[0].Result)
	}
	var ipcDetail ipc.IPCTrafficRecord
	getJSON(t, ts.URL+"/api/ipc/traffic/ipc-test-1", &ipcDetail)
	if ipcDetail.Result == nil || ipcDetail.Params["file_path"] != "internal/server/server.go" {
		t.Errorf("expected on-demand IPC detail, got: %+v", ipcDetail)
	}

	// 15. GET /api/atlas?include_symbols=false
	var noSymAtlas core.AtlasSnapshot
	getJSON(t, ts.URL+"/api/atlas?include_symbols=false", &noSymAtlas)
	if len(noSymAtlas.Packages) > 0 && len(noSymAtlas.Packages[0].Files) > 0 {
		if len(noSymAtlas.Packages[0].Files[0].Symbols) != 0 {
			t.Errorf("expected stripped symbols with include_symbols=false, got %d", len(noSymAtlas.Packages[0].Files[0].Symbols))
		}
	}

	// 16. POST /api/guardrail/unlock unlocks file
	unlockBody := `{"path":"internal/server/server.go"}`
	unlockResp, err := http.Post(ts.URL+"/api/guardrail/unlock", "application/json", strings.NewReader(unlockBody))
	if err != nil {
		t.Fatalf("POST /api/guardrail/unlock: %v", err)
	}
	defer unlockResp.Body.Close()
	if unlockResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/guardrail/unlock = %d", unlockResp.StatusCode)
	}
	getJSON(t, ts.URL+"/api/file/health?path=internal/server/server.go", &fileHealthResp)
	if fileHealthResp.IsLocked {
		t.Errorf("file still locked after unlock")
	}

	_ = engine
}
