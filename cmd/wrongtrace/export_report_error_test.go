package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// TestExportFailsLoudlyWhenStoreQueryFails pins the round-46 contract: when a
// store query fails, runExport must return an error naming the failing query —
// never ship a silently incomplete export at exit 0. The injection is a
// runtime_traces data page corrupted after a clean close: db.Open and Migrate
// still pass (only that table's page is damaged), so every traces query fails
// with SQLITE_CORRUPT while the rest of the store stays healthy.
func TestExportFailsLoudlyWhenStoreQueryFails(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "proof.db")

	st, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// A row so the traces query actually reads the data page we corrupt — an
	// empty table's ORDER BY is satisfied from the index alone.
	if _, err := st.DB().Exec(
		`INSERT INTO runtime_traces (trace_id, service_name, profiler_type, duration_ms, status_code, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"r46-1", "proofsvc", "custom", 1.5, 200, "2026-09-06 10:00:00",
	); err != nil {
		t.Fatalf("seed trace: %v", err)
	}
	var rootPage int
	if err := st.DB().QueryRow(`SELECT rootpage FROM sqlite_master WHERE type='table' AND name='runtime_traces'`).Scan(&rootPage); err != nil {
		t.Fatalf("rootpage: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	if _, err := f.WriteAt(make([]byte, 64), int64(rootPage-1)*4096); err != nil {
		t.Fatalf("corrupt page: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	outPath := filepath.Join(home, "out.json")

	// db/out are persistent flags on the root command; drive the real wiring
	// through cobra exactly as the CLI does.
	var cliOut bytes.Buffer
	rootCmd.SetOut(&cliOut)
	rootCmd.SetErr(&cliOut)
	rootCmd.SetArgs([]string{"export", "--db", dbPath, "--out", outPath})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	err = rootCmd.Execute()
	if err == nil {
		t.Fatal("export must fail loudly when a store query fails — a silent exit 0 ships an incomplete artifact")
	}
	if combined := err.Error() + cliOut.String(); !strings.Contains(combined, "recent traces") {
		t.Fatalf("error must name the failing query, got: %v / %s", err, cliOut.String())
	}
}
