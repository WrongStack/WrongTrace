package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
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
