package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpdateSettingsCanClearIgnorePatterns(t *testing.T) {
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	settingsMu.Lock()
	original := globalSettings
	globalSettings.IgnorePatterns = []string{"node_modules", "dist"}
	settingsMu.Unlock()
	t.Cleanup(func() {
		settingsMu.Lock()
		globalSettings = original
		settingsMu.Unlock()
	})

	engine := NewEngine(Config{RepoName: "settings-clear-regression"})

	cleared := engine.UpdateSettings(AppSettings{IgnorePatterns: []string{}})
	if len(cleared.IgnorePatterns) != 0 {
		t.Fatalf("explicit empty ignore_patterns was ignored: got %#v, want []", cleared.IgnorePatterns)
	}

	preserved := engine.UpdateSettings(AppSettings{DebounceMs: 333})
	if !reflect.DeepEqual(preserved.IgnorePatterns, []string{}) {
		t.Fatalf("omitted ignore_patterns should preserve the current list: got %#v, want []", preserved.IgnorePatterns)
	}
	if preserved.DebounceMs != 333 {
		t.Fatalf("control field DebounceMs = %d, want 333", preserved.DebounceMs)
	}
}

func TestUpdateSettingsCanSetRuntimePaths(t *testing.T) {
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	settingsMu.Lock()
	original := globalSettings
	globalSettings.DBPath = ""
	globalSettings.SocketPath = ""
	settingsMu.Unlock()
	t.Cleanup(func() {
		settingsMu.Lock()
		globalSettings = original
		settingsMu.Unlock()
	})

	engine := NewEngine(Config{RepoName: "settings-path-regression"})

	updated := engine.UpdateSettings(AppSettings{
		DBPath:     "/tmp/custom-wrongtrace.db",
		SocketPath: "/tmp/custom-wrongtrace.sock",
	})
	if updated.DBPath != "/tmp/custom-wrongtrace.db" {
		t.Fatalf("DBPath = %q, want custom path from settings update", updated.DBPath)
	}
	if updated.SocketPath != "/tmp/custom-wrongtrace.sock" {
		t.Fatalf("SocketPath = %q, want custom path from settings update", updated.SocketPath)
	}
}

// TestLoadSettingsFromDiskRestoresPersistedSettings pins the round-34 fix:
// loadSettingsFromDisk must mirror UpdateSettings semantics for every field
// the settings API persists — runtime paths survive the reload, an explicitly
// cleared ignore_patterns survives the reload, and omitted fields preserve
// the in-memory state instead of clearing it.
func TestLoadSettingsFromDiskRestoresPersistedSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", home)
	settingsMu.Lock()
	original := globalSettings
	globalSettings.DBPath = ""
	globalSettings.SocketPath = ""
	globalSettings.IgnorePatterns = []string{"sentinel-default"}
	settingsMu.Unlock()
	t.Cleanup(func() {
		settingsMu.Lock()
		globalSettings = original
		settingsMu.Unlock()
	})

	persisted := AppSettings{
		DebounceMs:         300,
		IgnorePatterns:     []string{},
		ThrashingThreshold: 4,
		DBPath:             "/tmp/custom-wrongtrace.db",
		SocketPath:         "/tmp/custom-wrongtrace.sock",
	}
	writeSettingsFixture(t, home, persisted)

	loadSettingsFromDisk()

	settingsMu.RLock()
	loaded := globalSettings
	settingsMu.RUnlock()
	if loaded.DBPath != persisted.DBPath {
		t.Fatalf("DBPath = %q, want persisted %q to survive the reload", loaded.DBPath, persisted.DBPath)
	}
	if loaded.SocketPath != persisted.SocketPath {
		t.Fatalf("SocketPath = %q, want persisted %q to survive the reload", loaded.SocketPath, persisted.SocketPath)
	}
	if !reflect.DeepEqual(loaded.IgnorePatterns, []string{}) {
		t.Fatalf("IgnorePatterns = %#v, want the persisted explicit clear [] to survive the reload", loaded.IgnorePatterns)
	}
	if loaded.DebounceMs != 300 || loaded.ThrashingThreshold != 4 {
		t.Fatalf("scalar fields lost on load: DebounceMs = %d, ThrashingThreshold = %d", loaded.DebounceMs, loaded.ThrashingThreshold)
	}

	// Omitted fields preserve the in-memory state instead of clearing it.
	settingsMu.Lock()
	globalSettings.DBPath = ""
	globalSettings.SocketPath = ""
	globalSettings.IgnorePatterns = []string{"sentinel-omitted"}
	settingsMu.Unlock()
	writeSettingsFixture(t, home, AppSettings{DebounceMs: 310})

	loadSettingsFromDisk()

	settingsMu.RLock()
	loaded = globalSettings
	settingsMu.RUnlock()
	if loaded.DBPath != "" || loaded.SocketPath != "" {
		t.Fatalf("omitted runtime paths must not produce values: DBPath = %q, SocketPath = %q", loaded.DBPath, loaded.SocketPath)
	}
	if !reflect.DeepEqual(loaded.IgnorePatterns, []string{"sentinel-omitted"}) {
		t.Fatalf("omitted ignore_patterns must preserve the current list: got %#v", loaded.IgnorePatterns)
	}
	if loaded.DebounceMs != 310 {
		t.Fatalf("DebounceMs = %d, want 310 from the second fixture", loaded.DebounceMs)
	}
}

func writeSettingsFixture(t *testing.T, home string, s AppSettings) {
	t.Helper()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshal settings fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), data, 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}
}
