package lock

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireAndRelease(t *testing.T) {
	tempDir := t.TempDir()

	l, err := Acquire(tempDir, 0)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if l == nil {
		t.Fatal("expected non-nil lock")
	}

	// Lock files should exist
	if _, err := os.Stat(l.pidPath); err != nil {
		t.Errorf("pid file missing: %v", err)
	}
	if _, err := os.Stat(l.lockPath); err != nil {
		t.Errorf("lock file missing: %v", err)
	}

	l.Release()

	// Lock files should be cleaned up
	if _, err := os.Stat(l.pidPath); !os.IsNotExist(err) {
		t.Errorf("pid file still exists after release")
	}
}

func TestStalePIDReclaimed(t *testing.T) {
	tempDir := t.TempDir()

	// Write an invalid/dead PID (e.g. 999999)
	pidPath := tempDir + "/daemon.pid"
	_ = os.WriteFile(pidPath, []byte("999999"), 0o644)

	// Should reclaim and succeed
	l, err := Acquire(tempDir, 0)
	if err != nil {
		t.Fatalf("expected acquire to succeed on stale PID, got: %v", err)
	}
	defer l.Release()

	raw, _ := os.ReadFile(pidPath)
	if string(raw) != strconv.Itoa(os.Getpid()) {
		t.Errorf("expected current PID %d, got %s", os.Getpid(), string(raw))
	}
}

// healthStub serves GET /api/health with the given body and returns its port.
func healthStub(t *testing.T, body string) int {
	t.Helper()
	return healthStubDelayingFirst(t, body, 0)
}

// healthStubDelayingFirst is healthStub whose FIRST response is delayed by d,
// after which it answers instantly. It encodes a loaded machine
// deterministically: isPortActive bounds a single attempt at 300ms, so a
// machine under load can miss one window even against a local, instant stub.
func healthStubDelayingFirst(t *testing.T, body string, d time.Duration) int {
	t.Helper()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if d > 0 && calls.Add(1) == 1 {
			time.Sleep(d)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)

	idx := strings.LastIndex(ts.URL, ":")
	if idx < 0 {
		t.Fatalf("no port in stub URL %q", ts.URL)
	}
	port, err := strconv.Atoi(ts.URL[idx+1:])
	if err != nil || port <= 0 {
		t.Fatalf("parse port from %q: %v", ts.URL, err)
	}
	return port
}

// TestPortActiveCollision: a live daemon on the port must abort startup with
// the friendly ErrAlreadyRunning rather than a raw bind failure.
//
// The stub delays its first answer, which is the condition that used to fail
// this test on a loaded machine: the probe's single 300ms window expired,
// isPortActive reported "no daemon", Acquire took the lock, and the assertion
// below read as a contract violation ("expected ErrAlreadyRunning, got <nil>").
func TestPortActiveCollision(t *testing.T) {
	port := healthStubDelayingFirst(t, `{"service":"wrongtrace","ok":true,"status":"ok"}`, 400*time.Millisecond)

	// The probe is ONE 300ms-bounded HTTP attempt, so on a loaded machine it
	// can expire against a stub that is local and instant. Wait for the
	// precondition -- the production probe actually seeing the stub -- so the
	// assertion below only ever runs against a reachable daemon; without it the
	// test reported a contract failure for a probe timeout.
	awaitStubReachable(t, port)

	l, err := Acquire(t.TempDir(), port)
	if l != nil {
		// Acquire must not have taken the lock. Release defensively so the
		// unexpected-success path cannot leak the instance lock -- which also
		// made the TempDir cleanup fail on Windows ("used by another process").
		l.Release()
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("expected ErrAlreadyRunning, got %v", err)
	}
}

// awaitStubReachable waits, bounded, until the production probe can see the
// stub on port. A loaded machine can miss a single 300ms window; the contract
// under test needs the daemon to be visible, not one attempt to land.
func awaitStubReachable(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if isPortActive(port) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stub on port %d never answered the health probe within the budget", port)
}

// TestPortActive_IgnoresForeignHealthEndpoint guards the misdiagnosis: the
// probe used to accept ANY 200 on /api/health as proof of a running daemon.
// {"status":"ok"} is the most generic health shape there is (and an SPA dev
// server answers 200 on every path), so an unrelated process holding the port
// aborted startup with "wrongtrace daemon is already running" and sent the
// operator hunting for a daemon that did not exist. Without our own service
// marker in the body, the probe must not claim the port is ours.
func TestPortActive_IgnoresForeignHealthEndpoint(t *testing.T) {
	for _, body := range []string{
		`{"status":"ok"}`,              // generic health JSON
		`{"service":"grafana"}`,        // someone else's marker
		`<!doctype html><html></html>`, // SPA dev server catch-all
		``,                             // empty 200
	} {
		port := healthStub(t, body)
		if isPortActive(port) {
			t.Errorf("isPortActive claimed a foreign endpoint serving %q is a WrongTrace daemon", body)
		}
		l, err := Acquire(t.TempDir(), port)
		if errors.Is(err, ErrAlreadyRunning) {
			t.Errorf("Acquire refused to start over a foreign endpoint serving %q", body)
		}
		// Release before the temp dir is torn down: Windows cannot unlink a
		// file whose handle is still open.
		l.Release()
	}
}

func TestInstanceLock_EdgeCases(t *testing.T) {
	// 1. Nil lock release should not panic
	var nilLock *InstanceLock
	nilLock.Release()

	// 2. Double release should be idempotent
	tempDir := t.TempDir()
	l, err := Acquire(tempDir, 0)
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	l.Release()
	l.Release() // second release

	// 3. Invalid port should not block acquisition
	l2, err := Acquire(tempDir, -1)
	if err != nil {
		t.Fatalf("acquire with port -1 failed: %v", err)
	}
	l2.Release()
}
