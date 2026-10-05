package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runTraceInProcess drives the real runTrace through the root cobra command and
// returns its banner. runTrace writes the banner to cmd.ErrOrStderr(), so
// capturing cobra's error writer captures exactly what a user sees. The traced
// child is a trivially-succeeding command so a non-zero child can never explain
// the outcome — the assertion is about runTrace's OWN persistence behavior.
func runTraceInProcess(t *testing.T, dbPath string) (string, error) {
	t.Helper()
	var tail []string
	if runtime.GOOS == "windows" {
		tail = []string{"cmd", "/c", "exit 0"}
	} else {
		tail = []string{"sh", "-c", "exit 0"}
	}
	args := append([]string{"trace", "--port", "1", "--db", dbPath, "--"}, tail...)

	var errBuf bytes.Buffer
	rootCmd.SetOut(&errBuf)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	err := rootCmd.Execute()
	return errBuf.String(), err
}

// TestRunTraceNeverClaimsCaptureWhenNotPersisted pins the contract that a
// command's user-facing summary must agree with what actually happened.
//
// runTrace tries an HTTP POST to a live daemon and, failing that, a local
// SQLite write. The old code printed "📊 Captured Execution Trace" to the banner
// UNCONDITIONALLY, even when both paths failed — the code itself already logged
// "trace not persisted" in those branches. So with the daemon down and the local
// store unavailable, the user was told the trace was captured while it was
// stored nowhere. This is the same self-contradiction fixed for runDoctor, and
// the exit code deliberately stays the child's (telemetry is best-effort and
// must never fail a passing test run).
//
// Injection: --db points at a DIRECTORY (db.Open cannot open a directory as
// SQLite) and --port 1 is privileged so the daemon POST is refused. Both
// persistence paths fail through the real production code.
func TestRunTraceNeverClaimsCaptureWhenNotPersisted(t *testing.T) {
	dir := t.TempDir()
	badDB := filepath.Join(dir, "not-a-db")
	if err := os.MkdirAll(badDB, 0o755); err != nil {
		t.Fatalf("mkdir fake db: %v", err)
	}

	banner, _ := runTraceInProcess(t, badDB)

	// It must NOT claim a capture it could not perform.
	if strings.Contains(banner, "Captured Execution Trace") {
		t.Errorf("runTrace printed %q although neither the daemon nor the local "+
			"store persisted anything:\n%s", "Captured Execution Trace", banner)
	}
	if !strings.Contains(banner, "NOT captured") {
		t.Errorf("a failed persistence must produce an explicit negative banner:\n%s", banner)
	}
}

// TestRunTraceClaimsCaptureWhenPersisted is the control: with a valid --db, the
// local fallback stores the trace, so the banner legitimately says "Captured".
// It fails if the fix suppresses the success banner instead of conditioning it.
func TestRunTraceClaimsCaptureWhenPersisted(t *testing.T) {
	dir := t.TempDir()
	goodDB := filepath.Join(dir, "wrongtrace.db")

	banner, _ := runTraceInProcess(t, goodDB)

	if strings.Contains(banner, "NOT captured") {
		t.Fatalf("a valid local store must persist the trace and report Captured:\n%s", banner)
	}
	if !strings.Contains(banner, "Captured Execution Trace") {
		t.Errorf("a genuinely persisted trace must still report Captured:\n%s", banner)
	}
}
