package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout replaced by a pipe and returns whatever
// was written. runDoctor prints with fmt.Printf (not through cobra's SetOut),
// so the only way to observe its output in-process is to swap the real file
// descriptor. The reader is drained concurrently: the pipe buffer is finite and
// doctor's agent-discovery table can exceed it, which would otherwise deadlock.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	func() {
		defer func() {
			os.Stdout = orig
			_ = w.Close()
		}()
		fn()
	}()

	out := <-done
	_ = r.Close()
	return out
}

// runDoctorCmd drives the real cobra wiring. db is a root PERSISTENT flag, so
// the argument goes after the subcommand exactly as it does on the CLI.
func runDoctorCmd(t *testing.T, dbPath, watch string) (string, error) {
	t.Helper()
	rootCmd.SetArgs([]string{"doctor", "--db", dbPath, "--watch", watch})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	return out, err
}

// TestDoctorSummaryNeverClaimsHealthWhenACheckFails pins the contract that a
// diagnostic's closing verdict must agree with its own sections.
//
// The storage section reports "FAIL (...)" honestly, but the summary was an
// unconditional `✓ Diagnostics complete. WrongTrace is operational.` — so a
// broken database produced FAIL immediately followed by a green "operational"
// verdict. The summary is the one line a user actually reads, and it is what a
// `doctor && start` script's human reads before trusting the daemon.
//
// Injection: --db points at a DIRECTORY. db.Open cannot open a directory as a
// SQLite database (ping fails), so the storage section genuinely fails through
// the production path — no page corruption or seeding needed.
func TestDoctorSummaryNeverClaimsHealthWhenACheckFails(t *testing.T) {
	dir := t.TempDir()
	watch := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(watch, 0o755); err != nil {
		t.Fatalf("mkdir watch: %v", err)
	}
	badDB := filepath.Join(dir, "not-a-db")
	if err := os.MkdirAll(badDB, 0o755); err != nil {
		t.Fatalf("mkdir fake db: %v", err)
	}

	out, _ := runDoctorCmd(t, badDB, watch)

	if !strings.Contains(out, "FAIL") {
		t.Fatalf("setup drift: doctor did not report FAIL for a bad --db:\n%s", out)
	}
	// Match the FULL success sentence, not the bare word "operational" — the
	// negative summary legitimately contains "operational" as a substring of
	// "NOT fully operational".
	const successSummary = "✓ Diagnostics complete. WrongTrace is operational."
	if strings.Contains(out, successSummary) {
		t.Errorf("doctor printed FAIL for storage yet its summary still said %q:\n%s", successSummary, out)
	}
	if !strings.Contains(out, "NOT fully operational") {
		t.Errorf("a failed check must produce an explicit negative summary:\n%s", out)
	}
}

// TestDoctorSummaryClaimsHealthWhenAllChecksPass is the control: on a healthy
// store the summary must still read operational. It fails if the fix suppresses
// the success line outright instead of conditioning it.
func TestDoctorSummaryClaimsHealthWhenAllChecksPass(t *testing.T) {
	dir := t.TempDir()
	watch := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(watch, 0o755); err != nil {
		t.Fatalf("mkdir watch: %v", err)
	}
	goodDB := filepath.Join(dir, "wrongtrace.db")

	out, _ := runDoctorCmd(t, goodDB, watch)

	if strings.Contains(out, "FAIL") {
		t.Fatalf("a healthy store must not report FAIL:\n%s", out)
	}
	if !strings.Contains(out, "✓ Diagnostics complete. WrongTrace is operational.") {
		t.Errorf("healthy run must keep the success summary:\n%s", out)
	}
}

// TestDoctorSummaryNotAffectedByStaleFlagState guards the harness itself: these
// tests reuse the package-level rootCmd, so a leaked --db from a previous test
// would silently make the "healthy" case fail for the wrong reason. Reset the
// persistent flag between subtests and confirm doctor still honours --db.
func TestDoctorSummaryRespectsDBFlag(t *testing.T) {
	dir := t.TempDir()
	watch := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(watch, 0o755); err != nil {
		t.Fatalf("mkdir watch: %v", err)
	}
	badDB := filepath.Join(dir, "not-a-db")
	if err := os.MkdirAll(badDB, 0o755); err != nil {
		t.Fatalf("mkdir fake db: %v", err)
	}

	// A healthy path FIRST, so a stale bad --db left on the flag would make the
	// healthy assertion fail — proving the flag is actually re-read per call.
	if out, _ := runDoctorCmd(t, filepath.Join(dir, "ok.db"), watch); strings.Contains(out, "FAIL") {
		t.Fatalf("healthy store reported FAIL (stale flag state?):\n%s", out)
	}
	if out, _ := runDoctorCmd(t, badDB, watch); !strings.Contains(out, "FAIL") {
		t.Errorf("second call did not honour the new --db (flag state leaked):\n%s", out)
	}
}
