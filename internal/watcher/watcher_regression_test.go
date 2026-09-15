package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func newIgnoreWatcher(t *testing.T, gitignore string) (*Watcher, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	w, err := New(Config{Dir: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, root
}

// TestPathIgnored_GitignoreWhitelistNegation pins `!` negation with
// last-match-wins. The whitelist idiom `/*` + `!/src` used to ignore the
// entire project: negations were read as ordinary patterns ("!/src" matched
// nothing) and `/*` excluded every root entry, src included, so addRecursive
// never registered a single directory below the root.
func TestPathIgnored_GitignoreWhitelistNegation(t *testing.T) {
	w, root := newIgnoreWatcher(t, "/*\n!/src\n!/.gitignore\n*.log\n!keep.log\n!/lib/\n/lib/*\n!/lib/core/\n")
	for rel, want := range map[string]bool{
		"src":                 false,
		"src/main.go":         false,
		"src/debug.log":       true,
		"src/keep.log":        false,
		"README.md":           true,
		"docs/guide.md":       true,
		"lib":                 false,
		"lib/core":            false,
		"lib/core/x.go":       false,
		"lib/other":           true,
		"lib/other/y.go":      true,
		"lib/top.go":          true,
		"generated/deep/z.go": true,
	} {
		if got := w.pathIgnored(filepath.Join(root, filepath.FromSlash(rel))); got != want {
			t.Errorf("pathIgnored(%q) = %v, want %v", rel, got, want)
		}
	}
}

// TestNew_GitignoreWhitelistRegistersWhitelistedTree is the integration half:
// the re-included directory must actually be watched.
func TestNew_GitignoreWhitelistRegistersWhitelistedTree(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"src/pkg", "other"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/*\n!/src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := New(Config{Dir: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	watched := map[string]bool{}
	for _, p := range w.fs.WatchList() {
		watched[filepath.Clean(p)] = true
	}
	for _, want := range []string{"src", filepath.Join("src", "pkg")} {
		if !watched[filepath.Join(root, want)] {
			t.Errorf("%s not watched; watch list = %v", want, w.fs.WatchList())
		}
	}
	if watched[filepath.Join(root, "other")] {
		t.Errorf("other/ is excluded by /* and must not be watched")
	}
}

// TestPathIgnored_GitignoreSlashAndDoubleStarPatterns pins gitignore
// semantics for patterns containing `/` or `**`. Previously an unanchored
// glob was only ever matched against the base name and literals were
// compared as raw strings, so `docs/*.md` and `**/gen` never matched.
func TestPathIgnored_GitignoreSlashAndDoubleStarPatterns(t *testing.T) {
	w, root := newIgnoreWatcher(t, "**/gen\ndocs/*.md\nassets/**\na/**/b.txt\n")
	for rel, want := range map[string]bool{
		"gen/x.ts":         true,
		"a/gen/x.ts":       true,
		"a/b/gen":          true,
		"generated/x.ts":   false,
		"docs/readme.md":   true,
		"docs/sub/deep.md": false, // `*` never crosses a slash
		"sub/docs/x.md":    false, // a middle slash anchors to the root
		"assets":           false, // trailing /** is everything INSIDE
		"assets/img/a.png": true,
		"a/b.txt":          true,
		"a/x/y/b.txt":      true,
		"c/a/b.txt":        false,
	} {
		if got := w.pathIgnored(filepath.Join(root, filepath.FromSlash(rel))); got != want {
			t.Errorf("pathIgnored(%q) = %v, want %v", rel, got, want)
		}
	}
}

// TestPathIgnored_GitignoreCaseFoldingIsConsistent pins one case rule for
// every pattern shape. Literals used to fold on every OS while globs never
// did, so `LOGS` ignored logs/ but `*.LOG` did not ignore a.log.
func TestPathIgnored_GitignoreCaseFoldingIsConsistent(t *testing.T) {
	// The directory must not be a DefaultIgnoreDirs entry ("out" is one, and
	// that set folds case on every OS), or the gitignore rule is never reached.
	w, root := newIgnoreWatcher(t, "LOGS\n*.LOG\n/Stage/*.TMP\n")
	for _, rel := range []string{"logs/x.txt", "a.log", "Stage/f.tmp"} {
		if got := w.pathIgnored(filepath.Join(root, filepath.FromSlash(rel))); got != foldIgnoreCase {
			t.Errorf("pathIgnored(%q) = %v, want %v (foldIgnoreCase)", rel, got, foldIgnoreCase)
		}
	}
	for _, rel := range []string{"LOGS/x.txt", "A.LOG", "Stage/F.TMP"} {
		if !w.pathIgnored(filepath.Join(root, filepath.FromSlash(rel))) {
			t.Errorf("pathIgnored(%q) = false, want exact-case match ignored", rel)
		}
	}
}

// TestRun_FilesInsideNewDirectoryAreDelivered pins that files already
// present when a directory appears reach the handler. The new-directory
// walk only registered watches, so files created before the watch existed
// (mkdir -p && write, unzip, or — deterministically here — a populated tree
// renamed into the root) raised no event and never produced a diff.
func TestRun_FilesInsideNewDirectoryAreDelivered(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(filepath.Join(staging, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.go", filepath.Join("sub", "b.go")} {
		if err := os.WriteFile(filepath.Join(staging, f), []byte("package pkg\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, h := startWatcher(t, root, 50*time.Millisecond, nil)
	time.Sleep(100 * time.Millisecond) // let Run start consuming

	if err := os.Rename(staging, filepath.Join(root, "pkg")); err != nil {
		t.Skipf("rename across temp dirs unsupported here: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool {
		return h.countFor("a.go") >= 1 && h.countFor("b.go") >= 1
	}, "files inside a directory that appeared populated never reached the handler")
}

// TestRun_DiscoveredFilesSkipIgnoredAndDirectories pins the synthetic path
// through the injected Create of a directory: only regular, non-ignored
// files are fed back.
func TestRun_DiscoveredFilesSkipIgnoredAndDirectories(t *testing.T) {
	root := t.TempDir()
	h := &recordingHandler{}
	w, err := New(Config{Dir: root, Engine: h, Debounce: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = w.Close()
	})
	// Build the tree BEFORE Run so fsnotify has nothing real to report for it
	// (the root watch predates it only for the top-level dir, which we inject).
	dir := filepath.Join(root, "fresh")
	for _, d := range []string{"inner", "node_modules"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"top.go", filepath.Join("inner", "in.go"), filepath.Join("node_modules", "dep.js")} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	go w.Run(ctx)
	w.fs.Events <- fsnotify.Event{Name: dir, Op: fsnotify.Create}

	waitFor(t, 3*time.Second, func() bool {
		return h.countFor("top.go") >= 1 && h.countFor("in.go") >= 1
	}, "discovered files never delivered")
	waitQuiet(t, 300*time.Millisecond, func() bool {
		return h.countFor("dep.js") > 0
	}, "a file inside an ignored directory was fed back as a discovered file")

	// The feed itself carries regular, non-ignored files only — never the
	// directories (real directory Create events keep their existing path).
	var mu sync.Mutex
	got := map[string]bool{}
	if err := w.addRecursiveFiles(dir, func(p string) {
		mu.Lock()
		got[p] = true
		mu.Unlock()
	}); err != nil {
		t.Fatalf("addRecursiveFiles: %v", err)
	}
	want := map[string]bool{
		filepath.Join(dir, "top.go"):         true,
		filepath.Join(dir, "inner", "in.go"): true,
	}
	if len(got) != len(want) {
		t.Fatalf("discovered = %v, want exactly %v", got, want)
	}
	for p := range want {
		if !got[p] {
			t.Errorf("discovered set missing %s: %v", p, got)
		}
	}
}

// blockingHandler records the peak number of concurrent HandleFileChange
// calls, holding each one until released.
type blockingHandler struct {
	cur, peak atomic.Int32
	done      atomic.Int32
	release   chan struct{}
	once      sync.Once
}

func (h *blockingHandler) HandleFileChange(ctx context.Context, _ string) {
	n := h.cur.Add(1)
	for {
		p := h.peak.Load()
		if n <= p || h.peak.CompareAndSwap(p, n) {
			break
		}
	}
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	h.cur.Add(-1)
	h.done.Add(1)
}

// TestRun_HandleFileChangeConcurrencyIsBounded pins the handler semaphore.
// Every debounce timer fires on its own goroutine, so a storm of distinct
// paths used to run that many HandleFileChange calls (read + parse + diff)
// simultaneously.
func TestRun_HandleFileChangeConcurrencyIsBounded(t *testing.T) {
	root := t.TempDir()
	h := &blockingHandler{release: make(chan struct{})}
	w, err := New(Config{Dir: root, Engine: h, Debounce: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const limit = 2
	w.handleSem = make(chan struct{}, limit)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		h.once.Do(func() { close(h.release) })
		cancel()
		_ = w.Close()
	})
	go w.Run(ctx)

	const paths = 12
	for i := 0; i < paths; i++ {
		w.fs.Events <- fsnotify.Event{Name: filepath.Join(root, "f"+string(rune('a'+i))+".go"), Op: fsnotify.Write}
	}
	waitFor(t, 3*time.Second, func() bool { return h.cur.Load() == limit }, "handlers never started")
	time.Sleep(200 * time.Millisecond) // every timer has fired by now
	if p := h.peak.Load(); p > limit {
		t.Fatalf("peak concurrent HandleFileChange = %d, want <= %d", p, limit)
	}
	h.once.Do(func() { close(h.release) })
	waitFor(t, 3*time.Second, func() bool { return h.done.Load() == paths },
		"queued handlers did not all run after release")
	if p := h.peak.Load(); p > limit {
		t.Fatalf("peak concurrent HandleFileChange = %d, want <= %d", p, limit)
	}
}
