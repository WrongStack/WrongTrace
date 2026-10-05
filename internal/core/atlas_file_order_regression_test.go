package core

// Regression (r2-atlasorder): Engine.Atlas documents "Sort packages and files
// deterministically" but sorted a package's FILES by basename alone. A basename
// is not a unique key inside a package: resolvePackageScope collapses every
// web/frontend/client/ui path into the single package "web", so
// web/a/index.ts and web/b/index.ts both carry Name "index.ts". Ties kept the
// append order, and that order comes from SnapshotList's map iteration, so it is
// re-randomised on every call — the cached payload's file order changed from one
// rebuild to the next (proven with a go-run probe: three different orderings in
// three calls). The sort now falls back to Path, which is unique per file.

import (
	"fmt"
	"testing"
)

func TestAtlas_FileOrderIsDeterministicForSameBasename(t *testing.T) {
	e, _, _ := newAtlasTestEngine(t)
	dir := t.TempDir()

	// Three files share the basename "index.ts"; the web-collapse puts them in
	// one package. unique.ts is a control that must still be ordered last.
	writeFixture(t, dir, "web/aaa/index.ts", "export function a(): number {\n  return 1;\n}\n")
	writeFixture(t, dir, "web/mmm/index.ts", "export function m(): number {\n  return 2;\n}\n")
	writeFixture(t, dir, "web/zzz/index.ts", "export function z(): number {\n  return 3;\n}\n")
	writeFixture(t, dir, "web/only/unique.ts", "export function u(): number {\n  return 4;\n}\n")

	// One ACTIVE project rooted at dir, so Atlas takes the root-relative path
	// branch where the web-collapse applies (the default branch keys packages
	// by their real two-level directory, which cannot contain duplicate
	// basenames).
	e.lockMu.Lock()
	e.projects = map[string]ProjectProfile{
		"p1": {ID: "p1", Name: "atlas-det", Path: dir, IsActive: true},
	}
	e.activeProjectID = "p1"
	e.lockMu.Unlock()

	e.PrimeDirectory(dir)

	// Sorted by Name, then Path. Ordering the three tied index.ts by path is
	// exactly what a total order buys: a stable, assertable sequence.
	want := []string{"web/aaa/index.ts", "web/mmm/index.ts", "web/zzz/index.ts", "web/only/unique.ts"}

	assertOrder := func(label string) {
		t.Helper()
		snap, err := e.Atlas()
		if err != nil {
			t.Fatalf("%s: atlas: %v", label, err)
		}
		var got []string
		for _, pkg := range snap.Packages {
			if pkg.Path != "web" {
				continue
			}
			for _, f := range pkg.Files {
				got = append(got, f.Path)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("%s: package web files = %v, want %v", label, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: file order = %v, want %v", label, got, want)
			}
		}
	}

	assertOrder("first build")
	// Force a real rebuild each time: the atlas cache is keyed by generation,
	// and the defect only showed once SnapshotList's map iteration re-ran.
	for i := 0; i < 20; i++ {
		e.BumpCacheGen()
		assertOrder(fmt.Sprintf("rebuild %d", i))
	}
}
