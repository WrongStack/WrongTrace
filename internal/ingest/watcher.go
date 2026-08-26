package ingest

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type fileState struct {
	offset  int64
	modTime time.Time
}

const missingFilePruneInterval = time.Hour

const (
	initialBackfillWindow = 24 * time.Hour
	maxInitialBackfill    = int64(4 * 1024 * 1024)
)

// SessionWatcher continuously scans or tails session logs in well-known agent directories.
type SessionWatcher struct {
	mu             sync.Mutex
	watchPaths     []string
	seenFiles      map[string]fileState
	seenOffsets    map[string]int64
	cursorPath     string
	cursorDirty    bool
	cursorVersion  uint64
	lastCursorSave time.Time
	lastStatePrune time.Time
	baselineBefore time.Time
	onToolCall     func(ToolCallEvent)
	onReadEvent    func(FileReadEvent)

	// dirCache remembers what each directory looked like on the previous poll
	// so dormant ones can be skipped. It has its own mutex because directory
	// enumeration runs outside mu, where event delivery happens. See scan.go.
	dirMu    sync.Mutex
	dirCache map[string]dirState
	pollGen  uint64

	// scanDepth caps how many directory levels below a watched root the walk
	// descends. It comes from WRONGTRACE_MAX_SCAN_DEPTH (default 8) at
	// construction; tests override it per instance.
	scanDepth int
}

// NewSessionWatcher creates a watcher for agent log files.
func NewSessionWatcher(onToolCall func(ToolCallEvent)) *SessionWatcher {
	return &SessionWatcher{
		seenFiles:   make(map[string]fileState),
		seenOffsets: make(map[string]int64),
		onToolCall:  onToolCall,
		scanDepth:   maxScanDepthFromEnv(),
	}
}

// SetOnReadEvent registers a callback for file read/inspection events.
func (sw *SessionWatcher) SetOnReadEvent(cb func(FileReadEvent)) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.onReadEvent = cb
}

// AddWatchDir registers a directory path to monitor for session logs.
func (sw *SessionWatcher) AddWatchDir(dir string) {
	if dir == "" {
		return
	}
	clean := filepath.Clean(dir)
	sw.mu.Lock()
	defer sw.mu.Unlock()
	for _, p := range sw.watchPaths {
		if p == clean {
			return
		}
	}
	sw.watchPaths = append(sw.watchPaths, clean)
}

// DiscoverAgentDirs automatically detects standard agent directories in the workspace.
func (sw *SessionWatcher) DiscoverAgentDirs(workspaceDir string) {
	if workspaceDir == "" {
		return
	}
	// Current workspace agent folders (targeted session subfolders where available)
	for _, sub := range []string{
		".claude/projects", ".claude/logs", ".cursor", ".gemini/antigravity-cli/brain",
		".wrongtrace", ".wrongstack/projects", ".continue/sessions", ".windsurf",
		".minimax/sessions", ".kimi/sessions", ".devin/sessions", ".trae",
		".openhands/conversations", ".goose/sessions",
	} {
		if p := filepath.Join(workspaceDir, sub); dirExists(p) {
			sw.AddWatchDir(p)
		}
	}
	if p := filepath.Join(workspaceDir, ".aider.chat.history.md"); fileExists(p) {
		sw.AddWatchDir(workspaceDir)
	}
}

// DiscoverGlobalAgentDirs scans user home directory and OS application storage for specific coding agent session log folders.
func (sw *SessionWatcher) DiscoverGlobalAgentDirs() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = filepath.Join(home, "AppData", "Local")
	}

	// TARGETED subdirectories only — NEVER add top-level AppData or tool roots (which contain gigabytes of Electron caches)
	candidateDirs := []string{
		// 1. WrongStack
		filepath.Join(home, ".wrongstack", "projects"),

		// 2. Google Antigravity & Gemini CLI (ONLY brain/logs, NOT ~/.gemini root)
		filepath.Join(home, ".gemini", "antigravity-cli", "brain"),

		// 3. Claude Code (Anthropic)
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".claude", "logs"),

		// 4. GitHub Copilot
		filepath.Join(appData, "Code", "User", "globalStorage", "github.copilot-chat"),
		filepath.Join(localAppData, "github-copilot"),
		filepath.Join(home, ".copilot", "logs"),

		// 8. Cline / Roo Code / Roo-Cline
		filepath.Join(appData, "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
		filepath.Join(appData, "Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "tasks"),
		filepath.Join(appData, "Cursor", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
		filepath.Join(appData, "Cursor", "User", "globalStorage", "rooveterinaryinc.roo-cline", "tasks"),
		filepath.Join(appData, "Windsurf", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
		filepath.Join(home, ".config", "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
		filepath.Join(home, ".config", "Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "tasks"),

		// 9. MiniMax Code & Kimi Code & ZCode
		filepath.Join(home, ".minimax", "sessions"),
		filepath.Join(home, ".kimi", "sessions"),
		filepath.Join(home, ".zcode", "tasks"),

		// 10. Continue.dev
		filepath.Join(home, ".continue", "sessions"),

		// 11. Replit Agent & Zed AI
		filepath.Join(home, ".replit", "agent"),
		filepath.Join(appData, "Zed", "conversations"),
		filepath.Join(home, ".config", "zed", "conversations"),

		// 12. Devin & Goose & OpenHands
		filepath.Join(home, ".devin", "sessions"),
		filepath.Join(home, ".goose", "sessions"),
		filepath.Join(home, ".local", "share", "goose", "sessions"),
		filepath.Join(home, ".openhands", "conversations"),
	}

	for _, cd := range candidateDirs {
		if dirExists(cd) {
			sw.AddWatchDir(cd)
		}
	}
}

var ignoredLogDirMap = map[string]struct{}{
	".git": {}, "node_modules": {}, "cache": {}, "gpucache": {}, "code cache": {}, "cachestorage": {},
	"temp": {}, "tmp": {}, "extensions": {}, "dist": {}, "build": {}, "bin": {}, "obj": {}, "venv": {}, ".venv": {},
	"__pycache__": {}, "crashpad": {}, "dawngraphitecache": {}, "grshadercache": {}, "shadercache": {},
	"indexeddb": {}, "local storage": {}, "session storage": {}, "blob_storage": {}, "service worker": {},
	"dictionaries": {}, "webrtc": {}, "packaged-extensions": {}, "state.vscdb.backup": {}, "backup": {},
	"assets": {}, "images": {}, "videos": {}, "media": {}, "thumbnails": {}, "coverage": {}, ".turbo": {},
	".next": {}, ".svelte-kit": {}, ".nuxt": {}, "out": {}, ".idea": {}, ".npm": {}, ".yarn": {}, ".cargo": {},
	"scratch": {}, "plans": {}, "artifacts": {}, "snapshots": {}, "diffs": {}, "checkpoints": {}, "storage": {},
}

// isIgnoredLogDir reports whether a directory segment should be skipped during walk.
func isIgnoredLogDir(name string) bool {
	lower := strings.ToLower(name)
	_, ok := ignoredLogDirMap[lower]
	return ok
}

// PollOnce inspects watched directories and incrementally processes new
// transcript lines. See scan.go for how the walk itself is kept cheap.
func (sw *SessionWatcher) PollOnce() {
	sw.mu.Lock()
	paths := make([]string, len(sw.watchPaths))
	copy(paths, sw.watchPaths)
	sw.pollGen++
	gen := sw.pollGen
	sw.mu.Unlock()

	now := time.Now()
	for _, rootDir := range paths {
		sw.scanRoot(filepath.Clean(rootDir), gen, now)
	}

	sw.pruneDirCache(gen)
	sw.pruneMissingFiles(now)
	if err := sw.saveOffsets(false); err != nil {
		log.Printf("ingest: save offset checkpoint: %v", err)
	}
}

// processFile advances one transcript's cursor and emits whatever the new
// bytes contained. It is a no-op when the file has not grown since the offset
// recorded for it.
func (sw *SessionWatcher) processFile(path string, kind fileKind, currentSize int64, modTime time.Time) {
	if kind == kindNone {
		return
	}
	sw.mu.Lock()
	st, seen := sw.seenFiles[path]
	if !seen {
		if off, ok := sw.seenOffsets[path]; ok {
			st = fileState{offset: off, modTime: modTime}
			seen = true
		} else if off, baseline := initialTranscriptOffset(currentSize, modTime, sw.baselineBefore, kind == kindJSONL); baseline {
			st = fileState{offset: off, modTime: modTime}
			sw.seenOffsets[path] = off
			sw.seenFiles[path] = st
			sw.cursorDirty = true
			sw.cursorVersion++
			seen = true
		}
	}

	if seen && currentSize <= st.offset && !modTime.After(st.modTime) {
		sw.mu.Unlock()
		return
	}

	lastOffset := st.offset
	if currentSize < lastOffset {
		lastOffset = 0 // File truncated or rewritten
	}
	sw.mu.Unlock()

	var events []ToolCallEvent
	var readEvents []FileReadEvent
	var newOffset int64
	var parseErr error

	switch kind {
	case kindJSONL:
		events, readEvents, newOffset, parseErr = ParseJSONLTranscriptFromOffset(path, lastOffset)
	case kindJSON:
		events, parseErr = ParseClineTask(path)
		newOffset = currentSize
	case kindAider:
		events, parseErr = ParseAiderHistory(path)
		newOffset = currentSize
	}

	if parseErr != nil {
		log.Printf("ingest: parse %s: %v", path, parseErr)
		return
	}

	sw.mu.Lock()
	sw.seenOffsets[path] = newOffset
	sw.seenFiles[path] = fileState{offset: newOffset, modTime: modTime}
	sw.cursorDirty = true
	sw.cursorVersion++
	onToolCall := sw.onToolCall
	onReadEvent := sw.onReadEvent
	sw.mu.Unlock()

	for _, ev := range events {
		if onToolCall != nil {
			onToolCall(ev)
		}
	}
	for _, rev := range readEvents {
		if onReadEvent != nil {
			onReadEvent(rev)
		}
	}
}

// initialTranscriptOffset bounds the first scan after a fresh install. Old
// history is baselined at EOF; recent JSONL keeps a small tail so the current
// session remains visible. Once a persistent cursor exists this path is never
// used, and files created after startup still ingest from byte zero.
func initialTranscriptOffset(size int64, modTime, baselineBefore time.Time, isJSONL bool) (int64, bool) {
	if baselineBefore.IsZero() || modTime.After(baselineBefore) {
		return 0, false
	}
	if !isJSONL || modTime.Before(baselineBefore.Add(-initialBackfillWindow)) {
		return size, true
	}
	return max(0, size-maxInitialBackfill), true
}

// pruneMissingFiles prevents the persisted cursor and its in-memory maps from
// growing forever as agents rotate transcript files. The filesystem walk
// already dominates polling, so the extra existence pass is deliberately
// infrequent and runs without holding the watcher mutex.
func (sw *SessionWatcher) pruneMissingFiles(now time.Time) {
	sw.mu.Lock()
	if !sw.lastStatePrune.IsZero() && now.Sub(sw.lastStatePrune) < missingFilePruneInterval {
		sw.mu.Unlock()
		return
	}
	sw.lastStatePrune = now
	paths := make([]string, 0, len(sw.seenOffsets))
	for path := range sw.seenOffsets {
		paths = append(paths, path)
	}
	sw.mu.Unlock()

	missing := make([]string, 0)
	for _, path := range paths {
		if !fileExists(path) {
			missing = append(missing, path)
		}
	}
	if len(missing) == 0 {
		return
	}

	sw.mu.Lock()
	for _, path := range missing {
		// A file may have been recreated while the stat pass was running.
		if !fileExists(path) {
			delete(sw.seenFiles, path)
			delete(sw.seenOffsets, path)
			sw.cursorDirty = true
			sw.cursorVersion++
		}
	}
	sw.mu.Unlock()
}

// StartPolling runs a background loop polling session logs every interval.
func (sw *SessionWatcher) StartPolling(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		defer func() {
			if err := sw.saveOffsets(true); err != nil {
				log.Printf("ingest: save final offset checkpoint: %v", err)
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sw.PollOnce()
			}
		}
	}()
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
