package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// Events are keyed to a project by its name (repo_name). Renaming a project
// keeps its history visible immediately, updates Repo(), and leaves events
// alone when another project still shares the old name.
func TestUpdateProject_RenameKeepsHistory(t *testing.T) {
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
	e := NewEngine(Config{Store: global})
	t.Cleanup(e.Close)
	dirA := t.TempDir()
	pA, err := e.AddProject("alpha", dirA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.SwitchActiveProject(pA.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s := e.Store(); s != nil && s != global {
			_ = s.Close()
		}
	})
	emit := func(name, sig string) {
		file := filepath.Join(dirA, name)
		_ = os.WriteFile(file, []byte("package x\n"), 0o644)
		// RepoName left empty: persistAndBroadcast resolves it via FindProjectForFile.
		e.persistAndBroadcast(ast.DiffResult{FilePath: file, Events: []ast.Event{{
			FilePath: file, Signature: sig, NodeType: "function",
			Action: ast.ActionAdded, OccurredAt: time.Now().UTC(),
		}}})
	}
	emit("a.go", "func A()")
	emit("b.go", "func B()")
	before, err := e.Metrics()
	if err != nil {
		t.Fatal(err)
	}

	// CONTROL: a description-only edit keeps the history visible.
	if _, err := e.UpdateProject(ProjectProfile{ID: pA.ID, Description: "new description"}); err != nil {
		t.Fatal(err)
	}
	e.BumpCacheGen()
	ctl, _ := e.Metrics()
	t.Logf("CONTROL: description edit -> TotalEvents %d -> %d", before.Overview.TotalEvents, ctl.Overview.TotalEvents)
	if before.Overview.TotalEvents != 2 || ctl.Overview.TotalEvents != 2 {
		t.Fatalf("control invalid")
	}

	if _, err := e.UpdateProject(ProjectProfile{ID: pA.ID, Name: "alpha-service"}); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Metrics() // no manual cache bump: the rename must invalidate
	recent, _ := e.GetRecentEvents(50)
	if after.Overview.TotalEvents != 2 || len(recent) != 2 || e.Repo() != "alpha-service" {
		t.Fatalf("after rename: events=%d recent=%d Repo()=%q; want 2, 2, alpha-service",
			after.Overview.TotalEvents, len(recent), e.Repo())
	}

	// A second project named like the first's new name: renaming either
	// cannot tell their events apart, so nothing is relabelled.
	pB, err := e.AddProject("alpha-service", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.UpdateProject(ProjectProfile{ID: pB.ID, Name: "beta"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := e.Metrics(); m.Overview.TotalEvents != 2 {
		t.Fatalf("renaming a same-named project moved alpha's events: %d left", m.Overview.TotalEvents)
	}
}
