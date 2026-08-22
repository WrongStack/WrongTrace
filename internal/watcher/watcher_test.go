package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ---------------------------------------------------------------
// fakes & helpers
// ---------------------------------------------------------------

// recordingHandler captures HandleFileChange invocations. The watcher fires
// them from AfterFunc timer goroutines, so access must be synchronized.
type recordingHandler struct {
	mu    sync.Mutex
	calls []string
}

func (h *recordingHandler) HandleFileChange(_ context.Context, path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, path)
}

func (h *recordingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// countFor counts calls whose path ends with base (robust against platform
// differences in how fsnotify reports the watched root).
func (h *recordingHandler) countFor(base string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, p := range h.calls {
		if strings.HasSuffix(filepath.ToSlash(p), "/"+base) || filepath.Base(p) == base {
			n++
		}
	}
	return n
}

// snapshot returns a copy of the recorded calls under lock, safe to print
// from a failing assertion while timer goroutines may still append.
func (h *recordingHandler) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.calls))
	copy(out, h.calls)
	return out
}

// counts returns the total call count and per-name counts computed under a
// SINGLE lock hold. Multi-count assertions must use this rather than
// separate count()/countFor() calls: timer goroutines can append between
// independent acquisitions, making total exceed the per-name snapshot and
// producing false-positive failures under -race.
func (h *recordingHandler) counts(names ...string) (int, map[string]int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	per := make(map[string]int, len(names))
	for _, p := range h.calls {
		for _, n := range names {
			if strings.HasSuffix(filepath.ToSlash(p), "/"+n) || filepath.Base(p) == n {
				per[n]++
			}
		}
	}
	return len(h.calls), per
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal(msg)
}

// waitQuiet asserts cond NEVER holds within the window (for "must not be
// called" checks).
func waitQuiet(t *testing.T, window time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if cond() {
			t.Fatal(msg)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// startWatcher builds a watcher over dir with the given debounce and a fresh
// recording handler, starts Run, and registers cleanup.
func startWatcher(t *testing.T, dir string, debounce time.Duration, ignoreDirs []string) (*Watcher, *recordingHandler) {
	t.Helper()
	h := &recordingHandler{}
	w, err := New(Config{Dir: dir, Engine: h, Debounce: debounce, IgnoreDirs: ignoreDirs})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = w.Close()
	})
	go w.Run(ctx)
	return w, h
}

// burst writes n versions of the file spaced gap apart, each with distinct
// content so every write is a real modification.
func burst(t *testing.T, path string, n int, gap time.Duration) {
	t.Helper()
	for i := 0; i < n; i++ {
		content := fmt.Sprintf("// version %d\nfunc F() int { return %d }\n", i, i)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s (v%d): %v", path, i, err)
		}
		if i < n-1 {
			time.Sleep(gap)
		}
	}
}

// ---------------------------------------------------------------
// construction defaults
// ---------------------------------------------------------------

func TestNewDefaults(t *testing.T) {
	w, err := New(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if w.debounce != 250*time.Millisecond {
		t.Errorf("default debounce = %v, want 250ms", w.debounce)
	}
	set := map[string]bool{}
	for _, d := range w.cfg.IgnoreDirs {
		set[d] = true
	}
	for _, want := range []string{".git", "node_modules", "vendor", "dist", "build", "target"} {
		if !set[want] {
			t.Errorf("default IgnoreDirs missing %q: %v", want, w.cfg.IgnoreDirs)
		}
	}
}

func TestNewCustomConfig(t *testing.T) {
	w, err := New(Config{
		Dir:        t.TempDir(),
		Debounce:   75 * time.Millisecond,
		IgnoreDirs: []string{"gen"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if w.debounce != 75*time.Millisecond {
		t.Errorf("debounce = %v, want 75ms", w.debounce)
	}
	// Custom IgnoreDirs REPLACE the defaults, they are not appended.
	if len(w.cfg.IgnoreDirs) != 1 || w.cfg.IgnoreDirs[0] != "gen" {
		t.Errorf("IgnoreDirs = %v, want exactly [gen] (replacement semantics)", w.cfg.IgnoreDirs)
	}
}

// ---------------------------------------------------------------
// path relevance (fsnotify op filtering)
// ---------------------------------------------------------------

func TestIsRelevant(t *testing.T) {
	cases := []struct {
		op   fsnotify.Op
		want bool
	}{
		{fsnotify.Create, true},
		{fsnotify.Write, true},
		{fsnotify.Remove, true},
		{fsnotify.Rename, true},
		{fsnotify.Chmod, false},
		{fsnotify.Op(0), false},
		{fsnotify.Chmod | fsnotify.Write, true}, // any relevant bit wins
		{fsnotify.Create | fsnotify.Chmod, true},
	}
	for _, c := range cases {
		if got := isRelevant(c.op); got != c.want {
			t.Errorf("isRelevant(%v) = %v, want %v", c.op, got, c.want)
		}
	}
}

// ---------------------------------------------------------------
// ignore-dir filtering
// ---------------------------------------------------------------

func TestPathIgnored_Defaults(t *testing.T) {
	w, err := New(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join("repo", "src", "main.go"), false},
		{filepath.Join("repo", "node_modules", "pkg", "index.js"), true},
		{filepath.Join("repo", "web", "node_modules", "dep", "d.js"), true}, // nested
		{filepath.Join("repo", ".git", "HEAD"), true},
		{filepath.Join("repo", "vendor", "x.go"), true},
		{filepath.Join("repo", "dist", "app.js"), true},
		{filepath.Join("repo", "build", "out.bin"), true},
		{filepath.Join("repo", "target", "debug", "x"), true},
		{filepath.Join("repo", ".wrongtrace", "db.sqlite"), true},
		// Segment must match EXACTLY — prefix names must not trip the filter.
		{filepath.Join("repo", "my_node_modules", "x.js"), false},
		{filepath.Join("repo", "distUtils", "a.js"), false},
		{filepath.Join("repo", ".gitignore"), false}, // file, not the .git dir
	}
	for _, c := range cases {
		if got := w.pathIgnored(c.path); got != c.want {
			t.Errorf("pathIgnored(%q) = %v, want %v", c.path, got, c.want)
		}
	}

	// Documents current behavior: a FILE literally named like an ignore dir
	// is also filtered, because matching is per path segment.
	if !w.pathIgnored(filepath.Join("repo", "dist")) {
		t.Error("pathIgnored should filter a path whose final segment is an ignore name (current behavior)")
	}
}

func TestPathIgnored_Custom(t *testing.T) {
	w, err := New(Config{Dir: t.TempDir(), IgnoreDirs: []string{"gen", "__snapshots__"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join("p", "gen", "a.go"), true},
		{filepath.Join("p", "src", "__snapshots__", "x.snap"), true},
		{filepath.Join("p", "node_modules", "x.js"), false}, // defaults replaced
		{filepath.Join("p", ".git", "HEAD"), false},         // defaults replaced
		{filepath.Join("p", "gen.go"), false},
	}
	for _, c := range cases {
		if got := w.pathIgnored(c.path); got != c.want {
			t.Errorf("pathIgnored(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestIgnoredDirs_NeverDeliverEvents is the integration half of ignore
// filtering: node_modules is skipped at registration time (fsnotify never
// watches it), so writes inside it must never reach the handler — while a
// control file in the watched root does.
func TestIgnoredDirs_NeverDeliverEvents(t *testing.T) {
	root := t.TempDir()
	// Directory tree must exist BEFORE New so addRecursive can skip it.
	ignored := filepath.Join(root, "node_modules", "left-pad")
	if err := os.MkdirAll(ignored, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	_, h := startWatcher(t, root, 100*time.Millisecond, nil)

	// Control: a watched file reaches the handler exactly once.
	keep := filepath.Join(root, "keep.go")
	burst(t, keep, 3, 20*time.Millisecond)
	waitFor(t, 2*time.Second, func() bool { return h.countFor("keep.go") == 1 },
		"control file never reached the handler")

	// Writes into ignored trees must produce nothing.
	dep := filepath.Join(ignored, "index.js")
	burst(t, dep, 4, 20*time.Millisecond)
	gitCfg := filepath.Join(root, ".git", "config")
	burst(t, gitCfg, 3, 20*time.Millisecond)

	waitQuiet(t, 600*time.Millisecond,
		func() bool { return h.countFor("index.js") > 0 },
		"event from node_modules reached the handler")
	waitQuiet(t, 300*time.Millisecond,
		func() bool { return h.countFor("config") > 0 },
		"event from .git reached the handler")

	// Sanity: the ignored writes really happened on disk.
	for _, p := range []string{dep, gitCfg} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("sanity: %s missing: %v", p, err)
		}
	}
	// The contract under test is that ONLY the control file is delivered:
	// node_modules and .git are excluded at registration time, so fsnotify
	// cannot even generate their events. The control file's exact call count
	// is timing-dependent — on a loaded CI runner under -race, event delivery
	// can lag past the debounce window and split the burst into two coalesced
	// calls — so assert it fired, not how often.
	if got := h.countFor("keep.go"); got < 1 {
		t.Errorf("control file never reached the handler: %v", h.snapshot())
	}
	// Single-lock multi-count: separate count()/countFor() calls can be
	// interleaved by timer goroutines, making total exceed the per-name
	// snapshot under -race.
	if total, per := h.counts("keep.go"); total != per["keep.go"] {
		t.Errorf("handler saw %d calls but only %d were for the control file — an ignored dir leaked: %v",
			total, per["keep.go"], h.snapshot())
	}
}

// ---------------------------------------------------------------
// debounce coalescing
// ---------------------------------------------------------------

// TestDebounce_CoalescesBurstIntoSingleCall is the core contract: many
// events on one path within the debounce window collapse into exactly one
// HandleFileChange invocation, fired after the last event settles.
func TestDebounce_CoalescesBurstIntoSingleCall(t *testing.T) {
	root := t.TempDir()
	_, h := startWatcher(t, root, 120*time.Millisecond, nil)

	f := filepath.Join(root, "hot.go")
	// 5 writes, 25ms apart — every event resets the 120ms timer.
	burst(t, f, 5, 25*time.Millisecond)

	waitFor(t, 2*time.Second, func() bool { return h.countFor("hot.go") >= 1 },
		"coalesced event never fired")

	// Real-filesystem burst counts are SOFT: under -race on a loaded runner,
	// fsnotify delivery can lag past the debounce window and split one burst
	// more than once — CI observed 3 calls for a 5-write burst on a commit
	// that touched no Go code (c02d712, fifth flake of this family). The
	// floor proves delivery happened; the ceiling is the number of writes,
	// since worst-case delivery (every event lands after the previous window
	// expired) is one call per write, and MORE calls than writes means
	// duplicate handling — a genuine bug. Coalescing QUALITY is enforced
	// deterministically by TestDebounce_SyntheticBurstFiresExactlyOnce.
	time.Sleep(2*120*time.Millisecond + 100*time.Millisecond)
	if n := h.countFor("hot.go"); n < 1 || n > 5 {
		t.Errorf("burst produced %d calls, want 1-5 (floor: delivered; ceiling: one per write — more means duplicate handling): %v", n, h.snapshot())
	}
	// Single-lock multi-count: every handler call must belong to hot.go.
	if total, per := h.counts("hot.go"); total != per["hot.go"] || total > 5 {
		t.Errorf("total handler calls = %d (%d for hot.go), want them equal and at most 5: %v",
			total, per["hot.go"], h.snapshot())
	}
}

// TestDebounce_SyntheticBurstFiresExactlyOnce enforces the EXACT coalescing
// contract deterministically. Real-filesystem tests cannot: under -race on a
// loaded runner, fsnotify delivery can lag past the debounce window and split
// one burst (the failure mode behind 91bef3d, 3ba8166, e1f7116), so the
// integration test only bounds the count to 1-2. Here events are injected
// directly into the watcher's fsnotify channel — no kernel delivery, no lag —
// so one burst MUST fire exactly once, and a second burst after the window
// closed MUST re-arm to exactly two total. A regression that always fires
// twice per burst fails here even though it passes the bounded test.
func TestDebounce_SyntheticBurstFiresExactlyOnce(t *testing.T) {
	root := t.TempDir()
	h := &recordingHandler{}
	w, err := New(Config{Dir: root, Engine: h, Debounce: 120 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = w.Close()
	})
	go w.Run(ctx)

	name := filepath.Join(root, "hot.go")
	inject := func(n int, gap time.Duration) {
		for i := 0; i < n; i++ {
			w.fs.Events <- fsnotify.Event{Name: name, Op: fsnotify.Write}
			if i < n-1 {
				time.Sleep(gap)
			}
		}
	}

	// Burst one: 6 events well inside the window — every one resets the
	// timer, so exactly one call may fire, after the last settles.
	inject(6, 20*time.Millisecond)
	waitFor(t, 2*time.Second, func() bool { return h.countFor("hot.go") == 1 },
		"synthetic burst never fired")

	// Quiet window: nothing else may arrive. Exact, because no further
	// events exist and no kernel delivery can inject lag.
	time.Sleep(2*120*time.Millisecond + 100*time.Millisecond)
	if n := h.countFor("hot.go"); n != 1 {
		t.Fatalf("first burst fired %d times, want exactly 1: %v", n, h.snapshot())
	}

	// Second burst after the window closed: the timer must have re-armed,
	// and the total must be exactly two.
	inject(4, 20*time.Millisecond)
	waitFor(t, 2*time.Second, func() bool { return h.countFor("hot.go") == 2 },
		"second synthetic burst never fired")
	time.Sleep(2*120*time.Millisecond + 100*time.Millisecond)
	if total, per := h.counts("hot.go"); total != 2 || per["hot.go"] != 2 {
		t.Errorf("after two bursts: total=%d hot.go=%d, want exactly 2/2: %v",
			total, per["hot.go"], h.snapshot())
	}
}

// TestDebounce_SeparateBurstsProduceSeparateCalls pins the window boundary:
// bursts separated by more than the debounce each get their own call.
func TestDebounce_SeparateBurstsProduceSeparateCalls(t *testing.T) {
	root := t.TempDir()
	_, h := startWatcher(t, root, 100*time.Millisecond, nil)

	f := filepath.Join(root, "twice.go")
	burst(t, f, 3, 20*time.Millisecond)
	waitFor(t, 2*time.Second, func() bool { return h.countFor("twice.go") >= 1 },
		"first burst never fired")

	// The first call firing proves the window closed; a new burst starts a
	// fresh timer.
	burst(t, f, 3, 20*time.Millisecond)
	waitFor(t, 2*time.Second, func() bool { return h.countFor("twice.go") >= 2 },
		"second burst never registered after the first")

	// Floor + ceiling: the window-boundary contract is that each
	// debounce-separated burst registers its own call. On a loaded runner
	// fsnotify delivery can lag past the debounce window and split a burst
	// more than once (CI observed 3 calls for a 5-write burst — c02d712), so
	// the ceiling is the total write count (worst case: one call per write);
	// MORE calls than writes means duplicate handling, a genuine bug. Exact
	// single-call-per-burst coalescing stays covered deterministically by
	// TestDebounce_SyntheticBurstFiresExactlyOnce.
	time.Sleep(2*100*time.Millisecond + 100*time.Millisecond)
	if n := h.countFor("twice.go"); n < 2 || n > 6 {
		t.Errorf("separate bursts produced %d calls, want 2-6 (floor: each window registers; ceiling: one per write — more means duplicate handling): %v", n, h.snapshot())
	}
}

// TestDebounce_IndependentPaths: per-path timers mean interleaved writes to
// two files each coalesce to their own single call.
func TestDebounce_IndependentPaths(t *testing.T) {
	root := t.TempDir()
	_, h := startWatcher(t, root, 120*time.Millisecond, nil)

	a := filepath.Join(root, "a.go")
	b := filepath.Join(root, "b.go")
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(a, []byte(fmt.Sprintf("// a v%d\n", i)), 0o644); err != nil {
			t.Fatalf("write a: %v", err)
		}
		if err := os.WriteFile(b, []byte(fmt.Sprintf("// b v%d\n", i)), 0o644); err != nil {
			t.Fatalf("write b: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}

	waitFor(t, 2*time.Second, func() bool {
		return h.countFor("a.go") >= 1 && h.countFor("b.go") >= 1
	}, "interleaved paths never fired")

	// Floor assertions: on a loaded runner a path's burst can split into two
	// coalesced calls (fsnotify delivery lag past the debounce window),
	// which made the previous ==1 condition unsatisfiable — the test then
	// timed out even though every event had fired. The contract here is
	// independence: both paths fired despite interleaved writes, and every
	// handler call belongs to exactly one of them.
	time.Sleep(2*120*time.Millisecond + 100*time.Millisecond)
	if got := h.countFor("a.go"); got < 1 {
		t.Errorf("a.go saw %d calls, want at least 1: %v", got, h.snapshot())
	}
	if got := h.countFor("b.go"); got < 1 {
		t.Errorf("b.go saw %d calls, want at least 1: %v", got, h.snapshot())
	}
	if total, per := h.counts("a.go", "b.go"); total != per["a.go"]+per["b.go"] {
		t.Errorf("total calls = %d, want only a.go+b.go (%d): %v", total, per["a.go"]+per["b.go"], h.snapshot())
	}
}

// TestRun_WithoutEngineParksQuietly documents that a nil handler makes Run
// idle instead of crashing — New is still usable for pure path filtering.
func TestRun_WithoutEngineParksQuietly(t *testing.T) {
	root := t.TempDir()
	w, err := New(Config{Dir: root}) // Engine nil
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	burst(t, filepath.Join(root, "x.go"), 2, 10*time.Millisecond)
	time.Sleep(250 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("Run exited before cancellation")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after cancellation")
	}
}
