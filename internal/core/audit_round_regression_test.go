package core

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// isolateAuditEnv points every home-derived path the engine reads (state
// home, user home for agent session discovery, APPDATA) at a temp fixture so
// no test touches the real machine's directories.
func isolateAuditEnv(t *testing.T) string {
	t.Helper()
	fixture := t.TempDir()
	home := filepath.Join(fixture, "home")
	t.Setenv("WRONGTRACE_HOME", filepath.Join(fixture, "wt"))
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(fixture, "appdata"))
	return fixture
}

func withBirthTime(t *testing.T, fn func(string, os.FileInfo) (time.Time, bool)) {
	t.Helper()
	old := fileBirthTimeHook
	fileBirthTimeHook = fn
	t.Cleanup(func() { fileBirthTimeHook = old })
}

func birthAgo(d time.Duration) func(string, os.FileInfo) (time.Time, bool) {
	return func(string, os.FileInfo) (time.Time, bool) { return time.Now().Add(-d), true }
}

func actionCounts(t *testing.T, store *db.Store) map[string]int {
	t.Helper()
	evs, err := store.RecentEvents(500)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	out := map[string]int{}
	for _, ev := range evs {
		out[ev.Action]++
	}
	return out
}

const auditThreeFuncs = "package x\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n\nfunc C() int { return 3 }\n"

// ---------------------------------------------------------------------------
// 1. Atlas cache aliasing
// ---------------------------------------------------------------------------

// TestAtlas_CallerMutationDoesNotCorruptCache pins that Atlas hands out slices
// the caller owns. The handler's include_symbols=false path sets
// pkg.Files[i].Symbols = nil on the returned value; with shared backing arrays
// that erased symbols from the cached snapshot for every later request.
func TestAtlas_CallerMutationDoesNotCorruptCache(t *testing.T) {
	e, _, _ := newAtlasTestEngine(t)
	dir := t.TempDir()
	writeFixture(t, dir, "x/a.go", auditThreeFuncs)
	e.PrimeDirectory(dir)

	for round := 0; round < 2; round++ { // round 0 = fresh build, round 1 = cache hit
		s, err := e.Atlas("x")
		if err != nil {
			t.Fatalf("atlas: %v", err)
		}
		if len(s.Packages) == 0 || len(s.Packages[0].Files) == 0 {
			t.Fatalf("round %d: no packages/files in atlas", round)
		}
		pkg := s.Packages[0]
		for i := range pkg.Files {
			pkg.Files[i].Symbols = nil
		}
		s.Packages[0].Files = nil
	}
	s, err := e.Atlas("x")
	if err != nil {
		t.Fatalf("atlas: %v", err)
	}
	if len(s.Packages) == 0 || len(s.Packages[0].Files) == 0 || len(s.Packages[0].Files[0].Symbols) != 3 {
		t.Fatalf("cached atlas was mutated through a returned snapshot: %+v", s.Packages)
	}
}

// ---------------------------------------------------------------------------
// 2. Same-named projects
// ---------------------------------------------------------------------------

func TestGetActiveProject_SameNameResolvesByTrackedID(t *testing.T) {
	isolateAuditEnv(t)
	e := NewEngine(Config{})
	a, err := e.AddProject("api", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.AddProject("api", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []ProjectProfile{b, a, b} {
		if _, err := e.SwitchActiveProject(target.ID); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 200; i++ {
			if p := e.GetActiveProject(); p == nil || p.ID != target.ID {
				t.Fatalf("GetActiveProject returned %+v, want the switched-to same-named project %s", p, target.ID)
			}
			if p, ok := e.resolveProject("api"); !ok || p.ID != target.ID {
				t.Fatalf("resolveProject(api) = %s, want active %s", p.ID, target.ID)
			}
		}
	}
	closeActiveStore(t, e)
}

func TestResolveProjectRef_Deterministic(t *testing.T) {
	projects := map[string]ProjectProfile{
		"proj-b": {ID: "proj-b", Name: "api"},
		"proj-a": {ID: "proj-a", Name: "API"},
		"proj-c": {ID: "proj-c", Name: "api", IsActive: true},
		"proj-d": {ID: "proj-d", Name: "web"},
	}
	cases := []struct {
		ref, prefer, want string
	}{
		{"proj-d", "", "proj-d"}, // exact ID
		{"PROJ-D", "", "proj-d"}, // case-insensitive ID
		{"api", "proj-b", "proj-b"},
		{"api", "", "proj-c"},       // IsActive among same-named
		{"api", "proj-d", "proj-c"}, // prefer not among matches
	}
	for _, tc := range cases {
		for i := 0; i < 50; i++ {
			p, ok := resolveProjectRef(projects, tc.ref, tc.prefer)
			if !ok || p.ID != tc.want {
				t.Fatalf("resolveProjectRef(%q, %q) = %q, want %q", tc.ref, tc.prefer, p.ID, tc.want)
			}
		}
	}
	delete(projects, "proj-c")
	for i := 0; i < 50; i++ {
		if p, _ := resolveProjectRef(projects, "api", ""); p.ID != "proj-a" {
			t.Fatalf("no active same-named project: got %q, want smallest ID proj-a", p.ID)
		}
	}
}

// closeActiveStore closes a store the engine switched in, so Windows TempDir
// cleanup is not blocked by an open SQLite handle.
func closeActiveStore(t *testing.T, e *Engine) {
	t.Helper()
	if st := e.Store(); st != nil {
		_ = st.Close()
	}
}

// ---------------------------------------------------------------------------
// 3. Watch root follows the active project
// ---------------------------------------------------------------------------

func TestSwitchActiveProject_UpdatesWatchRoot(t *testing.T) {
	isolateAuditEnv(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	e := NewEngine(Config{WatchDir: dirA})
	if _, err := e.AddProject("a", dirA); err != nil {
		t.Fatal(err)
	}
	b, err := e.AddProject("b", dirB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.SwitchActiveProject(b.ID); err != nil {
		t.Fatal(err)
	}
	defer closeActiveStore(t, e)
	if got, want := e.WatchRoot(), absClean(dirB); got != want {
		t.Fatalf("WatchRoot after switch = %q, want %q", got, want)
	}
	now := time.Now()
	e.RegisterFileOperation("src/x.go", "run-1", now)
	if got := e.fileOperationRunID(filepath.Join(dirB, "src", "x.go"), now); got != "run-1" {
		t.Fatalf("relative hint did not resolve against the active project root (got %q)", got)
	}
}

// ---------------------------------------------------------------------------
// 4. First sighting without a snapshot
// ---------------------------------------------------------------------------

// TestHandleFileChange_UnseenPreexistingFileBaselines pins that a file with no
// cached snapshot that already existed (old birth time) is stored as a silent
// baseline: a one-function edit must not report every declaration as ADDED.
func TestHandleFileChange_UnseenPreexistingFileBaselines(t *testing.T) {
	e, store, parser := newAtlasTestEngine(t)
	withBirthTime(t, birthAgo(24*time.Hour))
	dir := t.TempDir()
	path := writeFixture(t, dir, "x/old.go", auditThreeFuncs)
	ctx := context.Background()

	if err := os.WriteFile(path, []byte(strings.Replace(auditThreeFuncs, "return 2", "return 22", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	e.HandleFileChange(ctx, path)
	if got := actionCounts(t, store); len(got) != 0 {
		t.Fatalf("first sight of a pre-existing file emitted events %v, want a silent baseline", got)
	}
	if _, ok := parser.Snapshot(path); !ok {
		t.Fatal("baseline snapshot was not stored")
	}

	if err := os.WriteFile(path, []byte(strings.Replace(auditThreeFuncs, "return 3", "return 33", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	e.HandleFileChange(ctx, path)
	got := actionCounts(t, store)
	if got["ADDED"] != 0 || got["MODIFIED"] != 2 {
		// B reverts to 2 and C changes to 33 relative to the baseline.
		t.Fatalf("edit after baseline = %v, want exactly 2 MODIFIED and no ADDED", got)
	}
}

func TestHandleFileChange_RecentlyCreatedFileEmitsAdded(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	withBirthTime(t, birthAgo(time.Second))
	path := writeFixture(t, t.TempDir(), "x/new.go", auditThreeFuncs)
	e.HandleFileChange(context.Background(), path)
	if got := actionCounts(t, store); got["ADDED"] != 3 {
		t.Fatalf("genuinely new file events = %v, want 3 ADDED", got)
	}
}

func TestHandleFileChange_UnknownBirthTimeBaselines(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	withBirthTime(t, func(string, os.FileInfo) (time.Time, bool) { return time.Time{}, false })
	path := writeFixture(t, t.TempDir(), "x/any.go", auditThreeFuncs)
	e.HandleFileChange(context.Background(), path)
	if got := actionCounts(t, store); len(got) != 0 {
		t.Fatalf("unknown birth time emitted %v, want a silent baseline", got)
	}
}

// TestHandleFileChange_RecreateAfterDeleteEmitsAdded pins the tombstone: the
// OS may keep the old birth time on delete-then-recreate (Windows tunneling),
// yet symbols reported DELETED must come back as ADDED.
func TestHandleFileChange_RecreateAfterDeleteEmitsAdded(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	withBirthTime(t, birthAgo(24*time.Hour))
	dir := t.TempDir()
	path := writeFixture(t, dir, "x/re.go", auditThreeFuncs)
	e.PrimeDirectory(dir)
	ctx := context.Background()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	e.HandleFileChange(ctx, path)
	writeFixture(t, dir, "x/re.go", auditThreeFuncs)
	e.HandleFileChange(ctx, path)
	if got := actionCounts(t, store); got["DELETED"] != 3 || got["ADDED"] != 3 {
		t.Fatalf("delete+recreate events = %v, want 3 DELETED and 3 ADDED", got)
	}
	if n := len(e.tombstones); n != 0 {
		t.Fatalf("tombstone not consumed: %d left", n)
	}
}

// TestParseSizeLimitIsShared pins one ceiling for indexing and live parsing:
// a file above it is neither primed nor parsed on change (it used to be
// skipped by the 1MB indexer yet parsed by the 4MB/5MB live gates, which made
// its first edit report every declaration as ADDED).
func TestParseSizeLimitIsShared(t *testing.T) {
	e, store, parser := newAtlasTestEngine(t)
	withBirthTime(t, birthAgo(time.Second)) // even a "new" file must be skipped
	dir := t.TempDir()
	pad := strings.Repeat("// padding padding padding padding padding padding padding\n", maxParseFileBytes/50)
	path := writeFixture(t, dir, "x/big.go", auditThreeFuncs+pad)
	if info, _ := os.Stat(path); info.Size() <= maxParseFileBytes {
		t.Fatalf("fixture too small: %d", info.Size())
	}
	e.PrimeDirectory(dir)
	if _, ok := parser.Snapshot(path); ok {
		t.Fatal("oversized file was primed")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(auditThreeFuncs, "return 2", "return 22", 1)+pad), 0o644); err != nil {
		t.Fatal(err)
	}
	e.HandleFileChange(context.Background(), path)
	if _, ok := parser.Snapshot(path); ok {
		t.Fatal("oversized file was parsed")
	}
	if got := actionCounts(t, store); len(got) != 0 {
		t.Fatalf("oversized file emitted %v", got)
	}
}

// ---------------------------------------------------------------------------
// 5. Concurrent deliveries for one path
// ---------------------------------------------------------------------------

func TestHandleFileChange_ConcurrentSamePathEmitsOnce(t *testing.T) {
	e, store, _ := newAtlasTestEngine(t)
	dir := t.TempDir()
	path := writeFixture(t, dir, "x/c.go", auditThreeFuncs)
	e.PrimeDirectory(dir)
	if err := os.WriteFile(path, []byte(strings.Replace(auditThreeFuncs, "return 1", "return 11", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			e.HandleFileChange(context.Background(), path)
		}()
	}
	close(start)
	wg.Wait()
	if got := actionCounts(t, store); got["MODIFIED"] != 1 || len(got) != 1 {
		t.Fatalf("16 concurrent deliveries of one edit emitted %v, want exactly 1 MODIFIED", got)
	}
	if n := e.pathLocks.size(); n != 0 {
		t.Fatalf("path lock map retained %d entries after all callers finished", n)
	}
}

func TestKeyedMutex_SerializesPerKey(t *testing.T) {
	var k keyedMutex
	var inside, maxInside int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := k.lock("p")
			n := atomic.AddInt32(&inside, 1)
			for {
				m := atomic.LoadInt32(&maxInside)
				if n <= m || atomic.CompareAndSwapInt32(&maxInside, m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&inside, -1)
			unlock()
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("max concurrent holders = %d, want 1", maxInside)
	}
	if k.size() != 0 {
		t.Fatalf("keyedMutex retained %d entries", k.size())
	}
}

// ---------------------------------------------------------------------------
// 6. Prime job cancelled before the switch Reset
// ---------------------------------------------------------------------------

func TestSwitchActiveProject_CancelsPrimeJobBeforeReset(t *testing.T) {
	isolateAuditEnv(t)
	parser, err := ast.NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer parser.Close()
	e := NewEngine(Config{AST: parser})
	if _, err := e.AddProject("a", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	b, err := e.AddProject("b", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// AddProject primes the first (active) project asynchronously; let that
	// job finish so it cannot be the one cancelling the fake job below.
	deadline := time.Now().Add(5 * time.Second)
	for e.IndexStatus().LastIndexedAt.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("initial prime never completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Blank the target path so the switch launches no new PrimeDirectory:
	// only SwitchActiveProject itself can cancel the stale job.
	e.lockMu.Lock()
	pb := e.projects[b.ID]
	pb.Path = ""
	e.projects[b.ID] = pb
	e.lockMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.primeMu.Lock()
	e.primeCancel = cancel
	e.primeMu.Unlock()

	if _, err := e.SwitchActiveProject(b.ID); err != nil {
		t.Fatal(err)
	}
	defer closeActiveStore(t, e)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("SwitchActiveProject did not cancel the previous project's indexing job")
	}
}

// ---------------------------------------------------------------------------
// 7. Editor agent session discovery: path boundaries, not substrings
// ---------------------------------------------------------------------------

func auditFileURI(p string) string {
	s := filepath.ToSlash(p)
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return "file://" + strings.Replace(s, ":", "%3A", 1)
}

func auditWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanAgentSessions_EditorAgentsMatchByPathBoundary(t *testing.T) {
	fixture := isolateAuditEnv(t)
	home := filepath.Join(fixture, "home")
	appData := filepath.Join(fixture, "appdata")
	root := filepath.Join(fixture, "ws", "WrongTrace")
	sibling := root + "-docs"
	sub := filepath.Join(root, "web")
	for _, d := range []string{root, sibling, sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wsJSON := func(folder string) []byte {
		b, _ := json.Marshal(map[string]string{"folder": folder})
		return b
	}

	// Cursor / Windsurf / Trae workspaceStorage.
	for _, app := range []string{"Cursor", "Windsurf", "Trae"} {
		storage := filepath.Join(appData, app, "User", "workspaceStorage")
		auditWrite(t, filepath.Join(storage, "own", "workspace.json"), wsJSON(strings.ToUpper(auditFileURI(root)[:9])+auditFileURI(root)[9:]))
		auditWrite(t, filepath.Join(storage, "sub", "workspace.json"), wsJSON(auditFileURI(sub)))
		auditWrite(t, filepath.Join(storage, "sibling", "workspace.json"), wsJSON(auditFileURI(sibling)))
		auditWrite(t, filepath.Join(storage, "basename", "workspace.json"), wsJSON("file:///elsewhere/wrongtrace"))
	}

	// Cline: JSON-escaped paths (backslashes on Windows) and bare base names.
	tasks := filepath.Join(appData, "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks")
	mention := func(p string) []byte {
		b, _ := json.Marshal(map[string]string{"text": "Current Working Directory (" + p + ")"})
		return b
	}
	auditWrite(t, filepath.Join(tasks, "own", "api_conversation_history.json"), mention(filepath.Join(root, "main.go")))
	auditWrite(t, filepath.Join(tasks, "sibling", "api_conversation_history.json"), mention(sibling))
	auditWrite(t, filepath.Join(tasks, "basename", "ui_messages.json"), []byte(`[{"text":"working on WrongTrace today"}]`))

	// Antigravity transcripts.
	brain := filepath.Join(home, ".gemini", "antigravity-cli", "brain")
	logRel := filepath.Join(".system_generated", "logs", "transcript.jsonl")
	auditWrite(t, filepath.Join(brain, "own", logRel), mention(root))
	auditWrite(t, filepath.Join(brain, "sibling", logRel), mention(filepath.Join(sibling, "README.md")))

	counts := ScanAgentSessions(root)
	want := map[string]int{"cursor": 2, "windsurf": 2, "trae": 2, "cline": 1, "antigravity": 1}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s = %d, want %d (sibling %q and base-name-only mentions must not count); all=%v", k, counts[k], v, sibling, counts)
		}
	}
}

func TestContentMentionsRoot_Boundaries(t *testing.T) {
	root := normalizedSessionRoot(`D:\Codebox\PROJECTS\WrongTrace`)
	yes := []string{
		`{"folder":"file:///d%3A/Codebox/PROJECTS/WrongTrace"}`,
		`cwd: "D:\\Codebox\\PROJECTS\\WrongTrace\\internal\\core"`,
		`d:/codebox/projects/wrongtrace`,
		`(D:\Codebox\PROJECTS\WrongTrace)`,
	}
	no := []string{
		`D:\Codebox\PROJECTS\WrongTrace-docs\x`,
		`D:\Codebox\PROJECTS\WrongTrace.old`,
		`working on wrongtrace`,
		`XD:\Codebox\PROJECTS\WrongTrace`,
		`100%zz broken escape`,
	}
	for _, s := range yes {
		if !contentMentionsRoot(s, root) {
			t.Errorf("contentMentionsRoot(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if contentMentionsRoot(s, root) {
			t.Errorf("contentMentionsRoot(%q) = true, want false", s)
		}
	}
	if m, parsed := workspaceJSONMatchesRoot([]byte(`{"folder":"vscode-remote://ssh-remote+h/d%3A/Codebox/PROJECTS/WrongTrace"}`), root); !parsed || m {
		t.Errorf("remote workspace URI matched a local root (matched=%v parsed=%v)", m, parsed)
	}
}

// ---------------------------------------------------------------------------
// 8. SetWatcher sampler lifecycle
// ---------------------------------------------------------------------------

type countingWatcher struct{ n atomic.Int64 }

func (w *countingWatcher) AddWatchDir(string) error    { return nil }
func (w *countingWatcher) RemoveWatchDir(string) error { return nil }
func (w *countingWatcher) Handler() http.Handler       { return http.NotFoundHandler() }
func (w *countingWatcher) UpdateSemOccupied(int)       { w.n.Add(1) }

func waitCount(t *testing.T, w *countingWatcher, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for w.n.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%s: sampler never ticked", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertStopped(t *testing.T, w *countingWatcher, what string) {
	t.Helper()
	before := w.n.Load()
	time.Sleep(700 * time.Millisecond) // > 3 ticks
	if after := w.n.Load(); after > before+1 {
		t.Fatalf("%s: sampler still running (%d -> %d calls)", what, before, after)
	}
}

func TestSetWatcher_SamplerStopsOnReplaceAndClose(t *testing.T) {
	isolateAuditEnv(t)
	e := NewEngine(Config{})
	w1, w2, w3 := &countingWatcher{}, &countingWatcher{}, &countingWatcher{}

	e.SetWatcher(w1)
	waitCount(t, w1, "first watcher")
	e.SetWatcher(w2)
	waitCount(t, w2, "second watcher")
	assertStopped(t, w1, "replaced watcher")

	e.Close()
	e.Close() // idempotent
	assertStopped(t, w2, "after Close")

	e.SetWatcher(w3)
	time.Sleep(500 * time.Millisecond)
	if n := w3.n.Load(); n != 0 {
		t.Fatalf("SetWatcher after Close started a sampler (%d calls)", n)
	}
	if e.Watcher() != w3 {
		t.Fatal("SetWatcher after Close must still record the watcher")
	}
}

// ---------------------------------------------------------------------------
// 9. Hub broadcast: marshal outside the lock, race-free sends
// ---------------------------------------------------------------------------

type blockingPayload struct {
	entered chan struct{}
	release chan struct{}
}

func (p blockingPayload) MarshalJSON() ([]byte, error) {
	close(p.entered)
	<-p.release
	return []byte(`"ok"`), nil
}

func TestHub_BroadcastMarshalsOutsideLock(t *testing.T) {
	h := NewHub()
	ch := h.Subscribe(new(websocket.Conn))
	p := blockingPayload{entered: make(chan struct{}), release: make(chan struct{})}
	go h.Broadcast(WSEvent{Type: "code_event", Payload: p})
	<-p.entered

	subscribed := make(chan struct{})
	go func() {
		h.Subscribe(new(websocket.Conn))
		close(subscribed)
	}()
	select {
	case <-subscribed:
	case <-time.After(2 * time.Second):
		close(p.release)
		t.Fatal("Subscribe blocked while Broadcast was marshaling: encode runs under the hub lock")
	}
	close(p.release)
	ev := recvOne(t, ch)
	if string(ev.Wire) == "" || !json.Valid(ev.Wire) {
		t.Fatalf("broadcast did not carry a valid pre-encoded frame: %q", ev.Wire)
	}
}

func TestHub_ConcurrentBroadcastAndUnsubscribe(t *testing.T) {
	h := NewHub()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.Broadcast(WSEvent{Type: "code_event", Payload: "p"})
				}
			}
		}()
	}
	for i := 0; i < 300; i++ {
		c := new(websocket.Conn)
		ch := h.Subscribe(c)
		done := make(chan struct{})
		go func() {
			for range ch {
			}
			close(done)
		}()
		h.Unsubscribe(c)
		<-done
	}
	close(stop)
	wg.Wait()
	if n := h.ClientCount(); n != 0 {
		t.Fatalf("ClientCount = %d, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// 10. projects.json written outside the registry lock, latest wins
// ---------------------------------------------------------------------------

func TestProjectsIndexSave_DoesNotHoldRegistryLock(t *testing.T) {
	isolateAuditEnv(t)
	e := NewEngine(Config{})
	p, err := e.AddProject("alpha", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	e.projSaveMu.Lock() // simulate a slow disk write in progress
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.UpdateProject(ProjectProfile{ID: p.ID, Description: "updated"})
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := e.GetProject(p.ID) // needs lockMu.RLock
		if err == nil && got.Description == "updated" {
			break
		}
		if time.Now().After(deadline) {
			e.projSaveMu.Unlock()
			t.Fatal("registry readers blocked (or update never applied) while projects.json write was pending")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.projSaveMu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("UpdateProject never finished")
	}
	if got := LoadProjectsIndex()[p.ID].Description; got != "updated" {
		t.Fatalf("persisted description = %q, want updated", got)
	}
}

func TestProjectsIndexSave_StaleSnapshotNeverOverwritesNewer(t *testing.T) {
	isolateAuditEnv(t)
	e := NewEngine(Config{})
	e.lockMu.Lock()
	e.projects["proj-1"] = ProjectProfile{ID: "proj-1", Name: "old"}
	older := e.snapshotProjectsLocked()
	e.projects["proj-1"] = ProjectProfile{ID: "proj-1", Name: "new"}
	newer := e.snapshotProjectsLocked()
	e.lockMu.Unlock()

	e.persistProjects(newer)
	e.persistProjects(older) // arrives late: must be dropped
	if got := LoadProjectsIndex()["proj-1"].Name; got != "new" {
		t.Fatalf("persisted name = %q, want the newest snapshot's %q", got, "new")
	}
}
