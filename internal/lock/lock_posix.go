//go:build !windows

package lock

import (
	"os"
	"syscall"
)

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
