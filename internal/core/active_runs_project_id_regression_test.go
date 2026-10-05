package core

import (
	"testing"

	"github.com/wrongstack/wrongtrace/internal/ipc"
)

// Explicit project IDs take precedence over display labels; slug-only reports
// keep their legacy fallback and cannot override a foreign explicit ID.
func TestActiveRuns_ProjectIDPrecedesDisplaySlug(t *testing.T) {
	e, _ := newTestEngine(t)
	t.Cleanup(e.Close)
	e.projects = map[string]ProjectProfile{"proj-active": {ID: "proj-active", Name: "My Project", IsActive: true}}
	e.activeProjectID = "proj-active"
	for _, tc := range []struct {
		name, id, slug string
		visible        bool
	}{
		{"matching-name", "proj-active", "My Project", true},
		{"normalized-slug", "proj-active", "my-project", true},
		{"renamed-display", "proj-active", "Old Name", true},
		{"foreign-id-matching-slug", "proj-foreign", "My Project", false},
		{"foreign-id-no-slug", "proj-foreign", "", false},
		{"legacy-name", "", "MY PROJECT", true},
		{"legacy-id", "", "proj-active", true},
		{"foreign-legacy", "", "Other Project", false},
		{"unscoped", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := e.ReportRun(ipc.TelemetryReport{RunID: tc.name, ProjectID: tc.id, ProjectSlug: tc.slug}); err != nil {
				t.Fatal(err)
			}
			visible := false
			for _, run := range e.ActiveRuns() {
				if run.RunID == tc.name {
					visible = true
				}
			}
			if visible != tc.visible {
				t.Fatalf("visible=%t, want %t", visible, tc.visible)
			}
		})
	}
}
