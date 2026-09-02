package core

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAddProject_NameCollisionsGetDistinctStorage pins the storage-isolation
// fix: distinct workspaces whose names sanitize to the same storage slug
// ("api", "API", "  API  ", two checkouts of one repo name) must never share
// one wrongtrace.db — a shared file silently merges their telemetry and
// violates the dedicated/isolated-storage contract documented on
// ProjectProfile and GetProjectStorageDir.
func TestAddProject_NameCollisionsGetDistinctStorage(t *testing.T) {
	tempBase := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", tempBase)

	mkWorkspace := func(name string) string {
		dir := filepath.Join(tempBase, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create workspace %s: %v", name, err)
		}
		return dir
	}
	dirA := mkWorkspace("checkout-a")
	dirB := mkWorkspace("checkout-b")
	dirC := mkWorkspace("checkout-c")

	e := &Engine{}

	// A first add on a fresh home keeps the canonical, unsuffixed slug.
	p1, err := e.AddProject("api", dirA)
	if err != nil {
		t.Fatalf("add project 1: %v", err)
	}
	if want := filepath.Join(UserProjectsDir(), "api", "wrongtrace.db"); p1.DBPath != want {
		t.Fatalf("first project should keep the canonical slug path %s, got %s", want, p1.DBPath)
	}

	// Same display name, different checkout.
	p2, err := e.AddProject("api", dirB)
	if err != nil {
		t.Fatalf("add project 2: %v", err)
	}

	// Case and spacing variants collapse to the same base slug.
	p3, err := e.AddProject("  API  ", dirC)
	if err != nil {
		t.Fatalf("add project 3: %v", err)
	}

	projects := []ProjectProfile{p1, p2, p3}
	dirs := make(map[string]string, len(projects))
	for i, p := range projects {
		if p.ID == "" {
			t.Fatalf("project %d got an empty ID", i+1)
		}
		if _, err := os.Stat(p.DBPath); err != nil {
			t.Fatalf("project %d db missing: %v", i+1, err)
		}
		d := filepath.Dir(p.DBPath)
		if prev, dup := dirs[d]; dup {
			t.Fatalf("projects %s and %s share one storage folder %s", prev, p.ID, d)
		}
		dirs[d] = p.ID
	}

	// The persisted index keeps every disambiguated database path: restarts
	// restore DBPath verbatim, so a lost path would re-merge the projects.
	saved := LoadProjectsIndex()
	if len(saved) != len(projects) {
		t.Fatalf("projects.json holds %d projects, want %d", len(saved), len(projects))
	}
	for i, p := range projects {
		if got := saved[p.ID].DBPath; got != p.DBPath {
			t.Fatalf("projects.json lost the db path of project %d: got %s, want %s", i+1, got, p.DBPath)
		}
	}
}
