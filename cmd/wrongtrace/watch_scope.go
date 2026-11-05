package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// isUserHome reports whether dir is the current user's home directory.
//
// `wrongtrace start` observes "." by default, so running it from a fresh
// terminal (which opens in the home directory) used to make the daemon walk
// and Tree-sitter-parse the entire home tree before the HTTP server came up.
// On a real machine that is hundreds of thousands of files, and the dashboard
// stayed unreachable with no explanation. The daemon skips the scan for the
// home directory; projects are still observed once registered.
//
// dir must already be absolute. Comparison is case-insensitive on Windows and
// macOS, whose default filesystems are.
func isUserHome(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || dir == "" {
		return false
	}
	a := filepath.Clean(dir)
	b := filepath.Clean(home)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
