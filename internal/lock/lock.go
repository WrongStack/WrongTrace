package lock

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrAlreadyRunning indicates that an active WrongTrace daemon instance already exists.
var ErrAlreadyRunning = errors.New("wrongtrace daemon is already running")

// InstanceLock represents an exclusively held single-instance lock.
type InstanceLock struct {
	dir      string
	lockPath string
	pidPath  string
	file     *os.File
}

// Acquire attempts to acquire the single-instance daemon lock in dataDir.
// If an active daemon is detected (via health check or OS process check),
// it returns ErrAlreadyRunning.
func Acquire(dataDir string, port int) (*InstanceLock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	pidPath := filepath.Join(dataDir, "daemon.pid")
	lockPath := filepath.Join(dataDir, "daemon.lock")

	// 1. Check if an existing PID is registered and alive
	if raw, err := os.ReadFile(pidPath); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
			if isDaemonAlive(pid, port) {
				return nil, fmt.Errorf("%w (PID %d on port %d)", ErrAlreadyRunning, pid, port)
			}
		}
	}

	// 2. Also check if the port is already actively responding as WrongTrace
	if isPortActive(port) {
		return nil, fmt.Errorf("%w (active endpoint detected at http://localhost:%d)", ErrAlreadyRunning, port)
	}

	// 3. Acquire the OS-level single-instance lock. The PID/port pre-checks
	// above only produce friendly error messages; this non-blocking lock is
	// the authoritative gate — it closes the check-then-act window where two
	// concurrently started daemons both passed step 1 and 2, and the
	// different-port bypass where a healthy daemon on another port was
	// invisible to isDaemonAlive.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := tryLock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrAlreadyRunning) {
			return nil, fmt.Errorf("%w (lock file held by another daemon)", ErrAlreadyRunning)
		}
		return nil, err
	}

	// Record current PID
	currentPID := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(pidPath, []byte(currentPID), 0o644); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write pid file: %w", err)
	}

	return &InstanceLock{
		dir:      dataDir,
		lockPath: lockPath,
		pidPath:  pidPath,
		file:     f,
	}, nil
}

// Release releases the lock and removes the PID file. daemon.lock itself is
// deliberately kept: removing it would let a second process open a new file
// (new inode) and lock that instead of contending with the live lock holder.
func (l *InstanceLock) Release() {
	if l == nil {
		return
	}
	if l.file != nil {
		_ = l.file.Close() // closing the handle releases the OS lock
	}
	_ = os.Remove(l.pidPath)
}

// isDaemonAlive checks if a process with the given PID is running and responds on the health port.
func isDaemonAlive(pid int, port int) bool {
	if pid == os.Getpid() {
		return false
	}
	if isPortActive(port) {
		return true
	}
	if port > 0 {
		// If port is configured but not answering health checks, the daemon is not running.
		// Never block startup due to unrelated OS processes with recycled PIDs.
		return false
	}
	return isProcessAlive(pid)
}

// isPortActive checks if the WrongTrace health API is already answering.
func isPortActive(port int) bool {
	if port <= 0 {
		return false
	}
	client := http.Client{Timeout: 300 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/api/health", port))
	if err == nil && resp != nil {
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	return false
}
