//go:build linux

package core

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// fileBirthTime asks statx for the birth time; filesystems or kernels that do
// not report STATX_BTIME yield false.
func fileBirthTime(path string, _ os.FileInfo) (time.Time, bool) {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_STATX_SYNC_AS_STAT, unix.STATX_BTIME, &stx); err != nil {
		return time.Time{}, false
	}
	if stx.Mask&unix.STATX_BTIME == 0 || stx.Btime.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(stx.Btime.Sec, int64(stx.Btime.Nsec)), true
}
