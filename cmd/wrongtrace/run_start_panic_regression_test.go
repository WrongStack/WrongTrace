package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Round 9 (proof-driven bug hunt): runStart's panic recovery logged a fatal
// startup panic and then let the function return its ZERO value, so
// `wrongtrace start` exited 0 after a fatal failure. main() exits non-zero only
// when a command RETURNS an error (main.go:141-147), so a supervisor or script
// gating on that exit code was told a daemon that never came up had started.
//
// The recover exists because startup can panic -- it primes the whole workspace
// through Tree-sitter, binds sockets/pipes, and walks agent directories -- and
// whatever raises the panic, a fatal startup failure must surface as a command
// error. This test raises one deterministically (a nil command panics at the
// first flag read) and asserts the error, with the daemon log as the control
// that the panic it induced is the panic the recovery handled.
func TestRunStart_PanicSurfacesAsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WRONGTRACE_HOME", home)
	// runStart redirects logging to <dataDir>/daemon.log when it can open it,
	// so restore the writer for the rest of the package afterwards.
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })

	err := runStart(nil, nil)
	if err == nil {
		t.Fatalf("runStart returned nil after a panic: a fatal startup failure would exit 0")
	}

	// Control: the recovered panic really is the one this call induced.
	logged, readErr := os.ReadFile(filepath.Join(home, "daemon.log"))
	if readErr != nil {
		t.Fatalf("read daemon.log: %v", readErr)
	}
	if !strings.Contains(string(logged), "FATAL PANIC in runStart") {
		t.Fatalf("daemon.log does not record the recovered panic; got %q", string(logged))
	}
}
