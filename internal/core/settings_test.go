package core

import (
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
