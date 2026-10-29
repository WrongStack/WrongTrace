package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/ipc"
)

// F131: the "single_active_run" fallback attributes an edit in project A to a
// run that explicitly reported ProjectID = project B.
func TestSingleActiveRunStaysInItsProject(t *testing.T) {
	e, store := newTestEngine(t) // sets WRONGTRACE_HOME to a temp dir
	t.Cleanup(e.Close)
	dirA, dirB := t.TempDir(), t.TempDir()
	e.lockMu.Lock()
	e.projects = map[string]ProjectProfile{
		"proj-a": {ID: "proj-a", Name: "alpha", Path: dirA, IsActive: true},
		"proj-b": {ID: "proj-b", Name: "beta", Path: dirB},
	}
	e.activeProjectID = "proj-a"
	e.lockMu.Unlock()

	// The only active run is explicitly scoped to project B.
	if err := e.ReportRun(ipc.TelemetryReport{RunID: "run-b", ProjectID: "proj-b", AgentName: "agentB"}); err != nil {
		t.Fatal(err)
	}
	visible := len(e.ActiveRuns())
	t.Logf("ActiveRuns() visible for active project A: %d (run-b is filtered as foreign)", visible)

	emit := func(file string) {
		e.persistAndBroadcast(ast.DiffResult{FilePath: file, Events: []ast.Event{{
			FilePath: file, Signature: "func F()", NodeType: "function",
			Action: ast.ActionAdded, OccurredAt: time.Now().UTC(),
		}}})
	}
	fileA := filepath.Join(dirA, "a.go")
	fileB := filepath.Join(dirB, "b.go")
	_ = os.WriteFile(fileA, []byte("package a\n"), 0o644)
	_ = os.WriteFile(fileB, []byte("package b\n"), 0o644)

	emit(fileB) // CONTROL: edit inside the run's own project
	emit(fileA) // SUBJECT: edit inside a different project

	recs, err := store.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, r := range recs {
		got[r.FilePath] = [2]string{r.RunID, r.AttributionSource}
	}
	ctl, subj := got[fileB], got[fileA]
	t.Logf("CONTROL  (project B file): run_id=%q source=%q", ctl[0], ctl[1])
	t.Logf("EXPECTED: project A file run_id=\"\" source=\"unknown\" (the only active run belongs to project B)")
	t.Logf("ACTUAL:   project A file run_id=%q source=%q", subj[0], subj[1])
	if ctl[0] != "run-b" {
		t.Fatalf("control broken: project-B edit not attributed to run-b: %+v", ctl)
	}
	if subj[0] == "run-b" {
		t.Fatalf("edit in project A attributed to run-b, which reported project_id=proj-b")
	}
}
