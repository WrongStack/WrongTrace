//go:build !unix

package ipc

import "os"

// ownedByCurrentUser has no uid model to consult off unix (Windows binds a
// named pipe and never reaches the socket-file path), so it preserves the
// historical replace-the-entry behavior.
func ownedByCurrentUser(os.FileInfo) bool { return true }
