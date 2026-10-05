//go:build unix

package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression (sun-path bound path): the daemon advertises its IPC endpoint to
// agents through GET /api/health's socket_path, whose contract is "the IPC
// endpoint the daemon BOUND, if any" (cmd/wrongtrace/main.go). That contract
// was violated whenever the configured path overflowed sockaddr_un.sun_path:
// bindSocketPaths silently falls back to $TMPDIR/wrongtrace.sock and still
// returns nil from Start, but runStart set reportedSocketPath to the CONFIGURED
// value, so health advertised a socket that does not exist while the daemon was
// listening somewhere else. Server.boundPath already recorded the truth; it was
// unexported with no accessor, so no caller could report it.
//
// The test is unix-gated rather than !windows: it binds a real unix socket
// (net.Listen("unix", ...)), which does not exist on js/wasm or plan9, and the
// production fallback it pins is the POSIX branch of bindSocketPaths.
//
// Control: a SHORT configured path must bind exactly itself, proving BoundPath
// reports the real bind rather than a constant fallback.
func TestStart_LongConfiguredPathBindsFallbackAndReportsIt(t *testing.T) {
	// Redirect the well-known /tmp discovery symlink so this test cannot touch
	// the real one (shared with discovery_link_unix_regression_test.go).
	useTempDiscoveryLink(t)

	// os.TempDir() is read at call time, so pointing TMPDIR at a per-test dir
	// keeps the fallback socket inside it (no other user's squatting entry).
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	// Comfortably past every sun_path limit (104 darwin / 108 linux) while
	// staying under the platform's overall path limit.
	longPath := filepath.Join(tmp, strings.Repeat("d/", 60), "wrongtrace.sock")
	if len(longPath) < 108 {
		t.Fatalf("test fixture is too short to exercise the fallback: %d bytes", len(longPath))
	}

	srv := NewServer(Config{SocketPath: longPath, Engine: &fakeSink{}})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start with an over-long configured path must succeed via the fallback, got: %v", err)
	}
	defer srv.Stop()

	bound := srv.BoundPath()
	if bound == "" {
		t.Fatal("BoundPath is empty after a successful Start")
	}
	if bound == longPath {
		t.Fatalf("BoundPath reported the CONFIGURED path %q, but the bind that succeeded was the fallback", longPath)
	}

	// What was bound must exist, and the configured path must not — this is the
	// harm the caller's advertised endpoint inherited.
	fi, err := os.Stat(bound)
	if err != nil {
		t.Fatalf("the reported bound path %q does not exist: %v", bound, err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("the reported bound path %q is not a socket", bound)
	}
	if _, err := os.Lstat(longPath); err == nil {
		t.Fatalf("the over-long configured path %q unexpectedly exists", longPath)
	}

	wantFallback := filepath.Join(tmp, "wrongtrace.sock")
	if bound != wantFallback {
		t.Errorf("BoundPath = %q, want the documented fallback %q", bound, wantFallback)
	}
}

func TestStart_ShortConfiguredPathReportsItself(t *testing.T) {
	useTempDiscoveryLink(t)

	shortPath := filepath.Join(t.TempDir(), "wrongtrace.sock")
	srv := NewServer(Config{SocketPath: shortPath, Engine: &fakeSink{}})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start with a short configured path failed: %v", err)
	}
	defer srv.Stop()

	if got := srv.BoundPath(); got != shortPath {
		t.Errorf("BoundPath = %q, want the configured path %q", got, shortPath)
	}
}
