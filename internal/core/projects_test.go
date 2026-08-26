package core

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
)

func TestProjectIsolation_AtlasAndMetrics(t *testing.T) {
	tempBase := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", tempBase)

	// 1. Create Workspace A
	dirA := filepath.Join(tempBase, "WorkspaceA")
	_ = os.MkdirAll(filepath.Join(dirA, "pkg"), 0755)
	fileA := filepath.Join(dirA, "pkg", "service_a.go")
	_ = os.WriteFile(fileA, []byte("package pkg\n\nfunc HandleServiceA() string { return \"A\" }\n"), 0644)

	// 2. Create Workspace B
	dirB := filepath.Join(tempBase, "WorkspaceB")
	_ = os.MkdirAll(filepath.Join(dirB, "src"), 0755)
	fileB := filepath.Join(dirB, "src", "client_b.ts")
	_ = os.WriteFile(fileB, []byte("export function fetchClientB() { return 42; }\n"), 0644)

	dbPathA := filepath.Join(tempBase, "proj_a.db")
	storeA, err := db.Open(dbPathA)
	if err != nil {
		t.Fatalf("open db A: %v", err)
	}
	_ = storeA.Migrate()

	astEng, err := ast.NewEngine()
	if err != nil {
		t.Fatalf("init ast: %v", err)
	}
	defer astEng.Close()

	engine := NewEngine(Config{
		RepoName: "WorkspaceA",
		Store:    storeA,
		AST:      astEng,
	})
	defer func() {
		if engine.cfg.Store != nil {
			_ = engine.cfg.Store.Close()
		}
	}()

	// Add Project A
	projA, err := engine.AddProject("WorkspaceA", dirA)
	if err != nil {
		t.Fatalf("add project A failed: %v", err)
	}

	// Add Project B
	projB, err := engine.AddProject("WorkspaceB", dirB)
	if err != nil {
		t.Fatalf("add project B failed: %v", err)
	}

	// Switch to Project A
	if _, err := engine.SwitchActiveProject(projA.ID); err != nil {
		t.Fatalf("switch to A failed: %v", err)
	}
	engine.PrimeDirectory(dirA)

	// Report telemetry into Project A
	_ = engine.ReportRun(ipc.TelemetryReport{
		RunID:     "run-a-001",
		AgentName: "Claude",
		ModelName: "claude-3-7-sonnet",
		CostUSD:   1.25,
	})

	// Check Atlas for Project A
	atlasA, err := engine.Atlas()
	if err != nil {
		t.Fatalf("atlas A error: %v", err)
	}
	if len(atlasA.Packages) == 0 {
		t.Errorf("expected packages in Project A, got 0")
	}
	for _, p := range atlasA.Packages {
		for _, f := range p.Files {
			if f.Name == "client_b.ts" {
				t.Errorf("Project A Atlas leaked Project B file client_b.ts")
			}
		}
	}

	// Switch to Project B
	if _, err := engine.SwitchActiveProject(projB.ID); err != nil {
		t.Fatalf("switch to B failed: %v", err)
	}
	engine.PrimeDirectory(dirB)

	// Verify Project B's Atlas does NOT contain Project A's files
	atlasB, err := engine.Atlas()
	if err != nil {
		t.Fatalf("atlas B error: %v", err)
	}
	for _, p := range atlasB.Packages {
		for _, f := range p.Files {
			if f.Name == "service_a.go" {
				t.Errorf("Project B Atlas leaked Project A file service_a.go")
			}
		}
	}

	// Verify Project B's metrics database is isolated from Project A's runs
	overviewB, err := engine.cfg.Store.Overview()
	if err != nil {
		t.Fatalf("overview B error: %v", err)
	}
	if overviewB.TotalRuns != 0 {
		t.Errorf("expected 0 runs in clean Project B db, got %d", overviewB.TotalRuns)
	}

	// Report telemetry into Project B
	_ = engine.ReportRun(ipc.TelemetryReport{
		RunID:     "run-b-001",
		AgentName: "Cursor",
		ModelName: "gpt-4o",
		CostUSD:   0.50,
	})

	overviewB2, _ := engine.cfg.Store.Overview()
	if overviewB2.TotalRuns != 1 {
		t.Errorf("expected 1 run in Project B db, got %d", overviewB2.TotalRuns)
	}
}

func TestProjectLifecycle(t *testing.T) {
	tempBase := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", tempBase)
	dir := filepath.Join(tempBase, "sample_repo")
	_ = os.MkdirAll(dir, 0755)

	dbPath := filepath.Join(tempBase, "root.db")
	store, _ := db.Open(dbPath)
	_ = store.Migrate()
	defer store.Close()

	astEng, _ := ast.NewEngine()
	defer astEng.Close()

	engine := NewEngine(Config{
		RepoName: "sample_repo",
		Store:    store,
		AST:      astEng,
	})

	proj, err := engine.AddProject("sample_repo", dir)
	if err != nil {
		t.Fatalf("add project failed: %v", err)
	}

	// Update
	proj.Description = "Updated description"
	updated, err := engine.UpdateProject(proj)
	if err != nil || updated.Description != "Updated description" {
		t.Fatalf("update project failed: %v", err)
	}

	// List
	list := engine.ListProjects()
	if len(list) == 0 {
		t.Fatalf("expected >= 1 project in list")
	}

	// Remove
	if err := engine.RemoveProject(proj.ID); err != nil {
		t.Fatalf("remove project failed: %v", err)
	}
}

func TestSessionScanCacheIsBoundedAndPrunesExpired(t *testing.T) {
	sessScanMu.Lock()
	previous := sessScanCache
	sessScanCache = make(map[string]cachedSessScan)
	sessScanMu.Unlock()
	t.Cleanup(func() {
		sessScanMu.Lock()
		sessScanCache = previous
		sessScanMu.Unlock()
	})

	now := time.Now()
	storeSessScan("expired", map[string]int{"old": 1}, now.Add(-2*sessScanCacheTTL))
	for i := 0; i < maxSessScanCacheEntries+20; i++ {
		storeSessScan(fmt.Sprintf("root-%03d", i), map[string]int{"test": i}, now.Add(time.Duration(i)*time.Millisecond))
	}

	sessScanMu.RLock()
	defer sessScanMu.RUnlock()
	if _, ok := sessScanCache["expired"]; ok {
		t.Fatal("expired session scan remained cached")
	}
	if got := len(sessScanCache); got > maxSessScanCacheEntries {
		t.Fatalf("session scan cache grew to %d entries, cap is %d", got, maxSessScanCacheEntries)
	}
}
