package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// Regression: resolvePackageScope is written for PROJECT-RELATIVE paths -- its
// internal/, cmd/, packages/ and web/ conventions all assume a repo root -- but
// Atlas() falls back to the stored ABSOLUTE path whenever no registered project
// contains the file ("relPath := cleanPath", atlas.go). On an absolute path the
// leading segment is a filesystem root or a volume marker, and it was used as
// the package scope: every directory on a drive collapsed into a single package
// keyed "C:/Users" with workspace "C:" (POSIX: "/home", workspace ""). Distinct
// packages such as internal/database, web/routes and cmd/server were silently
// merged, with no error to show it.

func TestAtlas_AbsolutePathsDoNotCollapseIntoVolumePackage(t *testing.T) {
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	root := t.TempDir()

	store, err := db.Open(filepath.Join(root, "atlas-abs.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	astEng, err := ast.NewEngine()
	if err != nil {
		t.Fatalf("ast.NewEngine: %v", err)
	}
	defer astEng.Close()

	dirs := []string{"internal/database", "web/routes", "cmd/server"}
	for _, d := range dirs {
		full := filepath.Join(root, filepath.FromSlash(d))
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		src := "package " + filepath.Base(d) + "\n\nfunc Handle() int {\n\treturn 1\n}\n"
		if err := os.WriteFile(filepath.Join(full, "x.go"), []byte(src), 0o644); err != nil {
			t.Fatalf("write x.go: %v", err)
		}
	}

	// No AddProject at all, so Atlas has no project path to make the stored
	// paths relative to: this is the absolute-path condition under test.
	e := NewEngine(Config{RepoName: "abs-atlas", Store: store, AST: astEng})
	e.PrimeDirectory(root)

	snap, err := e.Atlas()
	if err != nil {
		t.Fatalf("Atlas(): %v", err)
	}
	if snap.TotalFiles < len(dirs) {
		t.Fatalf("primed %d files, want >=%d; the path under test was not exercised",
			snap.TotalFiles, len(dirs))
	}

	distinct := map[string]bool{}
	for _, p := range snap.Packages {
		if p.Workspace == "" {
			t.Errorf("package %q has an empty Workspace: a filesystem root leaked into the scope", p.Path)
		}
		if strings.HasSuffix(p.Workspace, ":") {
			t.Errorf("package %q has Workspace %q: a bare volume marker, not a directory", p.Path, p.Workspace)
		}
		if strings.HasPrefix(p.Path, "/") {
			t.Errorf("package path %q keeps a filesystem-root prefix", p.Path)
		}
		if len(p.Path) >= 2 && p.Path[1] == ':' {
			t.Errorf("package path %q is keyed on a drive letter", p.Path)
		}
		distinct[p.Path] = true
	}

	// The core invariant: three different source directories are three packages.
	if len(distinct) < len(dirs) {
		t.Errorf("distinct packages = %d, want >=%d; absolute paths merged distinct directories: %v",
			len(distinct), len(dirs), atlasScopeKeys(distinct))
	}
}

// TestResolvePackageScope_AbsoluteInputs pins the same contract at unit level
// for both absolute shapes, independent of the host OS. The argument is the
// FILE path, exactly as Atlas passes it, because the function strips the file
// name itself.
func TestResolvePackageScope_AbsoluteInputs(t *testing.T) {
	for _, in := range []string{
		"/home/dev/proj/internal/database/store.go",
		"C:/Users/dev/proj/internal/database/store.go",
		"c:/Users/dev/proj/web/routes/app.tsx",
	} {
		pkg, name, ws := resolvePackageScope(filepath.ToSlash(in))
		if ws == "" {
			t.Errorf("%q: empty workspace", in)
		}
		if strings.HasSuffix(ws, ":") {
			t.Errorf("%q: workspace %q is a volume marker", in, ws)
		}
		if strings.HasPrefix(pkg, "/") || (len(pkg) >= 2 && pkg[1] == ':') {
			t.Errorf("%q: package %q is rooted at the filesystem/volume", in, pkg)
		}
		if name == "" || name == "Users" || name == "home" {
			t.Errorf("%q: package name %q came from the path prefix, not a directory", in, name)
		}
		if pkg == "root" {
			t.Errorf("%q: resolved to root; the directory structure was discarded", in)
		}
	}

	// The absolute POSIX input must land on the real deepest directories.
	if pkg, name, ws := resolvePackageScope("/home/dev/proj/internal/database/store.go"); pkg != "internal/database" || name != "database" || ws != "internal" {
		t.Errorf("absolute posix scope = (%q,%q,%q), want (internal/database,database,internal)", pkg, name, ws)
	}

	// Relative inputs keep the documented conventions untouched.
	if pkg, _, ws := resolvePackageScope("internal/database/store.go"); pkg != "internal/database" || ws != "internal" {
		t.Errorf("relative internal/ path regressed: pkg=%q ws=%q", pkg, ws)
	}
	if pkg, _, _ := resolvePackageScope("packages/ui/kit/button.tsx"); pkg != "packages/ui" {
		t.Errorf("monorepo container regressed: pkg=%q", pkg)
	}
	if pkg, _, _ := resolvePackageScope("web/src/components/App.tsx"); pkg != "web" {
		t.Errorf("web grouping regressed: pkg=%q", pkg)
	}
}

func atlasScopeKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
