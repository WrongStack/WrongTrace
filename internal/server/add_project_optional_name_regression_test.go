package server

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/core"
)

func TestAddProjectOptionalNameUsesDirectoryName(t *testing.T) {
	for _, tc := range []struct {
		name       string
		include    bool
		value      interface{}
		wantStatus int
	}{
		{"explicit-empty-control", true, "", http.StatusOK},
		{"omitted-name", false, nil, http.StatusOK},
		{"explicit-null", true, nil, http.StatusOK},
		{"explicit-custom-control", true, "custom-name", http.StatusOK},
		{"wrong-type-control", true, 123, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withIsolatedProjectsHome(t)
			e, _, ts := newTestServer(t)
			t.Cleanup(e.Close)
			root := t.TempDir()
			body := map[string]interface{}{"path": root}
			if tc.include {
				body["name"] = tc.value
			}
			var got core.ProjectProfile
			resp := projReq(t, ts, http.MethodPost, "/api/projects", body, &got)
			t.Logf("EXPECTED: status=%d | ACTUAL: status=%d, name=%q", tc.wantStatus, resp.StatusCode, got.Name)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("optional project name was treated as malformed JSON")
			}
			if tc.wantStatus != http.StatusOK {
				if len(e.ListProjects()) != 0 {
					t.Fatal("invalid name created a project")
				}
				return
			}
			want := filepath.Base(root)
			if tc.value == "custom-name" {
				want = "custom-name"
			}
			if got.Name != want || got.Path != root || got.ID == "" {
				t.Fatalf("project did not use the expected name/path: %+v", got)
			}
		})
	}
}
