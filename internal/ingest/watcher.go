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
	size    int64
	modTime time.Time
}

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
	onToolCall     func(ToolCallEvent)
	onReadEvent    func(FileReadEvent)
}

// NewSessionWatcher creates a watcher for agent log files.
func NewSessionWatcher(onToolCall func(ToolCallEvent)) *SessionWatcher {
	return &SessionWatcher{
		seenFiles:   make(map[string]fileState),
		seenOffsets: make(map[string]int64),
		onToolCall:  onToolCall,
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

// PollOnce inspects watched directories and incrementally processes new transcript lines.
func (sw *SessionWatcher) PollOnce() {
	sw.mu.Lock()
	paths := make([]string, len(sw.watchPaths))
	copy(paths, sw.watchPaths)
	sw.mu.Unlock()

	for _, rootDir := range paths {
		cleanRoot := filepath.Clean(rootDir)
		_ = filepath.WalkDir(cleanRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}

			name := d.Name()

			if d.IsDir() {
				if path != cleanRoot {
					if isIgnoredLogDir(name) {
						return filepath.SkipDir
					}
					// Depth limit: count path separators without allocations
					rel := path[len(cleanRoot):]
					if len(rel) > 0 && (rel[0] == '/' || rel[0] == '\\') {
						rel = rel[1:]
					}
					depth := 0
					for i := 0; i < len(rel); i++ {
						if rel[i] == '/' || rel[i] == '\\' {
							depth++
						}
					}
					if depth >= 5 {
						return filepath.SkipDir
					}
				}
				return nil
			}

			// Skip transcript_full.jsonl when transcript.jsonl exists (compact version is sufficient)
			if name == "transcript_full.jsonl" {
				return nil
			}

			isJSONL := strings.HasSuffix(name, ".jsonl")
			isJSON := !isJSONL && strings.HasSuffix(name, ".json")
			isAider := name == ".aider.chat.history.md"

			if !isJSONL && !isJSON && !isAider {
				return nil
			}

			// For .json files, only process known task files (Cline/Roo tasks)
			if isJSON {
				// Fast extraction of parent dir name without allocating filepath.Dir
				dirOnly := path[:len(path)-len(name)]
				if len(dirOnly) > 0 && (dirOnly[len(dirOnly)-1] == '/' || dirOnly[len(dirOnly)-1] == '\\') {
					dirOnly = dirOnly[:len(dirOnly)-1]
				}
				lastSep := strings.LastIndexAny(dirOnly, "/\\")
				parent := dirOnly
				if lastSep != -1 {
					parent = dirOnly[lastSep+1:]
				}
				if parent != "tasks" && parent != "cline" && parent != "sessions" && parent != "conversations" {
					return nil
				}
			}

			// Fast file metadata lookup only for matched candidates
			info, err := d.Info()
			if err != nil || info == nil {
				return nil
			}

			currentSize := info.Size()
			modTime := info.ModTime()

			sw.mu.Lock()
			st, seen := sw.seenFiles[path]
			if !seen {
				if off, ok := sw.seenOffsets[path]; ok {
					st = fileState{offset: off, size: currentSize, modTime: modTime}
					seen = true
				}
			}

			if seen && currentSize <= st.offset && !modTime.After(st.modTime) {
				sw.mu.Unlock()
				return nil
			}

			lastOffset := st.offset
			if currentSize < lastOffset {
				lastOffset = 0 // File truncated or rewritten
			}
			sw.mu.Unlock()

			// Incrementally parse file from lastOffset
			var events []ToolCallEvent
			var readEvents []FileReadEvent
			var newOffset int64
			var parseErr error

			if isJSONL {
				events, readEvents, newOffset, parseErr = ParseJSONLTranscriptFromOffset(path, lastOffset)
			} else if isJSON {
				events, parseErr = ParseClineTask(path)
				newOffset = currentSize
			} else if isAider {
				events, parseErr = ParseAiderHistory(path)
				newOffset = currentSize
			}

			if parseErr != nil {
				log.Printf("ingest: parse %s: %v", path, parseErr)
				return nil
			}

			sw.mu.Lock()
			sw.seenOffsets[path] = newOffset
			sw.seenFiles[path] = fileState{
				offset:  newOffset,
				size:    currentSize,
				modTime: modTime,
			}
			sw.cursorDirty = true
			sw.cursorVersion++

			// Smart pruning: only purge entries when capacity is huge (> 25000), and only purge nonexistent paths first
			if len(sw.seenFiles) > 25000 {
				pruned := 0
				for k := range sw.seenFiles {
					if !fileExists(k) {
						delete(sw.seenFiles, k)
						delete(sw.seenOffsets, k)
						pruned++
						if pruned >= 5000 {
							break
						}
					}
				}
			}
			sw.mu.Unlock()

			for _, ev := range events {
				if sw.onToolCall != nil {
					sw.onToolCall(ev)
				}
			}

			for _, rev := range readEvents {
				if sw.onReadEvent != nil {
					sw.onReadEvent(rev)
				}
			}

			return nil
		})
	}
	if err := sw.saveOffsets(false); err != nil {
		log.Printf("ingest: save offset checkpoint: %v", err)
	}
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
