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
	"path"
	"path/filepath"
	"runtime"
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
	root     string
	fs       *fsnotify.Watcher
	debounce time.Duration
	// rules are the workspace .gitignore lines in file order; the LAST rule
	// matching a path decides, so `!` negations re-include. See ignore rules.
	rules []ignoreRule
	// anchoredNegation is true when some negation could re-include a path
	// below an excluded directory; false lets the matcher skip that probe.
	anchoredNegation bool
	ignoreSet        map[string]struct{}

	// handleSem bounds concurrent HandleFileChange calls. Debounce timers
	// fire on their own goroutines, one per pending path, so a build or
	// checkout storm touching thousands of files would otherwise parse them
	// all at once.
	handleSem chan struct{}

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
	// Written once in New before the Watcher escapes to any other goroutine,
	// read-only afterwards, so it needs no lock.
	httpHandler http.Handler
}

// fsEvent is one captured fsnotify event in arrival order.
type fsEvent struct {
	Seq         uint64      // monotonic sequence number
	Path        string      // absolute event path
	Op          fsnotify.Op // fsnotify.Create/Write/Rename/Remove/...
	Time        time.Time   // wall-clock time of arrival from fsnotify
	SemOccupied int         // len(inFlight) at moment of capture
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

// CapturesFSEvents reports whether fsnotify events are being recorded for
// the debug stream, i.e. whether UpdateSemOccupied's value is ever read.
func (w *Watcher) CapturesFSEvents() bool {
	return w.evBuf != nil
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

		// Stream buffered events first (oldest first), remembering the newest
		// delivered sequence so the poll loop below never re-emits what the
		// drain already sent. seenAny keeps an empty drain from pre-marking
		// Seq 0 as delivered for a client that connected before any event.
		var lastSeq uint64
		seenAny := false
		for _, ev := range ww.FSNotifyLog(4096) {
			lastSeq = ev.Seq
			seenAny = true
			fmt.Fprintf(w, "data: {\"seq\":%d,\"path\":%q,\"op\":%q,\"time\":%q,\"sem_occupied\":%d}\n\n",
				ev.Seq, ev.Path, ev.Op, ev.Time.Format(time.RFC3339Nano), ev.SemOccupied)
			flusher.Flush()
		}

		// Poll for new events until the client disconnects.
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				for _, ev := range ww.FSNotifyLog(4096) {
					if seenAny && ev.Seq <= lastSeq {
						continue
					}
					lastSeq = ev.Seq
					seenAny = true
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

	rules := loadGitIgnoreRules(cfg.Dir)
	anchoredNegation := false
	for _, r := range rules {
		if r.negate && r.anchored {
			anchoredNegation = true
		}
	}

	// Allocate the circular event buffer when debug capture is enabled.
	var evBuf []fsEvent
	if cfg.DebugFSEvents {
		evBuf = make([]fsEvent, 4096)
	}

	w := &Watcher{
		cfg:              cfg,
		root:             absRoot(cfg.Dir),
		fs:               fw,
		debounce:         cfg.Debounce,
		rules:            rules,
		anchoredNegation: anchoredNegation,
		ignoreSet:        ignoreSet,
		handleSem:        make(chan struct{}, max(2, runtime.NumCPU())),
		decisions:        make(map[string]bool, 256),
		evBuf:            evBuf,
	}

	// Wire the SSE handler after w is allocated so it can capture w.
	if cfg.DebugFSEvents {
		w.httpHandler = debugFSNotifyHandler(w)
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

// foldIgnoreCase mirrors git's core.ignorecase default: case-insensitive
// .gitignore matching on the case-insensitive filesystems of Windows and
// macOS, exact elsewhere. Every rule shape (literal, glob, anchored) folds
// the same way; previously literals folded everywhere while globs never did.
var foldIgnoreCase = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// ignoreRule is one .gitignore line.
type ignoreRule struct {
	// segs is the pattern split on '/', case-folded when foldIgnoreCase.
	// "**" as a whole segment matches any number of path segments.
	segs   []string
	negate bool
	// anchored rules ("/x", "a/b", "**/gen", "docs/*.md") match the whole
	// root-relative path; unanchored ones ("*.log", "logs/") match any
	// single segment, i.e. a file or directory of that name at any depth.
	anchored bool
}

// loadGitIgnoreRules reads the workspace .gitignore into ordered rules.
// A leading slash, or a slash anywhere but the end, anchors the pattern to
// the watched root ("/generated" ignores the root's generated tree but not
// pkg/generated; "docs/*.md" is docs directly under the root). A trailing
// slash is dropped: pathIgnored cannot tell directories from files, and a
// file named like an ignored directory has always been filtered too.
func loadGitIgnoreRules(root string) []ignoreRule {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil
	}
	var rules []ignoreRule
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || l[0] == '#' {
			continue
		}
		var r ignoreRule
		switch {
		case l[0] == '!':
			r.negate = true
			l = l[1:]
		case strings.HasPrefix(l, `\!`), strings.HasPrefix(l, `\#`):
			l = l[1:]
		}
		l = strings.TrimRight(l, "/")
		if strings.HasPrefix(l, "/") {
			r.anchored = true
			l = strings.TrimLeft(l, "/")
		}
		if l == "" {
			continue
		}
		if strings.Contains(l, "/") {
			r.anchored = true
		}
		if foldIgnoreCase {
			l = strings.ToLower(l)
		}
		for _, s := range strings.Split(l, "/") {
			if s != "" {
				r.segs = append(r.segs, s)
			}
		}
		rules = append(rules, r)
	}
	return rules
}

// matches reports whether the rule matches the path given as segments.
func (r *ignoreRule) matches(segs []string) bool {
	if !r.anchored {
		return globSegment(r.segs[0], segs[len(segs)-1])
	}
	return matchSegments(r.segs, segs)
}

// matchSegments matches pattern segments against path segments with "**"
// spanning zero or more segments; a trailing "**" needs at least one
// ("foo/**" is everything inside foo, not foo itself).
func matchSegments(pat, p []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			if len(rest) == 0 {
				return len(p) > 0
			}
			for i := 0; i <= len(p); i++ {
				if matchSegments(rest, p[i:]) {
					return true
				}
			}
			return false
		}
		if len(p) == 0 || !globSegment(pat[0], p[0]) {
			return false
		}
		pat, p = pat[1:], p[1:]
	}
	return len(p) == 0
}

// couldMatchBelow reports whether some path strictly below dir could match
// the pattern — i.e. whether dir must stay traversable for the pattern to
// ever apply.
func couldMatchBelow(pat, dir []string) bool {
	for len(dir) > 0 {
		if len(pat) == 0 {
			return false
		}
		if pat[0] == "**" {
			return true
		}
		if !globSegment(pat[0], dir[0]) {
			return false
		}
		pat, dir = pat[1:], dir[1:]
	}
	return len(pat) > 0
}

func globSegment(pat, s string) bool {
	if pat == s {
		return true
	}
	if !strings.ContainsAny(pat, `*?[\`) {
		return false
	}
	ok, _ := path.Match(pat, s)
	return ok
}

// negationBelow reports whether a negation rule could re-include a path
// inside the excluded directory dir. Such a directory must not be skipped:
// the whitelist idiom `/*` + `!/src` excludes every root entry, and treating
// that as final would ignore the whole project, src included.
func (w *Watcher) negationBelow(dir []string) bool {
	if !w.anchoredNegation {
		return false
	}
	for i := range w.rules {
		if r := &w.rules[i]; r.negate && r.anchored && couldMatchBelow(r.segs, dir) {
			return true
		}
	}
	return false
}

// gitIgnored applies the rules to a root-relative slash path. Like git, each
// ancestor is decided first (last matching rule wins) and an excluded
// ancestor excludes everything below it — unless a negation could still
// re-include something under it, in which case its descendants inherit
// "excluded" but remain individually re-includable.
func (w *Watcher) gitIgnored(norm string) bool {
	if len(w.rules) == 0 {
		return false
	}
	if foldIgnoreCase {
		norm = strings.ToLower(norm)
	}
	segs := strings.Split(norm, "/")
	inherited := false
	for k := 1; k <= len(segs); k++ {
		prefix := segs[:k]
		excluded := inherited
		for i := len(w.rules) - 1; i >= 0; i-- {
			if w.rules[i].matches(prefix) {
				excluded = !w.rules[i].negate
				break
			}
		}
		if !excluded {
			inherited = false
			continue
		}
		if !w.negationBelow(prefix) {
			return true
		}
		if k == len(segs) {
			// An excluded directory that still holds re-includable paths
			// stays watched; core ignores directory events anyway.
			return false
		}
		inherited = true
	}
	return false
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
	return w.addRecursiveFiles(root, nil)
}

// addRecursiveFiles is addRecursive that also reports every non-ignored
// regular file it passes to onFile (when non-nil). A directory's watch is
// registered before its entries are read, so any file created after the
// registration raises its own event and any file created before it is
// listed here — together nothing inside a newly created tree is missed.
func (w *Watcher) addRecursiveFiles(root string, onFile func(string)) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			log.Printf("watcher: skip %s: %v", p, err)
			return nil
		}
		if d == nil {
			return nil
		}
		if !d.IsDir() {
			if onFile != nil && d.Type().IsRegular() && !w.pathIgnored(p) {
				onFile(p)
			}
			return nil
		}
		if w.pathIgnored(p) {
			return filepath.SkipDir
		}
		if err := w.fs.Add(p); err != nil {
			log.Printf("watcher: cannot watch %s: %v", p, err)
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
	baseLower := strings.ToLower(filepath.Base(scoped))

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

	// 2. .gitignore rules, in order, with negation. Root-anchored rules
	// match only as located from the watched root — never a nested tree of
	// the same name ("/*.secret" ignores app.secret but not sub/app.secret).
	return w.gitIgnored(norm)
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

	// discovered carries files found inside newly created directories back
	// into this loop, so they go through the same debounce as real events.
	// runDone unblocks those walkers when Run returns for any reason.
	discovered := make(chan string, 256)
	runDone := make(chan struct{})
	defer close(runDone)

	defer func() {
		pendingMu.Lock()
		for _, e := range pending {
			if e != nil && e.timer != nil {
				e.timer.Stop()
			}
		}
		pendingMu.Unlock()
	}()

	schedule := func(path string) {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		if entry := pending[path]; entry != nil {
			entry.deadline = time.Now().Add(w.debounce)
			entry.timer.Reset(w.debounce)
			return
		}
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
			select {
			case w.handleSem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-w.handleSem }()
			w.cfg.Engine.HandleFileChange(ctx, path)
		})
		pending[path] = entry
	}

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
					dir := ev.Name
					go func() {
						// Files that landed in the tree before its watch was
						// registered (mkdir -p && write, unzip, a directory
						// renamed in) raise no event of their own.
						err := w.addRecursiveFiles(dir, func(file string) {
							select {
							case discovered <- file:
							case <-runDone:
							case <-ctx.Done():
							}
						})
						if err != nil {
							log.Printf("watcher: addRecursive %s: %v", dir, err)
						}
					}()
				}
			}
			if !isRelevant(ev.Op) {
				continue
			}
			schedule(ev.Name)
		case file := <-discovered:
			schedule(file)
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
