package core

import (
	"fmt"
	"github.com/wrongstack/wrongtrace/internal/db"
	"os"
	"path/filepath"
	"testing"
)

func TestAddProjectRejectsStorageInitializationFailures(t *testing.T) {
	t.Run("healthy control", func(t *testing.T) {
		withFakeHome(t)
		t.Setenv("APPDATA", t.TempDir())
		t.Setenv("LOCALAPPDATA", t.TempDir())
		e := &Engine{}
		p, err := e.AddProject("healthy", t.TempDir())
		if err != nil || p.ID == "" || len(e.ListProjects()) != 1 {
			t.Fatal("healthy control")
		}
		if _, err = os.Stat(p.DBPath); err != nil {
			t.Fatal("healthy database missing")
		}
		fmt.Println("CONTROL EXPECTED: healthy project with database admitted | ACTUAL: true")
	})
	failed := false
	for _, mode := range []string{"directory", "open", "migrate"} {
		t.Run(mode, func(t *testing.T) {
			_, home := withFakeHome(t)
			t.Setenv("APPDATA", t.TempDir())
			t.Setenv("LOCALAPPDATA", t.TempDir())
			workspace := t.TempDir()
			e := &Engine{}
			if mode == "directory" {
				block := filepath.Join(t.TempDir(), "ordinary-file")
				if err := os.WriteFile(block, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("WRONGTRACE_HOME", block)
			} else {
				path := filepath.Join(home, "projects", mode, "wrongtrace.db")
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if mode == "open" {
					if err := os.WriteFile(path, []byte("not a sqlite database"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					s, err := db.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					_, err = s.DB().Exec("CREATE TABLE code_node_events(event_id TEXT)")
					if err != nil {
						t.Fatal(err)
					}
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			p, err := e.AddProject(mode, workspace)
			n := len(e.ListProjects())
			fmt.Printf("%s EXPECTED: error/no project admission | ACTUAL: err%v projects%d returnedID%q\n", mode, err, n, p.ID)
			if err == nil || n != 0 || p.ID != "" {
				failed = true
			}
			if mode == "migrate" && err != nil {
				path := filepath.Join(home, "projects", mode, "wrongtrace.db")
				if removeErr := os.Remove(path); removeErr != nil {
					t.Fatal("failed migration retained an open fixture handle:", removeErr)
				}
				if recovered, retryErr := e.AddProject(mode, workspace); retryErr != nil || recovered.ID == "" || len(e.ListProjects()) != 1 {
					t.Fatal("retry after replacing owned bad database failed:", retryErr)
				}
			}
		})
	}
	t.Run("invalid root then retry", func(t *testing.T) {
		withFakeHome(t)
		t.Setenv("APPDATA", t.TempDir())
		t.Setenv("LOCALAPPDATA", t.TempDir())
		e := &Engine{}
		if _, err := e.AddProject("missing", filepath.Join(t.TempDir(), "absent")); err == nil || len(e.ListProjects()) != 0 {
			t.Fatal("invalid root mutated registry")
		}
		root := t.TempDir()
		p, err := e.AddProject("", root)
		if err != nil || p.Name != filepath.Base(root) || len(e.ListProjects()) != 1 {
			t.Fatal("valid retry/default name failed")
		}
	})
	t.Run("existing registry retained", func(t *testing.T) {
		withFakeHome(t)
		t.Setenv("APPDATA", t.TempDir())
		t.Setenv("LOCALAPPDATA", t.TempDir())
		block := filepath.Join(t.TempDir(), "ordinary-file")
		if err := os.WriteFile(block, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WRONGTRACE_HOME", block)
		e := &Engine{projects: map[string]ProjectProfile{"existing": {ID: "existing", Name: "Keep", DiscoveredSessions: map[string]int{"agent": 2}}}}
		if _, err := e.AddProject("blocked", t.TempDir()); err == nil {
			t.Fatal("storage error hidden")
		}
		if len(e.projects) != 1 || e.projects["existing"].Name != "Keep" || e.projects["existing"].DiscoveredSessions["agent"] != 2 {
			t.Fatal("failed admission altered existing registry")
		}
	})
	if failed {
		t.Fatal("PROBLEM CONFIRMED")
	}
	if t.Failed() {
		return
	}
}
