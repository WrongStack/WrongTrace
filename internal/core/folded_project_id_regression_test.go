package core

import "testing"

func TestResolveProjectRefFoldedIDsAreDeterministic(t *testing.T) {
	projects := map[string]ProjectProfile{
		"ALPHA":     {ID: "ALPHA", Name: "Shared"},
		"Alpha":     {ID: "Alpha", Name: "Shared", IsActive: true},
		"name-only": {ID: "name-only", Name: "alpha", IsActive: true},
	}
	for i := 0; i < 64; i++ {
		for _, tc := range []struct{ ref, prefer, want string }{
			{"alpha", "", "ALPHA"}, {"alpha", "Alpha", "ALPHA"},
			{"Alpha", "", "Alpha"}, {"ALPHA", "", "ALPHA"},
			{"Shared", "Alpha", "Alpha"}, {"Shared", "", "Alpha"},
		} {
			p, ok := resolveProjectRef(projects, tc.ref, tc.prefer)
			if !ok || p.ID != tc.want {
				t.Fatalf("resolve(%q,%q)=%q, want%q", tc.ref, tc.prefer, p.ID, tc.want)
			}
		}
	}
}
