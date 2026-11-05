package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wrongstack/wrongtrace/internal/core"
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
	return samePath(dir, home)
}

// coversUserHome reports whether dir is the home directory or one of its
// ancestors (C:\, C:\Users, /home). Scanning such a directory walks the whole
// home tree, which is exactly what isUserHome exists to prevent.
func coversUserHome(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || dir == "" {
		return false
	}
	d := filepath.Clean(dir)
	for h := filepath.Clean(home); ; {
		if samePath(d, h) {
			return true
		}
		parent := filepath.Dir(h)
		if parent == h {
			return false
		}
		h = parent
	}
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// homeStartProjectDir returns the directory to watch in place of the home
// directory: the active project's path, when there is one, it exists, and
// scanning it would not walk the home tree after all. "" means watch nothing.
func homeStartProjectDir(p *core.ProjectProfile) string {
	if p == nil || p.Path == "" {
		return ""
	}
	dir, err := filepath.Abs(p.Path)
	if err != nil || coversUserHome(dir) {
		return ""
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return dir
}

// logHomeStartWarning tells the user why the home directory they started in
// is not being scanned, what is watched instead, and how to pick a directory.
func logHomeStartWarning(home string, p *core.ProjectProfile, activeDir string) {
	log.Printf("WARNING: wrongtrace was started in your home directory (%s).", home)
	if activeDir != "" {
		log.Printf("WARNING: The home directory is not scanned. Watching the active project %q instead:", p.Name)
		log.Printf("WARNING:     %s", activeDir)
		log.Printf("WARNING: To watch a different directory, start wrongtrace from it:")
	} else {
		log.Printf("WARNING: The home directory is not scanned, so no files are being watched.")
		log.Printf("WARNING: Start it from a project directory instead:")
	}
	log.Printf("WARNING:     cd <project-dir>")
	log.Printf("WARNING:     wrongtrace start")
	log.Printf("WARNING: or pass the directory explicitly: wrongtrace start --watch <project-dir>")
}
