package main

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

func TestRootCmd_HelpAndVersion(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	rootCmd.SetArgs([]string{"--help"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute --help failed: %v", err)
	}

	out := buf.String()
	if len(out) == 0 {
		t.Error("expected non-empty help output")
	}
}

func TestStatusCmd(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "status_test.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	// Set db flag
	startCmd.Flags().Set("db", dbPath)
	statusCmd.Flags().Set("db", dbPath)

	rootCmd.SetArgs([]string{"status"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute status failed: %v", err)
	}
}

func TestDoctorCmd(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "doctor_test.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	doctorCmd.Flags().Set("db", dbPath)
	doctorCmd.Flags().Set("watch", tempDir)

	rootCmd.SetArgs([]string{"doctor", "--db", dbPath, "--watch", tempDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute doctor failed: %v", err)
	}
}

func TestExportCmd(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "export_test.db")
	outFile := filepath.Join(tempDir, "export.json")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	exportCmd.Flags().Set("db", dbPath)
	exportCmd.Flags().Set("out", outFile)

	rootCmd.SetArgs([]string{"export", "--db", dbPath, "--out", outFile})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute export failed: %v", err)
	}
}

func TestReportCmd(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "report_test.db")
	outMd := filepath.Join(tempDir, "report.md")
	outHtml := filepath.Join(tempDir, "report.html")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	// Test Markdown report
	rootCmd.SetArgs([]string{"report", "--db", dbPath, "--format", "markdown", "--out", outMd})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute report markdown failed: %v", err)
	}

	// Test HTML report
	rootCmd.SetArgs([]string{"report", "--db", dbPath, "--format", "html", "--out", outHtml})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute report html failed: %v", err)
	}
}

func TestHookCmd(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tempDir, ".git"), 0755)

	oldCwd, _ := os.Getwd()
	_ = os.Chdir(tempDir)
	defer func() { _ = os.Chdir(oldCwd) }()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	rootCmd.SetArgs([]string{"hook", "install"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute hook install failed: %v", err)
	}

	rootCmd.SetArgs([]string{"hook", "uninstall"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute hook uninstall failed: %v", err)
	}
}

func TestDefaultHelpers(t *testing.T) {
	dataDir := defaultDataDir()
	if len(dataDir) == 0 {
		t.Error("defaultDataDir returned empty string")
	}

	sockPath := defaultSocketPath()
	if len(sockPath) == 0 {
		t.Error("defaultSocketPath returned empty string")
	}

	cwd := mustCwd()
	if len(cwd) == 0 {
		t.Error("mustCwd returned empty string")
	}
}

func TestSingleInstance_PreventDuplicate(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", tempDir)

	// A live daemon is simulated by an endpoint that answers the health probe
	// lock.Acquire uses. A PID file alone is NOT enough: isDaemonAlive
	// deliberately ignores a PID equal to the current process (a stale file
	// holding a recycled PID must never block startup), so runStart would
	// acquire the lock, boot the whole daemon and block on its signal channel
	// until the test binary hit its 10-minute timeout.
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.NotFound(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	port := ln.Addr().(*net.TCPAddr).Port
	if err := rootCmd.PersistentFlags().Set("port", strconv.Itoa(port)); err != nil {
		t.Fatalf("set port flag: %v", err)
	}
	t.Cleanup(func() { _ = rootCmd.PersistentFlags().Set("port", "8000") })

	// The PID file is present too, so step 1 of Acquire is exercised as well.
	pidPath := filepath.Join(tempDir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	// Calling runStart should detect the running instance and return nil gracefully
	if err := runStart(rootCmd, []string{}); err != nil {
		t.Errorf("expected runStart to return nil on duplicate instance, got: %v", err)
	}
}

func TestInitCmd(t *testing.T) {
	tempDir := t.TempDir()
	oldCwd, _ := os.Getwd()
	_ = os.Chdir(tempDir)
	defer func() { _ = os.Chdir(oldCwd) }()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute init failed: %v", err)
	}
}

func TestTraceCmd(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "trace_test.db")

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	// Run a fast echo command via trace
	rootCmd.SetArgs([]string{"trace", "--db", dbPath, "--service", "test-svc", "--", "go", "version"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute trace failed: %v", err)
	}
}

func TestExportAndReport_Comprehensive(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "full_test.db")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	_ = store.Migrate()
	_ = store.InsertEvent(db.EventRecord{
		EventID:    "ev-test-1",
		RepoName:   "full-test",
		FilePath:   "main.go",
		Signature:  "func:main",
		NodeType:   "function",
		Action:     "ADDED",
		BodyHash:   "h1",
		LOC:        10,
		OccurredAt: time.Now().UTC(),
	})
	_ = store.Close()

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	exportCmd.Flags().Set("out", "")
	reportCmd.Flags().Set("out", "")

	// Export to stdout
	rootCmd.SetArgs([]string{"export", "--db", dbPath})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute export to stdout failed: %v", err)
	}

	// Report JSON to stdout
	rootCmd.SetArgs([]string{"report", "--db", dbPath, "--format", "json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute report json failed: %v", err)
	}

	// Status on populated DB
	rootCmd.SetArgs([]string{"status", "--db", dbPath})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute status populated failed: %v", err)
	}
}
