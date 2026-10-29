package core

import (
	"path/filepath"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// A failed health scan must surface as an error, not as an atlas in which
// every file is healthy (score 100, never fragile) and which then gets cached.
func TestAtlas_StoreErrorIsNotAHealthySnapshot(t *testing.T) {
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	store, err := db.Open(filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(Config{RepoName: "atlas-err", Store: store})

	// Control: a healthy store builds a snapshot.
	if _, err := e.Atlas("atlas-err"); err != nil {
		t.Fatalf("Atlas on a healthy store: %v", err)
	}

	_ = store.Close() // every query now fails
	e.BumpCacheGen()  // and the control snapshot is no longer current
	if _, err := e.Atlas("atlas-err"); err == nil {
		t.Fatal("Atlas returned a snapshot although the health scan failed")
	}
}
