// Package db owns the WrongTrace analytical store. It uses a pure-Go SQLite
// driver (modernc.org/sqlite) so the binary stays self-contained — no CGO, no
// native libraries, just a single file on disk. All writes are append-only to
// the two canonical tables; metrics are derived in queries.go.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Store is the public façade for the database. It is safe for concurrent use;
// all writes serialize through writeMu to avoid contention on SQLite's
// per-connection write lock.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

// Open opens (or creates) the SQLite database at path and configures pragmas
// for analytical workloads: WAL for concurrent readers, NORMAL sync for
// acceptable fsync latency, 64 MiB page cache, 256 MiB memory-mapped I/O, and in-memory temp tables.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir db dir: %w", err)
		}
	}

	// cache_size is PER CONNECTION, so the pool multiplies it. 8 MiB across up
	// to eight connections reserved 64 MiB of page cache for a database that is
	// usually a few megabytes; 4 MiB across four connections covers the same
	// working set at a quarter of the footprint. mmap_size caps the memory-
	// mapped window, which the OS reclaims under pressure.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-4096)&_pragma=mmap_size(33554432)&_pragma=temp_store(MEMORY)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		path,
	)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}

	// In WAL mode SQLite serves concurrent readers alongside a single writer,
	// and every dashboard query is an indexed lookup over a small table. Four
	// readers saturate that; more only multiply per-connection page cache.
	maxConns := runtime.NumCPU()
	if maxConns < 2 {
		maxConns = 2
	}
	if maxConns > 4 {
		maxConns = 4
	}
	conn.SetMaxOpenConns(maxConns)
	// Keep only a small warm set idle. A closed dashboard should not leave the
	// daemon holding a page cache per core for hours.
	conn.SetMaxIdleConns(2)
	conn.SetConnMaxLifetime(1 * time.Hour)
	conn.SetConnMaxIdleTime(2 * time.Minute)

	if err := conn.PingContext(context.Background()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Store{db: conn}, nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	// Run PRAGMA optimize on close to update query planner statistics without locking startup
	_, _ = s.db.Exec("PRAGMA optimize;")
	return s.db.Close()
}

// DB exposes the underlying *sql.DB for callers that need to run bespoke
// read-only queries (e.g. analytics endpoints). Prefer the higher-level
// methods on Store when available.
func (s *Store) DB() *sql.DB { return s.db }

// Migrate applies the embedded schema. It is idempotent — every DDL uses
// CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS.
func (s *Store) Migrate() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(context.Background(), schemaSQL)
	if err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	// Add new columns to existing databases safely. Only the expected
	// "already migrated" no-op may be swallowed: a locked database, full
	// disk, or corrupt page must fail startup here instead of surfacing as
	// "table has no column named ..." on every later insert.
	for _, col := range []string{
		"ALTER TABLE code_node_events ADD COLUMN start_line INTEGER DEFAULT 0",
		"ALTER TABLE code_node_events ADD COLUMN end_line INTEGER DEFAULT 0",
		"ALTER TABLE code_node_events ADD COLUMN diff_snippet TEXT DEFAULT ''",
		"ALTER TABLE code_node_events ADD COLUMN added_lines INTEGER DEFAULT 0",
		"ALTER TABLE code_node_events ADD COLUMN deleted_lines INTEGER DEFAULT 0",
		"ALTER TABLE code_node_events ADD COLUMN attribution_source VARCHAR DEFAULT 'unknown'",
		"ALTER TABLE code_node_events ADD COLUMN attribution_confidence DOUBLE DEFAULT 0.0",
	} {
		if _, err := s.db.ExecContext(context.Background(), col); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("migrate alter: %w", err)
		}
	}

	// Ensure composite performance indexes exist on existing DBs
	for _, idx := range []string{
		"CREATE INDEX IF NOT EXISTS idx_node_sig_time ON code_node_events(file_path, node_signature, event_time DESC)",
		"CREATE INDEX IF NOT EXISTS idx_node_repo_time ON code_node_events(repo_name, event_time DESC)",
		"CREATE INDEX IF NOT EXISTS idx_node_action_time ON code_node_events(action, event_time DESC)",
		"CREATE INDEX IF NOT EXISTS idx_node_sig_max ON code_node_events(node_signature, event_time DESC)",
		"CREATE INDEX IF NOT EXISTS idx_node_repo_sig ON code_node_events(repo_name, node_signature, event_time DESC)",
		"CREATE INDEX IF NOT EXISTS idx_node_time_repo_file ON code_node_events(event_time DESC, repo_name, file_path, node_signature)",
		"CREATE INDEX IF NOT EXISTS idx_runs_created ON agent_runs(created_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_read_file_time ON file_read_events(file_path, read_time DESC)",
	} {
		if _, err := s.db.ExecContext(context.Background(), idx); err != nil {
			if msg := err.Error(); strings.Contains(msg, "already exists") {
				continue
			}
			return fmt.Errorf("migrate index: %w", err)
		}
	}

	return nil
}

// Overview is a tiny aggregate used by `wrongtrace status`.
type Overview struct {
	TotalRuns    int
	TotalEvents  int
	TotalCost    float64
	UniqueModels int
}

// Overview returns at-a-glance counts from the store, optionally filtered by repo_name.
func (s *Store) Overview(repoFilter ...string) (Overview, error) {
	var repo string
	if len(repoFilter) > 0 {
		repo = repoFilter[0]
	}

	ctx, cancel := s.withTimeout(context.Background())
	defer cancel()

	var o Overview
	if repo == "" {
		row := s.db.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM agent_runs),
				(SELECT COUNT(*) FROM code_node_events),
				COALESCE((SELECT SUM(cost_usd) FROM agent_runs), 0),
				(SELECT COUNT(DISTINCT model_name) FROM agent_runs)
		`)
		if err := row.Scan(&o.TotalRuns, &o.TotalEvents, &o.TotalCost, &o.UniqueModels); err != nil {
			return Overview{}, fmt.Errorf("overview scan: %w", err)
		}
		return o, nil
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(DISTINCT r.run_id) FROM agent_runs r
			 WHERE r.run_id IN (
				SELECT DISTINCT run_id FROM code_node_events WHERE (repo_name = ? OR repo_name = '' OR repo_name IS NULL) AND run_id IS NOT NULL
				UNION
				SELECT DISTINCT run_id FROM file_read_events WHERE (repo_name = ? OR repo_name = '' OR repo_name IS NULL) AND run_id IS NOT NULL
			 ) OR NOT EXISTS (SELECT 1 FROM code_node_events WHERE repo_name != ? AND repo_name != '' AND repo_name IS NOT NULL)),
			(SELECT COUNT(*) FROM code_node_events WHERE (repo_name = ? OR repo_name = '' OR repo_name IS NULL)),
			COALESCE((SELECT SUM(r.cost_usd) FROM agent_runs r
			 WHERE r.run_id IN (
				SELECT DISTINCT run_id FROM code_node_events WHERE (repo_name = ? OR repo_name = '' OR repo_name IS NULL) AND run_id IS NOT NULL
				UNION
				SELECT DISTINCT run_id FROM file_read_events WHERE (repo_name = ? OR repo_name = '' OR repo_name IS NULL) AND run_id IS NOT NULL
			 ) OR NOT EXISTS (SELECT 1 FROM code_node_events WHERE repo_name != ? AND repo_name != '' AND repo_name IS NOT NULL)), 0),
			(SELECT COUNT(DISTINCT r.model_name) FROM agent_runs r
			 WHERE r.run_id IN (
				SELECT DISTINCT run_id FROM code_node_events WHERE (repo_name = ? OR repo_name = '' OR repo_name IS NULL) AND run_id IS NOT NULL
				UNION
				SELECT DISTINCT run_id FROM file_read_events WHERE (repo_name = ? OR repo_name = '' OR repo_name IS NULL) AND run_id IS NOT NULL
			 ) OR NOT EXISTS (SELECT 1 FROM code_node_events WHERE repo_name != ? AND repo_name != '' AND repo_name IS NOT NULL))
	`, repo, repo, repo, repo, repo, repo, repo, repo, repo, repo)
	if err := row.Scan(&o.TotalRuns, &o.TotalEvents, &o.TotalCost, &o.UniqueModels); err != nil {
		return Overview{}, fmt.Errorf("overview scan: %w", err)
	}
	return o, nil
}

// withTimeout returns a context bounded by 5 seconds for store operations.
// SQLite operations should be near-instant; if they aren't, we'd rather fail
// the request than wedge the daemon.
func (s *Store) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 5*time.Second)
}
