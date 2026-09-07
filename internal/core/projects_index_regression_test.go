package core

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression (round 42): a corrupt projects.json must be quarantined out of
// the live path by LoadProjectsIndex instead of being silently swallowed.
// The empty-registry return is correct for startup, but bytes left in place
// were erased without any signal by the next SaveProjectsIndex.
func TestLoadProjectsIndex_QuarantinesCorruptIndex(t *testing.T) {
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	indexPath := filepath.Join(UserWrongTraceDir(), "projects.json")

	seed := map[string]ProjectProfile{
		"proj-a": {ID: "proj-a", Name: "alpha", Path: filepath.Join(t.TempDir(), "alpha"), IsActive: true},
		"proj-b": {ID: "proj-b", Name: "beta", Path: filepath.Join(t.TempDir(), "beta")},
	}

	// Control: a healthy index round-trips.
	SaveProjectsIndex(seed)
	if got := LoadProjectsIndex(); len(got) != 2 || got["proj-a"].Name != "alpha" || got["proj-b"].Name != "beta" {
		t.Fatalf("control: healthy index round-trip failed: got %+v", got)
	}

	// Corrupt: a truncated write; load must yield an empty registry, move the
	// bytes out of the live path, and preserve them verbatim for diagnosis.
	corrupt := []byte(`{"projects":[{"id":"proj-` + strings.Repeat("x", 200))
	if err := os.WriteFile(indexPath, corrupt, 0o644); err != nil {
		t.Fatalf("write corrupt index: %v", err)
	}
	if got := LoadProjectsIndex(); len(got) != 0 {
		t.Fatalf("corrupt index must yield an empty registry, got %d entries", len(got))
	}
	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Fatalf("corrupt index still on the live path (stat err=%v) — the next save would erase it silently", err)
	}
	quarantined, err := os.ReadFile(indexPath + ".corrupt")
	if err != nil {
		t.Fatalf("quarantined copy missing: %v", err)
	}
	if string(quarantined) != string(corrupt) {
		t.Fatalf("quarantined bytes differ from the original corrupt bytes")
	}

	// Recovery: the live path is free; the registry round-trips again.
	SaveProjectsIndex(seed)
	if got := LoadProjectsIndex(); len(got) != 2 {
		t.Fatalf("recovery round-trip failed after quarantine: got %d entries", len(got))
	}

	// Missing-file path: quiet fresh start, no quarantine artifacts. Clear the
	// quarantine artifact the corrupt phase above already verified first, so
	// this assertion can only fail if the missing-file load creates a new one.
	if err := os.Remove(indexPath + ".corrupt"); err != nil {
		t.Fatalf("remove stale quarantine: %v", err)
	}
	if err := os.Remove(indexPath); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	if got := LoadProjectsIndex(); len(got) != 0 {
		t.Fatalf("missing index must yield an empty registry, got %d entries", len(got))
	}
	if _, err := os.Stat(indexPath + ".corrupt"); !os.IsNotExist(err) {
		t.Fatalf("missing-file load must not create quarantine artifacts (stat err=%v)", err)
	}
}

// Regression (round 43): SaveProjectsIndex must signal persistence failures —
// every caller treats the save as fire-and-forget, so silence read as success —
// and publish atomically: the live index is only ever replaced by a complete
// sibling, and a healthy save leaves no artifacts behind.
func TestSaveProjectsIndex_SignalsFailureAndKeepsPublishClean(t *testing.T) {
	registry := map[string]ProjectProfile{
		"proj-a": {ID: "proj-a", Name: "alpha", Path: filepath.Join(t.TempDir(), "alpha")},
	}

	// (a) Structural write failure: WRONGTRACE_HOME's parent is an existing
	// file, so MkdirAll and the publish write both fail. The failure must be
	// logged — pre-fix it was swallowed with `_ =`.
	parentFile := filepath.Join(t.TempDir(), "i-am-a-file")
	if err := os.WriteFile(parentFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Setenv("WRONGTRACE_HOME", filepath.Join(parentFile, "home"))

	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	SaveProjectsIndex(registry)
	log.SetOutput(os.Stderr)
	if logBuf.Len() == 0 {
		t.Fatalf("persistence failure was silent — no diagnostic was emitted")
	}

	// (b) Control: a healthy home round-trips, stays quiet, leaves no siblings.
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	SaveProjectsIndex(registry)
	indexPath := filepath.Join(UserWrongTraceDir(), "projects.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("control: healthy save produced no index: %v", err)
	}
	var parsed struct {
		Projects []ProjectProfile `json:"projects"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Projects) != 1 || parsed.Projects[0].ID != "proj-a" {
		t.Fatalf("control: saved index does not round-trip (unmarshal=%v, entries=%d)", err, len(parsed.Projects))
	}
	if siblings, _ := filepath.Glob(indexPath + ".*"); len(siblings) > 0 {
		t.Fatalf("healthy save left sibling artifacts: %v", siblings)
	}
}
