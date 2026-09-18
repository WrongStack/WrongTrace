package db

import (
	"path/filepath"
	"testing"
)

// A database created before the index cleanup carries the redundant prefix
// indexes and lacks the covering ones; Migrate must converge it to the
// current set, and Optimize must succeed on it.
func TestMigrateConvergesIndexes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	// Recreate the legacy layout.
	for _, q := range []string{
		"DROP INDEX IF EXISTS idx_node_run_repo",
		"DROP INDEX IF EXISTS idx_read_repo_run",
		"CREATE INDEX idx_node_sig ON code_node_events(file_path, node_signature)",
		"CREATE INDEX idx_node_time ON code_node_events(event_time)",
		"CREATE INDEX idx_node_run ON code_node_events(run_id)",
		"CREATE INDEX idx_read_repo ON file_read_events(repo_name)",
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	exists := func(name string) bool {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	for _, gone := range []string{"idx_node_sig", "idx_node_time", "idx_node_run", "idx_read_repo"} {
		if exists(gone) {
			t.Errorf("legacy index %s survived Migrate", gone)
		}
	}
	for _, want := range []string{"idx_node_run_repo", "idx_read_repo_run", "idx_node_sig_time", "idx_node_time_repo_file"} {
		if !exists(want) {
			t.Errorf("index %s missing after Migrate", want)
		}
	}
	if err := s.Optimize(); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
}
