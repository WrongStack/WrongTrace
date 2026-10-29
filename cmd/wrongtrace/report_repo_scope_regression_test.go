package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// The database is shared by every project: `report --repo alpha` must count
// only alpha, and a report without --repo must say it covers all of them
// rather than wearing the current directory's name over everyone's data.
func TestReport_RepoScope(t *testing.T) {
	tmp := t.TempDir()
	for _, k := range []string{"WRONGTRACE_HOME", "HOME", "USERPROFILE", "APPDATA"} {
		t.Setenv(k, tmp)
	}
	dbPath := filepath.Join(tmp, "wt.db")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.UpsertRun(db.RunRecord{RunID: "run-a", AgentName: "claude", ModelName: "model-alpha", CostUSD: 1.0, CreatedAt: now}))
	must(store.UpsertRun(db.RunRecord{RunID: "run-b", AgentName: "claude", ModelName: "model-beta", CostUSD: 9.0, CreatedAt: now}))
	must(store.InsertEvent(db.EventRecord{EventID: "ea1", RunID: "run-a", RepoName: "alpha", FilePath: "a.go", Signature: "func:A", NodeType: "function", Action: "ADDED", BodyHash: "h", LOC: 3, OccurredAt: now}))
	for i := 0; i < 4; i++ {
		must(store.InsertEvent(db.EventRecord{EventID: fmt.Sprintf("eb%d", i), RunID: "run-b", RepoName: "beta", FilePath: "secret_beta.go", Signature: "func:BetaOnly", NodeType: "function", Action: "MODIFIED", BodyHash: fmt.Sprintf("h%d", i), LOC: 3, OccurredAt: now.Add(-time.Duration(i) * time.Minute)}))
	}
	// CONTROL: the store itself scopes correctly when given the repo.
	ctrl, err := store.Overview("alpha")
	must(err)
	all, err := store.Overview()
	must(err)
	store.Close()
	t.Logf("CONTROL store.Overview(\"alpha\") = %+v ; store.Overview() = %+v", ctrl, all)
	if ctrl.TotalRuns != 1 || ctrl.TotalEvents != 1 || all.TotalEvents != 5 {
		t.Fatalf("invalid control: %+v / %+v", ctrl, all)
	}

	repoFlag := rootCmd.PersistentFlags().Lookup("repo")
	resetRepo := func() { _ = repoFlag.Value.Set(repoFlag.DefValue); repoFlag.Changed = false }
	resetRepo()
	t.Cleanup(resetRepo)

	type snapshot struct {
		Repo     string `json:"repo"`
		Overview struct {
			TotalRuns   int
			TotalEvents int
		} `json:"overview"`
		Thrashing []struct {
			FilePath string `json:"file_path"`
		} `json:"thrashing"`
		RecentEvents []struct {
			RepoName string `json:"repo_name"`
		} `json:"recent_events"`
	}
	report := func(args ...string) snapshot {
		t.Helper()
		out := filepath.Join(t.TempDir(), "report.json")
		rootCmd.SetArgs(append([]string{"report", "--db", dbPath, "--format", "json", "--out", out}, args...))
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("report %v: %v", args, err)
		}
		raw, err := os.ReadFile(out)
		must(err)
		var rep struct{ Snapshot snapshot }
		must(json.Unmarshal(raw, &rep))
		return rep.Snapshot
	}

	s := report("--repo", "alpha")
	if s.Repo != "alpha" || s.Overview.TotalRuns != ctrl.TotalRuns || s.Overview.TotalEvents != ctrl.TotalEvents || len(s.Thrashing) != 0 {
		t.Fatalf("--repo alpha: %+v; want alpha's figures only (%+v)", s, ctrl)
	}
	for _, e := range s.RecentEvents {
		if e.RepoName != "alpha" {
			t.Fatalf("--repo alpha lists an event from %q", e.RepoName)
		}
	}

	resetRepo()
	s = report()
	if s.Repo != "all repositories" || s.Overview.TotalEvents != all.TotalEvents {
		t.Fatalf("no --repo: repo=%q events=%d; want \"all repositories\" with %d events", s.Repo, s.Overview.TotalEvents, all.TotalEvents)
	}
}
