package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/core"
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

// fakeHome points the home directory at root/users/me and returns root, home.
func fakeHome(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "users", "me")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return root, home
}

func TestCoversUserHome(t *testing.T) {
	root, home := fakeHome(t)
	cases := map[string]bool{
		home:                         true,
		filepath.Join(root, "users"): true,
		root:                         true,
		filepath.VolumeName(root) + string(filepath.Separator): true,
		filepath.Join(home, "code"):                            false,
		filepath.Join(root, "users", "you"):                    false,
		filepath.Join(root, "proj"):                            false,
		"":                                                     false,
	}
	for dir, want := range cases {
		if got := coversUserHome(dir); got != want {
			t.Errorf("coversUserHome(%q) = %v, want %v (home %q)", dir, got, want, home)
		}
	}
}

// Started from home, the daemon watches the active project instead -- but
// never one whose path is missing or would walk the home tree anyway.
func TestHomeStartProjectDir(t *testing.T) {
	root, home := fakeHome(t)
	inHome := filepath.Join(home, "code", "app")
	outside := filepath.Join(root, "proj")
	for _, d := range []string{inHome, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		p    *core.ProjectProfile
		want string
	}{
		{"no active project", nil, ""},
		{"empty path", &core.ProjectProfile{Name: "x"}, ""},
		{"missing directory", &core.ProjectProfile{Name: "x", Path: filepath.Join(root, "gone")}, ""},
		{"home itself", &core.ProjectProfile{Name: "x", Path: home}, ""},
		{"ancestor of home", &core.ProjectProfile{Name: "x", Path: root}, ""},
		{"project under home", &core.ProjectProfile{Name: "app", Path: inHome}, inHome},
		{"project outside home", &core.ProjectProfile{Name: "proj", Path: outside}, outside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := homeStartProjectDir(tc.p); got != tc.want {
				t.Fatalf("homeStartProjectDir = %q, want %q", got, tc.want)
			}
		})
	}
}
