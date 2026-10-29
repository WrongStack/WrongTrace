package lock

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// A stale Release -- here a second call on an already-released lock -- must
// not delete the daemon.pid a successor wrote after taking the lock over.
func TestRelease_KeepsSuccessorPIDFile(t *testing.T) {
	dir := t.TempDir()
	a, err := Acquire(dir, 0)
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	a.Release()

	b, err := Acquire(dir, 0)
	if err != nil {
		t.Fatalf("acquire B: %v", err)
	}
	defer b.Release()

	a.Release() // stale owner releases again
	if _, err := os.Stat(b.pidPath); err != nil {
		t.Fatalf("successor's daemon.pid removed by stale Release: %v", err)
	}
}

// Release only removes a daemon.pid that still names this instance.
func TestRelease_KeepsForeignPIDFile(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir, 0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := os.WriteFile(l.pidPath, []byte("999999"), 0o644); err != nil {
		t.Fatalf("overwrite pid: %v", err)
	}
	l.Release()
	raw, err := os.ReadFile(l.pidPath)
	if err != nil || string(raw) != "999999" {
		t.Fatalf("foreign daemon.pid = %q, %v; want it untouched", raw, err)
	}

	// Control: an own pid file is still cleaned up.
	l2, err := Acquire(dir, 0)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	l2.Release()
	if _, err := os.Stat(l2.pidPath); !os.IsNotExist(err) {
		t.Fatalf("own daemon.pid not removed: %v", err)
	}
}

// The pid file is advisory: failing to write it must not give up the OS lock,
// or a second start (which the caller lets continue on any non-sentinel
// error) runs concurrently.
func TestAcquire_PIDWriteFailureKeepsLock(t *testing.T) {
	dir := t.TempDir()
	// A directory where daemon.pid belongs makes the write fail on every
	// platform, regardless of privileges.
	if err := os.Mkdir(filepath.Join(dir, "daemon.pid"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := Acquire(dir, 0)
	if err != nil {
		t.Fatalf("first Acquire with unwritable pid file: %v", err)
	}
	defer a.Release()

	b, err := Acquire(dir, 0)
	if b != nil {
		b.Release()
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire = %v; want ErrAlreadyRunning", err)
	}
	a.Release()
	if fi, err := os.Stat(filepath.Join(dir, "daemon.pid")); err != nil || !fi.IsDir() {
		t.Fatalf("Release touched a daemon.pid it never wrote: %v", err)
	}
}

// Without a port, a stale daemon.pid naming a live but unrelated process
// (a recycled PID) must not block startup while daemon.lock is free.
func TestAcquire_PortlessRecycledPIDDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	unrelated := os.Getppid() // alive for the whole test, never this process
	if !isProcessAlive(unrelated) {
		t.Skip("parent process not observable as alive")
	}
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(unrelated)), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(dir, 0)
	if err != nil {
		t.Fatalf("Acquire(port 0) over a recycled PID: %v", err)
	}
	defer l.Release()

	// While the lock is held, the same live PID still yields the sentinel.
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(unrelated)), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir, 0)
	if second != nil {
		second.Release()
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire = %v; want ErrAlreadyRunning", err)
	}
}
