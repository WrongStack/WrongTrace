package core

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSwitchActiveProject_StoreFailureAbortsSwitch pins the round-41 contract:
// a project switch whose dedicated SQLite store cannot be opened or migrated
// must ABORT with an error and leave identity, registry, persisted index, and
// the previous database untouched. The pre-fix implementation flipped IsActive
// across the registry, cfg.RepoName, and projects.json BEFORE opening the
// store, then logged "active database not switched" and returned success — a
// split brain in which the dashboard showed the new project active while every
// subsequent event kept landing in the previous project's database.
//
// Round-29 Windows lesson applied: the engine holds a switched-in SQLite store
// after the control switch, so cleanup closes it explicitly or TempDir
// teardown fails with a file lock.
func TestSwitchActiveProject_StoreFailureAbortsSwitch(t *testing.T) {
	oldHome, hadHome := os.LookupEnv("WRONGTRACE_HOME")
	home := t.TempDir()
	if err := os.Setenv("WRONGTRACE_HOME", home); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() {
		if hadHome {
			_ = os.Setenv("WRONGTRACE_HOME", oldHome)
		} else {
			_ = os.Unsetenv("WRONGTRACE_HOME")
		}
	})

	engine := NewEngine(Config{})

	dirA := filepath.Join(home, "ws-alpha")
	dirB := filepath.Join(home, "ws-beta")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir workspace: %v", err)
		}
	}

	pa, err := engine.AddProject("alpha", dirA)
	if err != nil {
		t.Fatalf("AddProject alpha: %v", err)
	}
	pb, err := engine.AddProject("beta", dirB)
	if err != nil {
		t.Fatalf("AddProject beta: %v", err)
	}

	// Control: switching to a healthy store succeeds and commits identity.
	if _, err := engine.SwitchActiveProject(pb.ID); err != nil {
		t.Fatalf("control switch to healthy beta failed: %v", err)
	}
	if active := engine.GetActiveProject(); active == nil || active.ID != pb.ID {
		t.Fatalf("control: active project is not beta after a healthy switch")
	}

	// Sabotage alpha's dedicated store: a DIRECTORY where the DB file belongs
	// (sqlite CANTOPEN — the same deterministic unopenable-store shape the
	// round-36 fixture work established).
	alphaDB := pa.DBPath
	if alphaDB == "" {
		t.Fatalf("alpha registered without a DBPath")
	}
	if err := os.Remove(alphaDB); err != nil {
		t.Fatalf("sabotage remove: %v", err)
	}
	if err := os.MkdirAll(alphaDB, 0o755); err != nil {
		t.Fatalf("sabotage mkdir: %v", err)
	}

	// The switch to the sabotaged store must abort with an error...
	if _, err := engine.SwitchActiveProject(pa.ID); err == nil {
		t.Fatal("switch to an unopenable store reported success")
	}

	// ...and identity must be untouched: beta remains the active project.
	active := engine.GetActiveProject()
	if active == nil || active.ID != pb.ID {
		got := "<nil>"
		if active != nil {
			got = active.ID
		}
		t.Errorf("after the aborted switch the active project is %s, want beta (%s)", got, pb.ID)
	}

	// Registry flags untouched.
	for _, p := range engine.ListProjects() {
		wantActive := p.ID == pb.ID
		if p.IsActive != wantActive {
			t.Errorf("registry: project %s active=%v, want %v", p.Name, p.IsActive, wantActive)
		}
	}

	// The persisted index must not have recorded the aborted switch (a daemon
	// restart would otherwise resurrect the aborted identity).
	index := LoadProjectsIndex()
	if p, ok := index[pa.ID]; ok && p.IsActive {
		t.Errorf("persisted index marked the sabotaged project active from an aborted switch")
	}
	if p, ok := index[pb.ID]; !ok || !p.IsActive {
		t.Errorf("persisted index lost the active beta project")
	}

	// Windows file-lock lesson: close the switched-in store before the temp
	// registry directory is torn down.
	if st := engine.Store(); st != nil {
		_ = st.Close()
	}
}
