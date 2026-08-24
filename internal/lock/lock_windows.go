//go:build windows

package lock

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetExitCodeProcess = modkernel32.NewProc("GetExitCodeProcess")
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

// tryLock takes a non-blocking exclusive byte-range lock on the first byte of
// the lock file. Windows byte-range locks are per-file and shared across
// processes, so a second daemon fails immediately with ERROR_LOCK_VIOLATION
// instead of racing the PID/port pre-checks. The OS releases the lock when
// the handle closes — including on process crash — which also removes the
// stale-PID / recycled-PID ambiguity the pre-checks alone cannot solve.
func tryLock(f *os.File) error {
	const flags = syscall.LOCKFILE_EXCLUSIVE_LOCK | syscall.LOCKFILE_FAIL_IMMEDIATELY
	var overlapped syscall.Overlapped
	if err := syscall.LockFileEx(syscall.Handle(f.Fd()), flags, 0, 1, 0, &overlapped); err != nil {
		if errno, ok := err.(syscall.Errno); ok && errno == syscall.ERROR_LOCK_VIOLATION {
			return ErrAlreadyRunning
		}
		return fmt.Errorf("lock daemon.lock: %w", err)
	}
	return nil
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var exitCode uint32
	r, _, _ := procGetExitCodeProcess.Call(uintptr(h), uintptr(unsafe.Pointer(&exitCode)))
	if r == 0 {
		return false
	}
	return exitCode == stillActive
}
