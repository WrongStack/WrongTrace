//go:build !windows

package lock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tryLock takes a non-blocking exclusive flock on the lock file. The kernel
// releases it on close and on process crash, so a second daemon gets
// EWOULDBLOCK immediately instead of racing the PID/port pre-checks, and a
// crashed daemon never leaves a stale lock behind.
func tryLock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return ErrAlreadyRunning
		}
		return fmt.Errorf("flock daemon.lock: %w", err)
	}
	return nil
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, sending signal 0 checks for process existence without killing it.
	err = p.Signal(syscall.Signal(0))
	return err == nil
}
