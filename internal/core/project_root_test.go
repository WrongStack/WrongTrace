package core

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateProjectRoot_AcceptsOrdinaryDirectory(t *testing.T) {
	tmp := t.TempDir()
	if err := validateProjectRoot(tmp); err != nil {
		t.Errorf("ordinary directory %s should be accepted: %v", tmp, err)
	}
	// Nested paths inherit the acceptance.
	nested := filepath.Join(tmp, "workspace")
	if err := validateProjectRoot(nested); err != nil {
		t.Errorf("nested directory %s should be accepted: %v", nested, err)
	}
}

func TestValidateProjectRoot_RefusesVolumeRoot(t *testing.T) {
	volumeRoot := "/"
	if runtime.GOOS == "windows" {
		volumeRoot = filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	}
	if err := validateProjectRoot(volumeRoot); err == nil {
		t.Errorf("volume root %s must be refused", volumeRoot)
	}
}

func TestValidateProjectRoot_RefusesOwnStorageArea(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", home)

	// The state home itself, and anything inside its per-project DB tree,
	// would create an ingestion feedback loop with the daemon's own writes.
	for _, p := range []string{
		home,
		filepath.Join(home, "projects"),
		filepath.Join(home, "projects", "some-slug", "wrongtrace.db"),
	} {
		if err := validateProjectRoot(p); err == nil {
			t.Errorf("watching %s must be refused", p)
		}
	}

	// A workspace sitting next to the storage tree is a normal layout when
	// WRONGTRACE_HOME is co-located with the repos it observes.
	sibling := filepath.Join(home, "WorkspaceA")
	if err := validateProjectRoot(sibling); err != nil {
		t.Errorf("workspace beside the storage tree should be accepted: %v", err)
	}
}
