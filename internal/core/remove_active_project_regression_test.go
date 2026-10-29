package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
)

func runRowsIn(t *testing.T, dbPath, runID string) bool {
	t.Helper()
	st, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, _, _, ok, err := st.RunTotals(runID)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// F132: removing the ACTIVE project leaves the engine bound to the removed
// project's database and identity: no project is active any more, yet every
// write keeps landing in the deregistered project's wrongtrace.db (whose
// handle also stays open).
func TestRemovingActiveProjectHandsOver(t *testing.T) {
	e, _ := newTestEngine(t) // WRONGTRACE_HOME -> temp dir
	t.Cleanup(e.Close)
	a, err := e.AddProject("alpha", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.AddProject("beta", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.AddProject("gamma", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.SwitchActiveProject(a.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Store().Close() })

	// CONTROL: removing a NON-active project keeps a coherent active binding.
	if err := e.RemoveProject(c.ID); err != nil {
		t.Fatal(err)
	}
	ap := e.GetActiveProject()
	ctlOK := ap != nil && ap.ID == a.ID
	t.Logf("CONTROL  remove non-active: GetActiveProject=%v (want %s)", func() string {
		if ap == nil {
			return "<nil>"
		}
		return ap.ID
	}(), a.ID)
	if !ctlOK {
		t.Fatal("control broken")
	}

	// SUBJECT: remove the active project.
	if err := e.RemoveProject(a.ID); err != nil {
		t.Fatal(err)
	}
	active := e.GetActiveProject()
	anyActive := false
	for _, p := range e.ListProjects() {
		anyActive = anyActive || p.IsActive
	}
	if err := e.ReportRun(ipc.TelemetryReport{RunID: "after-remove", AgentName: "x"}); err != nil {
		t.Logf("ReportRun after remove: %v", err)
	}
	inRemoved := runRowsIn(t, a.DBPath, "after-remove")
	inRemaining := runRowsIn(t, b.DBPath, "after-remove")
	rmErr := os.Remove(filepath.Clean(a.DBPath))

	t.Logf("EXPECTED: after removing the active project, writes do NOT land in the removed project's DB; remaining project %s is active (or writes are refused)", b.ID)
	t.Logf("ACTUAL:   GetActiveProject=%v anyIsActive=%t Repo()=%q run row in REMOVED db=%t in remaining db=%t; delete removed db file err=%v",
		active, anyActive, e.Repo(), inRemoved, inRemaining, rmErr)
	if inRemoved && active == nil {
		t.Fatalf("telemetry keeps flowing into the deregistered project %s's database while no project is active", a.ID)
	}
}
