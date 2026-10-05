package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

func TestUnresolvedProjectReferenceStaysScoped(t *testing.T) {
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	core.SaveProjectsIndex(map[string]core.ProjectProfile{"known-id": {ID: "known-id", Name: "known-repo"}})
	store, err := db.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"known-repo", "other-repo"} {
		if err = store.InsertEvent(db.EventRecord{EventID: repo, RepoName: repo, FilePath: "a.go", Signature: "function:a.go::Work", Action: "ADDED", NodeType: "function", BodyHash: "fixture", OccurredAt: time.Unix(1700000000, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	engine := core.NewEngine(core.Config{Store: store})
	defer engine.Close()
	h := Handlers{Engine: engine}
	for _, c := range []struct {
		query string
		count int
		repo  string
	}{{"?project_id=missing-id", 0, ""}, {"?project_id=known-id", 1, "known-repo"}, {"?project_id=known-id&repo=other-repo", 1, "other-repo"}, {"", 2, ""}} {
		w := httptest.NewRecorder()
		h.RecentEvents(w, httptest.NewRequest("GET", "/api/events/recent"+c.query, nil))
		var rows []db.EventRecord
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(rows) != c.count {
			t.Fatalf("query%s status%d count%d want%d", c.query, w.Code, len(rows), c.count)
		}
		if c.repo != "" && rows[0].RepoName != c.repo {
			t.Fatal("query returned another repository")
		}
	}
}
