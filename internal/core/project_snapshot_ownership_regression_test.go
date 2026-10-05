package core

import (
	"path/filepath"
	"testing"
)

func TestProjectSnapshotOwnership(t *testing.T) {
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	getters := []struct {
		name string
		get  func(*Engine) ProjectProfile
	}{
		{"GetProject", func(e *Engine) ProjectProfile { p, _ := e.GetProject("p"); return p }},
		{"ListProjects", func(e *Engine) ProjectProfile { return e.ListProjects()[0] }},
		{"GetActiveProjectID", func(e *Engine) ProjectProfile { e.activeProjectID = "p"; return *e.GetActiveProject() }},
		{"GetActiveProjectName", func(e *Engine) ProjectProfile { e.cfg.RepoName = "fixture"; return *e.GetActiveProject() }},
		{"GetActiveProjectFlag", func(e *Engine) ProjectProfile { return *e.GetActiveProject() }},
		{"FindProjectForFile", func(e *Engine) ProjectProfile {
			p, _ := e.FindProjectForFile(filepath.Join(e.projects["p"].Path, "x.go"))
			return p
		}},
	}
	for _, getter := range getters {
		t.Run(getter.name, func(t *testing.T) {
			e := NewEngine(Config{})
			e.projects = map[string]ProjectProfile{"p": {ID: "p", Name: "fixture", Path: t.TempDir(), IsActive: true, DiscoveredSessions: map[string]int{"agent": 1}}}
			snapshot := getter.get(e)
			peer, _ := e.GetProject("p")
			gate, done := make(chan struct{}), make(chan struct{})
			go func() { <-gate; snapshot.DiscoveredSessions["agent"] = 99; close(done) }()
			e.Close()
			close(gate)
			<-done
			stored, _ := e.GetProject("p")
			if stored.DiscoveredSessions["agent"] != 1 || peer.DiscoveredSessions["agent"] != 1 {
				t.Fatal("snapshot mutation reached registry or another snapshot")
			}
		})
	}
	for _, counts := range []map[string]int{nil, {}} {
		p := cloneProjectProfile(ProjectProfile{DiscoveredSessions: counts})
		if (p.DiscoveredSessions == nil) != (counts == nil) {
			t.Fatal("nil/empty map shape changed")
		}
	}
	e := NewEngine(Config{})
	defer e.Close()
	if _, err := e.GetProject("missing"); err == nil {
		t.Fatal("missing project accepted")
	}
	if _, ok := e.FindProjectForFile(""); ok {
		t.Fatal("empty file matched")
	}
	if e.GetActiveProject() != nil {
		t.Fatal("empty registry has active project")
	}
}
