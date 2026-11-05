package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Running `wrongtrace` from a fresh terminal (cwd = home) made the daemon walk
// and parse the whole home tree before the HTTP server started, so the
// dashboard never came up. runStart skips the scan when isUserHome is true.
func TestIsUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"home itself", home, true},
		{"trailing separator", home + string(filepath.Separator), true},
		{"project under home", filepath.Join(home, "code", "proj"), false},
		{"parent of home", filepath.Dir(home), false},
		{"empty", "", false},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases, struct {
			name string
			dir  string
			want bool
		}{"different case on windows", strings.ToUpper(home), true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUserHome(tc.dir); got != tc.want {
				t.Fatalf("isUserHome(%q) = %v, want %v (home %q)", tc.dir, got, tc.want, home)
			}
		})
	}
}
