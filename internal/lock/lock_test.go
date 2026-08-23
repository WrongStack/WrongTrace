package lock

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
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

func TestPortActiveCollision(t *testing.T) {
	tempDir := t.TempDir()

	// Start a mock server serving /api/health with 200 OK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer ts.Close()

	// Extract port from ts.URL
	var port int
	for i := len(ts.URL) - 1; i >= 0; i-- {
		if ts.URL[i] == ':' {
			p, _ := strconv.Atoi(ts.URL[i+1:])
			port = p
			break
		}
	}

	if port > 0 {
		_, err := Acquire(tempDir, port)
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Errorf("expected ErrAlreadyRunning, got %v", err)
		}
	}
}
