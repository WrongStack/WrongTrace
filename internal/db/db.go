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
// acceptable fsync latency, and a 32 MiB in-memory journal cache.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir db dir: %w", err)
		}
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-32768)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		path,
	)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite: one writer at a time; reads use the same connection pool safely when WAL is enabled.
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
	// Add new columns to existing databases safely
	for _, col := range []string{
		"ALTER TABLE code_node_events ADD COLUMN start_line INTEGER DEFAULT 0",
		"ALTER TABLE code_node_events ADD COLUMN end_line INTEGER DEFAULT 0",
		"ALTER TABLE code_node_events ADD COLUMN diff_snippet TEXT DEFAULT ''",
		"ALTER TABLE code_node_events ADD COLUMN added_lines INTEGER DEFAULT 0",
		"ALTER TABLE code_node_events ADD COLUMN deleted_lines INTEGER DEFAULT 0",
	} {
		_, _ = s.db.ExecContext(context.Background(), col)
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

// Overview returns at-a-glance counts from the store.
func (s *Store) Overview() (Overview, error) {
	var o Overview
	row := s.db.QueryRowContext(context.Background(), `
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

// withTimeout returns a context bounded by 5 seconds for store operations.
// SQLite operations should be near-instant; if they aren't, we'd rather fail
// the request than wedge the daemon.
func (s *Store) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 5*time.Second)
}
