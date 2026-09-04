package lock

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
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
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			w.WriteHeader(http.StatusNotFound)
			return
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
func TestPortActiveCollision(t *testing.T) {
	port := healthStub(t, `{"service":"wrongtrace","ok":true,"status":"ok"}`)

	_, err := Acquire(t.TempDir(), port)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("expected ErrAlreadyRunning, got %v", err)
	}
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
