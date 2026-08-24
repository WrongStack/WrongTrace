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

// SessionWatcher continuously scans or tails session logs in well-known agent directories.
type SessionWatcher struct {
	mu          sync.Mutex
	watchPaths  []string
	seenOffsets map[string]int64
	dirModTimes map[string]time.Time
	onToolCall  func(ToolCallEvent)
	onReadEvent func(FileReadEvent)
}

// NewSessionWatcher creates a watcher for agent log files.
func NewSessionWatcher(onToolCall func(ToolCallEvent)) *SessionWatcher {
	return &SessionWatcher{
		seenOffsets: make(map[string]int64),
		dirModTimes: make(map[string]time.Time),
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
	sw.mu.Lock()
	defer sw.mu.Unlock()
	for _, p := range sw.watchPaths {
		if p == dir {
			return
		}
	}
	sw.watchPaths = append(sw.watchPaths, dir)
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
		filepath.Join(home, ".wrongstack"),

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

// isIgnoredLogDir reports whether a directory segment should be skipped during walk.
func isIgnoredLogDir(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case ".git", "node_modules", "cache", "gpucache", "code cache", "cachestorage",
		"temp", "tmp", "extensions", "dist", "build", "bin", "obj", "venv", ".venv",
		"__pycache__", "crashpad", "dawngraphitecache", "grshadercache", "shadercache",
		"indexeddb", "local storage", "session storage", "blob_storage", "service worker",
		"dictionaries", "webrtc", "packaged-extensions", "state.vscdb.backup", "backup":
		return true
	}
	return false
}

// PollOnce inspects watched directories and incrementally processes new transcript lines.
func (sw *SessionWatcher) PollOnce() {
	sw.mu.Lock()
	paths := make([]string, len(sw.watchPaths))
	copy(paths, sw.watchPaths)
	if sw.dirModTimes == nil {
		sw.dirModTimes = make(map[string]time.Time)
	}
	sw.mu.Unlock()

	for _, rootDir := range paths {
		_ = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}

			if info.IsDir() {
				if path != rootDir {
					name := info.Name()
					if isIgnoredLogDir(name) {
						return filepath.SkipDir
					}
					// Depth limit: transcript files are max 4-5 levels deep from root
					rel, relErr := filepath.Rel(rootDir, path)
					if relErr == nil {
						segs := strings.Split(filepath.ToSlash(rel), "/")
						if len(segs) > 5 {
							return filepath.SkipDir
						}
					}
					// Prune unchanged subtrees using directory modification time
					modTime := info.ModTime()
					sw.mu.Lock()
					lastMod, seenDir := sw.dirModTimes[path]
					sw.mu.Unlock()
					if seenDir && !modTime.After(lastMod) {
						// Directory modification time has not changed, skip sub-tree walk
						return filepath.SkipDir
					}
					sw.mu.Lock()
					sw.dirModTimes[path] = modTime
					sw.mu.Unlock()
				}
				return nil
			}

			ext := filepath.Ext(path)
			base := filepath.Base(path)

			// Skip transcript_full.jsonl when transcript.jsonl exists (compact version is sufficient)
			if base == "transcript_full.jsonl" {
				return nil
			}

			if ext != ".jsonl" && ext != ".json" && base != ".aider.chat.history.md" {
				return nil
			}

			// For .json files, only process known task files (Cline/Roo tasks)
			if ext == ".json" {
				parent := filepath.Base(filepath.Dir(path))
				if parent != "tasks" && parent != "cline" && parent != "sessions" && parent != "conversations" {
					return nil
				}
			}

			sw.mu.Lock()
			lastOffset, seen := sw.seenOffsets[path]
			currentSize := info.Size()
			if seen && currentSize <= lastOffset {
				sw.mu.Unlock()
				return nil
			}
			if currentSize < lastOffset {
				lastOffset = 0 // File truncated or rewritten
			}
			sw.mu.Unlock()

			// Incrementally parse file from lastOffset
			var events []ToolCallEvent
			var readEvents []FileReadEvent
			var newOffset int64
			var parseErr error

			if ext == ".jsonl" {
				events, readEvents, newOffset, parseErr = ParseJSONLTranscriptFromOffset(path, lastOffset)
			} else if ext == ".json" {
				events, parseErr = ParseClineTask(path)
				newOffset = currentSize
			} else if base == ".aider.chat.history.md" {
				events, parseErr = ParseAiderHistory(path)
				newOffset = currentSize
			}

			if parseErr != nil {
				log.Printf("ingest: parse %s: %v", path, parseErr)
				return nil
			}

			sw.mu.Lock()
			sw.seenOffsets[path] = newOffset
			// Prevent seenOffsets map unbounded growth
			if len(sw.seenOffsets) > 10000 {
				for k := range sw.seenOffsets {
					if !fileExists(k) {
						delete(sw.seenOffsets, k)
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
}

// StartPolling runs a background loop polling session logs every interval.
func (sw *SessionWatcher) StartPolling(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
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
