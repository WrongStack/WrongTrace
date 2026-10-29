package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

func goroutineInMigrate() bool {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Contains(string(buf[:n]), "db.(*Store).Migrate")
}

// F133: SwitchActiveProject reads the target under lockMu, releases it to
// open/migrate the store, then re-acquires it and writes the STALE target
// copy back with e.projects[id] = target. A RemoveProject(id) that lands in
// that window is undone: the deleted project is resurrected (and made active,
// and persisted to projects.json).
func TestSwitchDoesNotResurrectRemovedProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", home)
	base, err := db.Open(filepath.Join(t.TempDir(), "base.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Migrate(); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(Config{RepoName: "default", Store: base})
	t.Cleanup(e.Close)

	mk := func(id string) ProjectProfile {
		dir := filepath.Join(home, "projects", id)
		_ = os.MkdirAll(dir, 0o755)
		return ProjectProfile{ID: id, Name: id, Path: t.TempDir(), DBPath: filepath.Join(dir, "wrongtrace.db")}
	}
	e.lockMu.Lock()
	e.projects = map[string]ProjectProfile{"proj-ctl": mk("proj-ctl"), "proj-x": mk("proj-x"), "proj-keep": mk("proj-keep")}
	e.lockMu.Unlock()

	// CONTROL: sequential remove -> switch is refused.
	if err := e.RemoveProject("proj-ctl"); err != nil {
		t.Fatal(err)
	}
	_, ctlErr := e.SwitchActiveProject("proj-ctl")
	t.Logf("CONTROL  sequential Remove->Switch: err=%v", ctlErr)
	if !errors.Is(ctlErr, ErrProjectNotFound) {
		t.Fatal("control broken")
	}

	// Gate: hold the SQLite write lock on proj-x's DB so the switch parks
	// inside Migrate (busy_timeout 5s) after it has released lockMu.
	target := e.projects["proj-x"].DBPath
	gateDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(target)+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer gateDB.Close()
	ctx := context.Background()
	conn, err := gateDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	type res struct {
		p   *ProjectProfile
		err error
	}
	done := make(chan res, 1)
	go func() {
		p, err := e.SwitchActiveProject("proj-x")
		done <- res{p, err}
	}()
	deadline := time.Now().Add(4 * time.Second)
	for !goroutineInMigrate() {
		if time.Now().After(deadline) {
			t.Fatal("switch never reached Migrate")
		}
		runtime.Gosched()
	}
	// Switch is now past its first lockMu section and parked in Migrate.
	rmErr := e.RemoveProject("proj-x")
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
	_ = conn.Close()
	r := <-done
	t.Cleanup(func() {
		if st := e.Store(); st != nil {
			_ = st.Close()
		}
	})

	_, stillThere := func() (ProjectProfile, bool) {
		e.lockMu.RLock()
		defer e.lockMu.RUnlock()
		p, ok := e.projects["proj-x"]
		return p, ok
	}()
	onDisk := LoadProjectsIndex()
	_, persisted := onDisk["proj-x"]
	ap := e.GetActiveProject()
	apID := "<nil>"
	if ap != nil {
		apID = ap.ID
	}
	t.Logf("EXPECTED: RemoveProject(proj-x)=nil is durable: proj-x absent from registry and projects.json (switch fails with ErrProjectNotFound)")
	t.Logf("ACTUAL:   RemoveProject err=%v; Switch err=%v; proj-x in registry=%t, in projects.json=%t, active=%s",
		rmErr, r.err, stillThere, persisted, apID)
	if rmErr == nil && (stillThere || persisted) {
		t.Fatal("removed project resurrected by a concurrent SwitchActiveProject")
	}
}
