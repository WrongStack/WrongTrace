package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
)

func storeTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"WRONGTRACE_HOME", "HOME", "USERPROFILE", "APPDATA"} {
		t.Setenv(k, home)
	}
}

func openMigrated(t *testing.T, path string) *db.Store {
	t.Helper()
	s, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	return s
}

func emitEvent(t *testing.T, e *Engine, repo, file, sig string) {
	t.Helper()
	_ = os.WriteFile(file, []byte("package x\n"), 0o644)
	e.persistAndBroadcast(ast.DiffResult{FilePath: file, Events: []ast.Event{{
		RepoName: repo, FilePath: file, Signature: sig, NodeType: "function",
		Action: ast.ActionAdded, OccurredAt: time.Now().UTC(),
	}}})
}

func samePath(a, b string) bool { return normalizeRoot(a) == normalizeRoot(b) }

// The first project keeps the store events are already written to, so its
// history survives switching away and back.
func TestFirstProjectAdoptsOpenStore(t *testing.T) {
	storeTestHome(t)
	globalPath := filepath.Join(t.TempDir(), "global.db")
	global := openMigrated(t, globalPath)
	e := NewEngine(Config{Store: global})
	t.Cleanup(e.Close)

	dirA := t.TempDir()
	pA, err := e.AddProject("alpha", dirA)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(pA.DBPath, globalPath) {
		t.Fatalf("first project DBPath = %s; want the open store %s", pA.DBPath, globalPath)
	}
	emitEvent(t, e, "alpha", filepath.Join(dirA, "a.go"), "func A()")
	pB, err := e.AddProject("beta", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if samePath(pB.DBPath, globalPath) {
		t.Fatal("second project shares the first one's database")
	}
	for _, id := range []string{pB.ID, pA.ID} {
		if _, err := e.SwitchActiveProject(id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = e.Store().Close() })
	if m, _ := e.Metrics(); m.Overview.TotalEvents != 1 {
		t.Fatalf("alpha after a switch round trip shows %d events; want 1", m.Overview.TotalEvents)
	}
}

// After a restart the active project reads its own database, the one its
// events went to before the restart.
func TestRestartBindsActiveProjectStore(t *testing.T) {
	storeTestHome(t)
	globalPath := filepath.Join(t.TempDir(), "global.db")
	e := NewEngine(Config{Store: openMigrated(t, globalPath)})
	if _, err := e.AddProject("alpha", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	dirB := t.TempDir()
	pB, err := e.AddProject("beta", dirB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.SwitchActiveProject(pB.ID); err != nil {
		t.Fatal(err)
	}
	emitEvent(t, e, "beta", filepath.Join(dirB, "b.go"), "func B()")
	_ = e.Store().Close()
	e.Close()

	e2 := NewEngine(Config{Store: openMigrated(t, globalPath)})
	t.Cleanup(e2.Close)
	t.Cleanup(func() { _ = e2.Store().Close() })
	if got := e2.Store().Path(); !samePath(got, pB.DBPath) {
		t.Fatalf("restarted engine reads %s; want beta's %s", got, pB.DBPath)
	}
	if m, _ := e2.Metrics(); m.Overview.TotalEvents != 1 {
		t.Fatalf("beta after restart shows %d events; want 1", m.Overview.TotalEvents)
	}
}

// An installation whose active project got an empty database of its own
// while its events went to the shared store keeps seeing those events.
func TestRestartAdoptsStoreHoldingTheHistory(t *testing.T) {
	storeTestHome(t)
	e := NewEngine(Config{}) // no open store: the project gets its own database
	dirA := t.TempDir()
	pA, err := e.AddProject("alpha", dirA)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()

	globalPath := filepath.Join(t.TempDir(), "global.db")
	global := openMigrated(t, globalPath)
	if err := global.InsertEvent(db.EventRecord{EventID: "ev1", RepoName: "alpha", FilePath: filepath.Join(dirA, "a.go"),
		Signature: "func A()", NodeType: "function", Action: "ADDED", OccurredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(Config{Store: global})
	t.Cleanup(e2.Close)
	if e2.Store() != global {
		t.Fatal("history-holding store was replaced by alpha's empty database")
	}
	if active := e2.GetActiveProject(); active == nil || active.ID != pA.ID || !samePath(active.DBPath, globalPath) {
		t.Fatalf("active project = %+v; want alpha pointed at %s", active, globalPath)
	}
	if m, _ := e2.Metrics(); m.Overview.TotalEvents != 1 {
		t.Fatalf("alpha shows %d events; want 1", m.Overview.TotalEvents)
	}
}
