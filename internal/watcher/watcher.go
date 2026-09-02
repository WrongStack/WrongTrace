// Package watcher wraps fsnotify with debouncing, ignore rules, and a clean
// integration into the core Engine. Edits to the same file within the
// debounce window coalesce into a single AST diff; binary, vendored, and
// dotfile churn is filtered before reaching the engine.
package watcher

import (
	"context"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	// DebugFSEvents enables in-memory capture of every fsnotify event with
	// path, op, timestamp, and semaphore occupancy. Access via
	// GET /api/debug/fsnotify. When disabled the buffer is nil (zero cost).
	DebugFSEvents bool
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
	"__pycache__",
	".venv",
	"venv",
	".pytest_cache",
	".idea",
	".vscode",
	".svelte-kit",
	".astro",
}

// Watcher is a debouncing filesystem observer rooted at a single directory.
type Watcher struct {
	cfg Config
	// root is cfg.Dir made absolute and clean. Ignore rules are evaluated
	// against paths relative to it so directories ABOVE the project never
	// participate in matching.
	root          string
	fs            *fsnotify.Watcher
	debounce      time.Duration
	patterns      []string
	patternsNorm  []string
	patternsLower []string
	ignoreSet     map[string]struct{}

	// decisions memoizes pathIgnored. An editor save-burst, a build, or a
	// dependency install replays the SAME handful of paths through the filter
	// thousands of times, and each miss cost four string allocations plus a
	// filepath.Match against every .gitignore line. The map is capped and
	// cleared wholesale rather than evicted per-entry: the answer for a path
	// never changes while the watcher lives, so any survivor is equally valid.
	decisionMu sync.RWMutex
	decisions  map[string]bool

	// DebugFSEvents captures every fsnotify event in a bounded circular buffer
	// when Config.DebugFSEvents is true. Access via Watcher.FSNotifyLog().
	evBuf   []fsEvent // nil when disabled (zero allocation cost)
	evMu    sync.Mutex
	evHead  int
	evCount uint64 // monotonic sequence number for ordering

	// semOcc records the webhook dispatcher's in-flight count at the moment each
	// fsnotify event was captured, enabling correlation between disk activity and
	// semaphore pressure. Updated by Engine via UpdateSemOccupied; read atomically.
	semOcc atomic.Int32

	// httpHandler serves GET /api/debug/fsnotify when DebugFSEvents is true.
	httpHandler http.Handler
	httpMu     sync.RWMutex
}

// fsEvent is one captured fsnotify event in arrival order.
type fsEvent struct {
	Seq         uint64        // monotonic sequence number
	Path        string        // absolute event path
	Op         fsnotify.Op   // fsnotify.Create/Write/Rename/Remove/...
	Time        time.Time     // wall-clock time of arrival from fsnotify
	SemOccupied int           // len(inFlight) at moment of capture
}

// captureEvent appends ev to the circular buffer when DebugFSEvents is enabled.
// Safe to call from the fsnotify event-loop goroutine without a lock on the
// hot path when evBuf is nil.
func (w *Watcher) captureEvent(ev fsnotify.Event) {
	if w.evBuf == nil {
		return
	}
	w.evMu.Lock()
	w.evBuf[w.evHead] = fsEvent{
		Seq:         w.evCount,
		Path:        ev.Name,
		Op:          ev.Op,
		Time:        time.Now(),
		SemOccupied: int(w.semOcc.Load()),
	}
	w.evHead = (w.evHead + 1) % len(w.evBuf)
	w.evCount++
	w.evMu.Unlock()
}

// FSNotifyLog returns up to n most-recent captured fsnotify events, oldest
// first. Only slots captureEvent has actually written are returned: before
// the buffer fills, no unwritten (zero-value) entries leak out. Returns nil
// when Config.DebugFSEvents is false or nothing has been captured.
func (w *Watcher) FSNotifyLog(n int) []fsEvent {
	w.evMu.Lock()
	defer w.evMu.Unlock()
	if w.evBuf == nil {
		return nil
	}
	bufLen := len(w.evBuf)
	filled := bufLen
	if w.evCount < uint64(bufLen) {
		filled = int(w.evCount)
	}
	if n > filled {
		n = filled
	}
	if n <= 0 {
		return nil
	}
	// evHead is the slot captureEvent will write NEXT, so the oldest event
	// of the most-recent-n window sits n slots behind it. Walking forward
	// from there yields strictly increasing Seqs across the wrap boundary.
	start := (w.evHead - n + bufLen) % bufLen
	out := make([]fsEvent, n)
	for i := 0; i < n; i++ {
		out[i] = w.evBuf[(start+i)%bufLen]
	}
	return out
}

// Handler returns an HTTP handler that streams captured fsnotify events as SSE.
// The handler drains the circular buffer and then waits for new events.
// Returns nil when Config.DebugFSEvents is false.
func (w *Watcher) Handler() http.Handler {
	if w.httpHandler == nil {
		return nil
	}
	return w.httpHandler
}

// UpdateSemOccupied records the webhook dispatcher's in-flight count so that
// captureEvent can embed it in the next captured fsnotify event. Called by the
// engine after each dispatch completes. Zero cost when evBuf is nil.
func (w *Watcher) UpdateSemOccupied(occ int) {
	w.semOcc.Store(int32(occ))
}

// debugFSNotifyHandler returns an SSE handler that streams fsnotify events from
// w's circular buffer by polling every 200ms. It first sends any buffered events
// (oldest first), then streams new events as they accumulate.
func debugFSNotifyHandler(ww *Watcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ww.cfg.DebugFSEvents {
			http.Error(w, "debug fsnotify is not enabled", 400)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(200)
		flusher.Flush()

		// Stream buffered events first (oldest first).
		for _, ev := range ww.FSNotifyLog(4096) {
			fmt.Fprintf(w, "data: {\"seq\":%d,\"path\":%q,\"op\":%q,\"time\":%q,\"sem_occupied\":%d}\n\n",
				ev.Seq, ev.Path, ev.Op, ev.Time.Format(time.RFC3339Nano), ev.SemOccupied)
			flusher.Flush()
		}

		// Poll for new events until the client disconnects.
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		var lastSeq uint64
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				for _, ev := range ww.FSNotifyLog(4096) {
					if ev.Seq <= lastSeq {
						continue
					}
					lastSeq = ev.Seq
					fmt.Fprintf(w, "data: {\"seq\":%d,\"path\":%q,\"op\":%q,\"time\":%q,\"sem_occupied\":%d}\n\n",
						ev.Seq, ev.Path, ev.Op, ev.Time.Format(time.RFC3339Nano), ev.SemOccupied)
					flusher.Flush()
				}
			}
		}
	})
}

// maxIgnoreDecisions caps the memo. Large enough to cover a real repository's
// working set, small enough that the map itself is never the leak.
const maxIgnoreDecisions = 8192

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

	ignoreSet := make(map[string]struct{}, len(cfg.IgnoreDirs))
	for _, ig := range cfg.IgnoreDirs {
		ignoreSet[strings.ToLower(filepath.Clean(ig))] = struct{}{}
	}

	patterns := loadGitIgnorePatterns(cfg.Dir)
	patternsNorm := make([]string, len(patterns))
	patternsLower := make([]string, len(patterns))
	for i, pat := range patterns {
		patNorm := filepath.ToSlash(pat)
		patternsNorm[i] = patNorm
		patternsLower[i] = strings.ToLower(patNorm)
	}

	// Allocate the circular event buffer when debug capture is enabled.
	var evBuf []fsEvent
	var httpHandler http.Handler
	if cfg.DebugFSEvents {
		evBuf = make([]fsEvent, 4096)
	}

	w := &Watcher{
		cfg:           cfg,
		root:          absRoot(cfg.Dir),
		fs:            fw,
		debounce:      cfg.Debounce,
		patterns:      patterns,
		patternsNorm:  patternsNorm,
		patternsLower: patternsLower,
		ignoreSet:     ignoreSet,
		decisions:     make(map[string]bool, 256),
		evBuf:        evBuf,
	}

	// Wire the SSE handler after w is allocated so it can capture w.
	if cfg.DebugFSEvents {
		httpHandler = debugFSNotifyHandler(w)
		w.httpHandler = httpHandler
	}
	if err := w.addRecursive(cfg.Dir); err != nil {
		_ = fw.Close()
		return nil, err
	}
	return w, nil
}

// absRoot normalizes the watch root so scopedPath can relate event paths to
// it. An unresolvable root yields "", which disables scoping and falls back
// to whole-path matching.
func absRoot(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
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

// AddWatchDir dynamically registers a directory tree with fsnotify for live observation.
func (w *Watcher) AddWatchDir(dir string) error {
	if w == nil || w.fs == nil {
		return nil
	}
	return w.addRecursive(dir)
}

// RemoveWatchDir unregisters a directory tree from fsnotify.
func (w *Watcher) RemoveWatchDir(dir string) error {
	if w == nil || w.fs == nil {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || !d.IsDir() {
			return nil
		}
		_ = w.fs.Remove(path)
		return nil
	})
}

// addRecursive walks the root and registers every directory with fsnotify.
// Symlink loops and permission errors are logged and skipped rather than
// aborting the whole watch.
func (w *Watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			log.Printf("watcher: skip %s: %v", path, err)
			return nil
		}
		if d == nil || !d.IsDir() {
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

// scopedPath reduces p to its location relative to the watched root.
//
// Ignore rules must only ever see the project's own directory names. Matching
// the full absolute path made every ancestor segment count, so a checkout
// living under /tmp, ~/build, /opt/out or C:\bin collided with the default
// IgnoreDirs list: addRecursive hit SkipDir on the root itself and the
// watcher silently observed nothing at all.
//
// Paths that are not under the root (and relative paths supplied directly by
// callers) fall back to whole-path matching, which is the historical
// behavior.
func (w *Watcher) scopedPath(p string) string {
	if w.root == "" {
		return p
	}
	if len(p) >= len(w.root) {
		if strings.HasPrefix(p, w.root) {
			if len(p) == len(w.root) {
				return ""
			}
			if p[len(w.root)] == filepath.Separator || p[len(w.root)] == '/' {
				return p[len(w.root)+1:]
			}
		}
	}
	rel, err := filepath.Rel(w.root, p)
	if err != nil {
		return p
	}
	if rel == "." {
		// The watched root itself is never ignored by its own name.
		return ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return rel
}

// pathIgnored is a fast-path filter for events and directory walking. The
// decision is memoized; only a cache miss pays for normalization and pattern
// matching.
func (w *Watcher) pathIgnored(p string) bool {
	w.decisionMu.RLock()
	cached, ok := w.decisions[p]
	w.decisionMu.RUnlock()
	if ok {
		return cached
	}

	verdict := w.computePathIgnored(p)

	w.decisionMu.Lock()
	if len(w.decisions) >= maxIgnoreDecisions {
		w.decisions = make(map[string]bool, 256)
	}
	w.decisions[p] = verdict
	w.decisionMu.Unlock()
	return verdict
}

// computePathIgnored is the uncached filter: ignore-directory segments first
// (O(1) per segment), then .gitignore patterns.
func (w *Watcher) computePathIgnored(p string) bool {
	scoped := w.scopedPath(p)
	if scoped == "" {
		return false
	}
	norm := filepath.ToSlash(scoped)
	normLower := strings.ToLower(norm)
	base := filepath.Base(scoped)
	baseLower := strings.ToLower(base)

	// 1. Fast O(1) segment check without allocations
	if _, ok := w.ignoreSet[baseLower]; ok {
		return true
	}
	for seg := normLower; len(seg) > 0; {
		idx := strings.IndexByte(seg, '/')
		var s string
		if idx == -1 {
			s = seg
			seg = ""
		} else {
			s = seg[:idx]
			seg = seg[idx+1:]
		}
		if _, ok := w.ignoreSet[s]; ok {
			return true
		}
	}

	// 2. Check .gitignore patterns using precomputed normalized patterns
	for i, patNorm := range w.patternsNorm {
		if matched, _ := filepath.Match(patNorm, base); matched {
			return true
		}
		patLower := w.patternsLower[i]
		if patLower == normLower ||
			strings.HasPrefix(normLower, patLower+"/") ||
			strings.HasSuffix(normLower, "/"+patLower) ||
			strings.Contains(normLower, "/"+patLower+"/") {
			return true
		}
	}

	return false
}

// debounceEntry is the per-path debounce state. The timer is reused via Reset
// instead of Stop+allocate on every fsnotify event, which build storms (tens
// of thousands of events) turned into constant allocation churn. deadline is
// what makes stale callbacks distinguishable: a timer that fired before a
// Reset extended the deadline must not consume the entry, because Reset does
// not cancel an AfterFunc callback that is already queued.
type debounceEntry struct {
	timer    *time.Timer
	deadline time.Time
}

// Run blocks until ctx is cancelled or the underlying fsnotify watcher fails.
// It implements per-path debouncing with a reused timer per pending path.
func (w *Watcher) Run(ctx context.Context) {
	if w.cfg.Engine == nil {
		log.Printf("watcher: no engine configured; idle")
		<-ctx.Done()
		return
	}

	var pendingMu sync.Mutex
	pending := make(map[string]*debounceEntry)

	defer func() {
		pendingMu.Lock()
		for _, e := range pending {
			if e != nil && e.timer != nil {
				e.timer.Stop()
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
			// Capture every fsnotify event to the circular buffer when enabled.
			// This has zero overhead when evBuf is nil (DebugFSEvents=false).
			w.captureEvent(ev)
			if w.pathIgnored(ev.Name) {
				continue
			}
			if ev.Op&fsnotify.Create == fsnotify.Create {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					go func() {
						if err := w.addRecursive(ev.Name); err != nil {
							log.Printf("watcher: addRecursive %s: %v", ev.Name, err)
						}
					}()
				}
			}
			if !isRelevant(ev.Op) {
				continue
			}

			path := ev.Name
			pendingMu.Lock()
			if entry := pending[path]; entry != nil {
				entry.deadline = time.Now().Add(w.debounce)
				entry.timer.Reset(w.debounce)
			} else {
				entry := &debounceEntry{deadline: time.Now().Add(w.debounce)}
				entry.timer = time.AfterFunc(w.debounce, func() {
					pendingMu.Lock()
					cur := pending[path]
					// Only the newest schedule for this path may consume the
					// entry: an in-flight callback from before a Reset sees a
					// deadline in the future and leaves it for the rearmed
					// timer. Timers never fire early, so firing at or after
					// the deadline means this callback owns the schedule.
					if cur == nil || cur.timer != entry.timer || time.Now().Before(cur.deadline) {
						pendingMu.Unlock()
						return
					}
					delete(pending, path)
					pendingMu.Unlock()
					if ctx.Err() != nil {
						return
					}
					w.cfg.Engine.HandleFileChange(ctx, path)
				})
				pending[path] = entry
			}
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
