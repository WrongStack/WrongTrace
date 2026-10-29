package watcher

import (
	"os"
	"path/filepath"
	"testing"
)

// A project added with AddWatchDir is scoped to its own root: an ANCESTOR
// named like a default ignore dir (build, out, tmp, ...) must not hide it,
// while the same names INSIDE it are still ignored. Removing the dir drops
// the scoping again.
func TestAddWatchDir_ScopesIgnoreRulesToItsRoot(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "a")
	projB := filepath.Join(base, "build", "svc")
	for _, d := range []string{rootA, filepath.Join(projB, "pkg"), filepath.Join(projB, "node_modules", "x")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w, err := New(Config{Dir: rootA})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if !w.pathIgnored(projB) {
		t.Fatal("before AddWatchDir the build/ ancestor should hide projB (control)")
	}
	if err := w.AddWatchDir(projB); err != nil {
		t.Fatal(err)
	}
	if w.pathIgnored(projB) || w.pathIgnored(filepath.Join(projB, "pkg", "b.go")) {
		t.Fatal("projB or its files ignored because of an ancestor directory name")
	}
	if !w.pathIgnored(filepath.Join(projB, "node_modules", "x")) {
		t.Fatal("node_modules inside projB is no longer ignored")
	}
	if err := w.RemoveWatchDir(projB); err != nil {
		t.Fatal(err)
	}
	if !w.pathIgnored(projB) {
		t.Fatal("RemoveWatchDir left projB scoped as a root")
	}
}
