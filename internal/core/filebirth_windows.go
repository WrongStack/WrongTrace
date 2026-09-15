//go:build windows

package core

import (
	"os"
	"syscall"
	"time"
)

// fileBirthTime returns the NTFS creation time carried by os.Stat.
func fileBirthTime(_ string, info os.FileInfo) (time.Time, bool) {
	if info == nil {
		return time.Time{}, false
	}
	d, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || d == nil {
		return time.Time{}, false
	}
	ns := d.CreationTime.Nanoseconds()
	if ns <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}
