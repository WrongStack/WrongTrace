package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
)

func TestAtlas_EmptyAndWithData(t *testing.T) {
	tempDir := t.TempDir()
	store, err := db.Open(filepath.Join(tempDir, "atlas.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	astEng, err := ast.NewEngine()
	if err != nil {
		t.Fatalf("ast.NewEngine: %v", err)
	}
	defer astEng.Close()

	engine := NewEngine(Config{
		RepoName: "atlas-repo",
		Store:    store,
		AST:      astEng,
	})

	// 1. Empty Atlas snapshot
	snap, err := engine.Atlas()
	if err != nil {
		t.Fatalf("Atlas() on empty engine failed: %v", err)
	}
	if snap.Repo != "atlas-repo" {
		t.Errorf("expected repo atlas-repo, got %s", snap.Repo)
	}
	if len(snap.Packages) != 0 {
		t.Errorf("expected 0 packages, got %d", len(snap.Packages))
	}

	// 2. Prime directory with dummy Go and Python files
	subPkg := filepath.Join(tempDir, "pkg", "auth")
	if err := os.MkdirAll(subPkg, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	goFile := filepath.Join(subPkg, "auth.go")
	goSrc := `package auth

func ValidateToken(t string) bool {
	return len(t) > 0
}

type UserSession struct {
	ID string
}
`
	if err := os.WriteFile(goFile, []byte(goSrc), 0644); err != nil {
		t.Fatalf("write auth.go: %v", err)
	}

	pyFile := filepath.Join(tempDir, "main.py")
	pySrc := `def run_server():
    print("running")
`
	if err := os.WriteFile(pyFile, []byte(pySrc), 0644); err != nil {
		t.Fatalf("write main.py: %v", err)
	}

	engine.PrimeDirectory(tempDir)

	// Seed some DB node stats
	_ = store.InsertEvent(db.EventRecord{
		EventID:      "ev-1",
		RepoName:     "atlas-repo",
		FilePath:     goFile,
		Signature:    "function:" + filepath.Base(goFile) + "::ValidateToken",
		NodeType:     "function",
		Action:       "MODIFIED",
		BodyHash:     "hash123",
		LOC:          3,
		StartLine:    3,
		EndLine:      5,
		DiffSnippet:  "+ return len(t) > 0",
		AddedLines:   1,
		DeletedLines: 0,
		OccurredAt:   time.Now().UTC(),
	})

	// 3. Query Atlas snapshot with indexed files
	snap, err = engine.Atlas()
	if err != nil {
		t.Fatalf("Atlas() with primed files failed: %v", err)
	}

	if snap.TotalFiles < 2 {
		t.Errorf("expected at least 2 files, got %d", snap.TotalFiles)
	}
	if snap.TotalNodes < 2 {
		t.Errorf("expected at least 2 nodes, got %d", snap.TotalNodes)
	}

	// Verify symbolShortName helper
	if name := symbolShortName("function:auth.go::ValidateToken"); name != "ValidateToken" {
		t.Errorf("symbolShortName failed, got %s", name)
	}
	if name := symbolShortName("class:server.py:Server"); name != "Server" {
		t.Errorf("symbolShortName failed, got %s", name)
	}
	if name := symbolShortName("simple_symbol"); name != "simple_symbol" {
		t.Errorf("symbolShortName failed, got %s", name)
	}
}

func TestEngine_HandleFileChange_And_Metrics(t *testing.T) {
	tempDir := t.TempDir()
	store, err := db.Open(filepath.Join(tempDir, "change.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer store.Close()
	_ = store.Migrate()

	astEng, err := ast.NewEngine()
	if err != nil {
		t.Fatalf("ast.NewEngine: %v", err)
	}
	defer astEng.Close()

	engine := NewEngine(Config{
		RepoName: "change-repo",
		Store:    store,
		AST:      astEng,
	})

	filePath := filepath.Join(tempDir, "calc.go")
	src1 := `package calc

func Add(a, b int) int {
	return a + b
}
`
	if err := os.WriteFile(filePath, []byte(src1), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// 1. Initial change (first snapshot)
	engine.HandleFileChange(context.Background(), filePath)

	// 2. Modify file to trigger diff events
	src2 := `package calc

func Add(a, b int) int {
	// modified comment
	return a + b + 0
}

func Sub(a, b int) int {
	return a - b
}
`
	if err := os.WriteFile(filePath, []byte(src2), 0644); err != nil {
		t.Fatalf("modify file: %v", err)
	}

	engine.HandleFileChange(context.Background(), filePath)

	// Check metrics snapshot
	metrics, err := engine.Metrics()
	if err != nil {
		t.Fatalf("engine.Metrics() error: %v", err)
	}
	if metrics.Repo != "change-repo" {
		t.Errorf("metrics repo = %s, want change-repo", metrics.Repo)
	}

	// 3. Delete file and trigger gone handler
	_ = os.Remove(filePath)
	engine.HandleFileChange(context.Background(), filePath)

	// Verify shouldSkip filters
	if !engine.shouldSkip("file.unknown_extension_xyz") {
		t.Error("shouldSkip should be true for unknown extensions")
	}
	if engine.shouldSkip("valid.go") {
		t.Error("shouldSkip should be false for valid.go")
	}
}
