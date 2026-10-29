package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// Every registered project is watched and its edits land in the active store
// under its own repo_name. Per-file answers (symbol history, file health, the
// guardrail) for a project-relative path must stay within one project: the
// active one by default, or the project_id the dashboard sends.
func TestPerFileEndpoints_ScopedToProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", home)

	global, err := db.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = global.Close() })
	if err := global.Migrate(); err != nil {
		t.Fatal(err)
	}
	astEng, err := ast.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(astEng.Close)
	e := core.NewEngine(core.Config{Store: global, AST: astEng})
	t.Cleanup(e.Close)

	dirA, dirB := t.TempDir(), t.TempDir()
	pA, err := e.AddProject("alpha", dirA)
	if err != nil {
		t.Fatal(err)
	}
	pB, err := e.AddProject("beta", dirB)
	if err != nil {
		t.Fatal(err)
	}
	// Bind alpha's own store explicitly (sidesteps F233).
	if _, err := e.SwitchActiveProject(pA.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s := e.Store(); s != nil && s != global {
			_ = s.Close()
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for e.IndexStatus().IsIndexing && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	write := func(dir string, n int) {
		p := filepath.Join(dir, "main.go")
		body := ""
		for i := 0; i <= n; i++ {
			body += fmt.Sprintf("\tprintln(%d)\n", i)
		}
		if err := os.WriteFile(p, []byte("package main\n\nfunc Work() {\n"+body+"}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		e.HandleFileChange(context.Background(), p)
	}
	// alpha: created + 1 edit (calm). beta: created + 6 edits (thrashing).
	for i := 0; i < 2; i++ {
		write(dirA, i)
	}
	for i := 0; i < 7; i++ {
		write(dirB, i)
	}

	srv := New(Config{Port: 0, Engine: e})
	ts := httptest.NewServer(srv.router)
	t.Cleanup(ts.Close)
	get := func(path string, out interface{}) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s -> %d", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	countRepo := func(repos []string) map[string]int {
		m := map[string]int{}
		for _, r := range repos {
			m[r]++
		}
		return m
	}

	// CONTROL: the project-scoped file history endpoint.
	var recent []db.EventRecord
	get("/api/metrics/recent?file_path=main.go&limit=100&project_id="+url.QueryEscape(pA.ID), &recent)
	var rr []string
	sig := ""
	for _, r := range recent {
		rr = append(rr, r.RepoName)
		if sig == "" {
			sig = r.Signature
		}
	}
	ctl := countRepo(rr)
	if ctl["alpha"] == 0 || ctl["beta"] != 0 || sig == "" {
		t.Fatalf("control invalid: %v sig=%q", ctl, sig)
	}

	var hist []db.SymbolHistoryRecord
	get("/api/symbol/history?path=main.go&signature="+url.QueryEscape(sig)+"&project_id="+url.QueryEscape(pA.ID), &hist)
	var hr []string
	for _, h := range hist {
		hr = append(hr, h.RepoName)
	}
	hc := countRepo(hr)

	var health core.IPCHealth
	get("/api/file/health?path=main.go&project_id="+url.QueryEscape(pA.ID), &health)
	var guard core.GuardrailResult
	get("/api/guardrail/check?path=main.go", &guard)

	if hc["beta"] > 0 || hc["alpha"] == 0 {
		t.Fatalf("symbol history for alpha's main.go mixes in beta: %v", hc)
	}
	if health.RecentThrashingCount > ctl["alpha"] {
		t.Fatalf("file health counts %d edits; alpha's main.go has %d", health.RecentThrashingCount, ctl["alpha"])
	}
	if !guard.Allowed {
		t.Fatalf("guardrail blocked alpha's main.go for beta's churn: %q", guard.Recommendation)
	}
	// Scoped to beta explicitly, the same path reports beta's churn.
	var betaHealth core.IPCHealth
	get("/api/file/health?path=main.go&project_id="+url.QueryEscape(pB.ID), &betaHealth)
	if betaHealth.RecentThrashingCount <= ctl["alpha"] {
		t.Fatalf("beta-scoped health = %d edits; want beta's own (more than alpha's %d)", betaHealth.RecentThrashingCount, ctl["alpha"])
	}
}
