//go:build darwin

package core

import (
	"os"
	"syscall"
	"time"
)

// fileBirthTime returns the APFS/HFS+ birth time carried by os.Stat.
func fileBirthTime(_ string, info os.FileInfo) (time.Time, bool) {
	if info == nil {
		return time.Time{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil || st.Birthtimespec.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(st.Birthtimespec.Unix()), true
}
