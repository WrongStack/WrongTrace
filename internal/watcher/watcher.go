// Package watcher wraps fsnotify with debouncing, ignore rules, and a clean
// integration into the core Engine. Edits to the same file within the
// debounce window coalesce into a single AST diff; binary, vendored, and
// dotfile churn is filtered before reaching the engine.
package watcher

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileHandler is implemented by the core engine. We keep it as an interface
// so the watcher package does not import the engine (avoiding a cycle when
// the engine eventually wants to subscribe to specific paths).
type FileHandler interface {
	HandleFileChange(ctx context.Context, path string)
}

// Config configures a Watcher.
type Config struct {
	Dir    string
	Engine FileHandler
	// Debounce is the minimum interval between two AST diffs for the same
	// file. Editor save-bursts often emit 3-5 events within ~100ms; 250ms is
	// a good default for balancing latency and CPU.
	Debounce time.Duration
	// IgnoreDirs are directory basenames whose changes never reach the
	// engine: VCS metadata, dependency caches, build output.
	IgnoreDirs []string
}

// DefaultIgnoreDirs contains directory names that are always ignored from watching and AST diffs.
var DefaultIgnoreDirs = []string{
	".git",
	".temp_files",
	"temp_files",
	".tmp",
	"tmp",
	"node_modules",
	"vendor",
	"dist",
	"build",
	"target",
	".next",
	".nuxt",
	".turbo",
	".cache",
	".wrongtrace",
	"coverage",
	"out",
	".out",
	"bin",
}

// Watcher is a debouncing filesystem observer rooted at a single directory.
type Watcher struct {
	cfg      Config
	fs       *fsnotify.Watcher
	debounce time.Duration
	patterns []string
}

// New constructs and primes a Watcher. It does not start the event loop; call
// Run from a goroutine.
func New(cfg Config) (*Watcher, error) {
	if cfg.Debounce <= 0 {
		cfg.Debounce = 250 * time.Millisecond
	}
	if len(cfg.IgnoreDirs) == 0 {
		cfg.IgnoreDirs = make([]string, len(DefaultIgnoreDirs))
		copy(cfg.IgnoreDirs, DefaultIgnoreDirs)
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		cfg:      cfg,
		fs:       fw,
		debounce: cfg.Debounce,
		patterns: loadGitIgnorePatterns(cfg.Dir),
	}
	if err := w.addRecursive(cfg.Dir); err != nil {
		_ = fw.Close()
		return nil, err
	}
	return w, nil
}

func loadGitIgnorePatterns(root string) []string {
	var patterns []string
	giPath := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(giPath)
	if err != nil {
		return patterns
	}
	lines := strings.Split(string(data), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		l = strings.TrimSuffix(l, "/")
		patterns = append(patterns, l)
	}
	return patterns
}

// Close releases the underlying fsnotify resources.
func (w *Watcher) Close() error {
	if w == nil || w.fs == nil {
		return nil
	}
	return w.fs.Close()
}

// addRecursive walks the root and registers every directory with fsnotify.
// Symlink loops and permission errors are logged and skipped rather than
// aborting the whole watch.
func (w *Watcher) addRecursive(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Permission denied on a single subdir should not kill startup.
			if info == nil {
				log.Printf("watcher: skip %s: %v", path, err)
				return nil
			}
			log.Printf("watcher: skip %s: %v", path, err)
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if w.pathIgnored(path) {
			return filepath.SkipDir
		}
		if err := w.fs.Add(path); err != nil {
			log.Printf("watcher: cannot watch %s: %v", path, err)
		}
		return nil
	})
}

// pathIgnored is a fast-path filter for events and directory walking.
func (w *Watcher) pathIgnored(p string) bool {
	norm := filepath.ToSlash(p)
	segs := strings.Split(norm, "/")
	base := filepath.Base(p)

	// 1. Check directory segments
	for _, seg := range segs {
		for _, ig := range w.cfg.IgnoreDirs {
			if strings.EqualFold(seg, ig) {
				return true
			}
		}
	}

	// 2. Check .gitignore patterns
	for _, pattern := range w.patterns {
		patNorm := filepath.ToSlash(pattern)
		if matched, _ := filepath.Match(patNorm, base); matched {
			return true
		}
		if strings.Contains(norm, "/"+patNorm+"/") || strings.HasSuffix(norm, "/"+patNorm) || strings.HasPrefix(norm, patNorm+"/") {
			return true
		}
	}

	return false
}

// Run blocks until ctx is cancelled or the underlying fsnotify watcher fails.
// It implements per-path debouncing with a timer per pending path.
func (w *Watcher) Run(ctx context.Context) {
	if w.cfg.Engine == nil {
		log.Printf("watcher: no engine configured; idle")
		<-ctx.Done()
		return
	}

	var pendingMu sync.Mutex
	pending := make(map[string]*time.Timer)

	defer func() {
		pendingMu.Lock()
		for _, t := range pending {
			if t != nil {
				t.Stop()
			}
		}
		pendingMu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			if w.pathIgnored(ev.Name) {
				continue
			}
			if ev.Op&fsnotify.Create == fsnotify.Create {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = w.addRecursive(ev.Name)
				}
			}
			if !isRelevant(ev.Op) {
				continue
			}

			path := ev.Name
			pendingMu.Lock()
			if t, exists := pending[path]; exists && t != nil {
				t.Stop()
			}
			pending[path] = time.AfterFunc(w.debounce, func() {
				pendingMu.Lock()
				delete(pending, path)
				pendingMu.Unlock()
				w.cfg.Engine.HandleFileChange(ctx, path)
			})
			pendingMu.Unlock()
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

// isRelevant filters fsnotify opcodes down to the ones we care about. CREATE
// is significant because new files deserve an initial ADDED event; CHMOD
// alone rarely implies a semantic change.
func isRelevant(op fsnotify.Op) bool {
	switch {
	case op&fsnotify.Write == fsnotify.Write,
		op&fsnotify.Create == fsnotify.Create,
		op&fsnotify.Remove == fsnotify.Remove,
		op&fsnotify.Rename == fsnotify.Rename:
		return true
	}
	return false
}
