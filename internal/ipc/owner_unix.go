//go:build unix

package ipc

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether fi (from os.Lstat) belongs to the
// effective user running the daemon.
func ownedByCurrentUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == os.Geteuid()
}
