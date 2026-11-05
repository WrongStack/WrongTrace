package watcher

import (
	"os"
	"path/filepath"
	"testing"
)

// The daemon passes an empty Dir when it is started from the user's home
// directory, which must not be walked. New must then register nothing, and a
// project added later through AddWatchDir must still be observed.
func TestNew_EmptyDirWatchesNothingUntilAdded(t *testing.T) {
	w, err := New(Config{Dir: ""})
	if err != nil {
		t.Fatalf("New with empty Dir: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if got := w.fs.WatchList(); len(got) != 0 {
		t.Fatalf("empty Dir registered %d watches, want 0: %v", len(got), got)
	}

	proj := t.TempDir()
	sub := filepath.Join(proj, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.AddWatchDir(proj); err != nil {
		t.Fatalf("AddWatchDir: %v", err)
	}
	watched := map[string]bool{}
	for _, p := range w.fs.WatchList() {
		watched[filepath.Clean(p)] = true
	}
	for _, want := range []string{proj, sub} {
		if !watched[filepath.Clean(want)] {
			t.Fatalf("AddWatchDir did not register %s; watch list %v", want, w.fs.WatchList())
		}
	}
}
