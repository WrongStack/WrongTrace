package ingest

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionWatcher continuously scans or tails session logs in well-known agent directories.
type SessionWatcher struct {
	mu           sync.Mutex
	watchPaths   []string
	seenFiles    map[string]int64
	onToolCall   func(ToolCallEvent)
}

// NewSessionWatcher creates a watcher for agent log files.
func NewSessionWatcher(onToolCall func(ToolCallEvent)) *SessionWatcher {
	return &SessionWatcher{
		seenFiles:  make(map[string]int64),
		onToolCall: onToolCall,
	}
}

// AddWatchDir registers a directory path to monitor for session logs.
func (sw *SessionWatcher) AddWatchDir(dir string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.watchPaths = append(sw.watchPaths, dir)
}

// DiscoverAgentDirs automatically detects standard agent directories in the workspace.
func (sw *SessionWatcher) DiscoverAgentDirs(workspaceDir string) {
	if workspaceDir == "" {
		return
	}
	// Current workspace agent folders
	for _, sub := range []string{
		".claude", ".cursor", ".gemini", ".wrongtrace", ".wrongstack",
		".continue", ".windsurf", ".aider", ".pi", ".codex", ".zcode",
		".minimax", ".kimi", ".devin", ".trae", ".copilot", ".openhands",
		".goose", ".bolt", ".lovable", ".v0", ".plandex", ".sweep", ".tabnine",
	} {
		if p := filepath.Join(workspaceDir, sub); dirExists(p) {
			sw.AddWatchDir(p)
		}
	}
	if p := filepath.Join(workspaceDir, ".aider.chat.history.md"); fileExists(p) {
		sw.AddWatchDir(workspaceDir)
	}
}

// DiscoverGlobalAgentDirs scans user home directory and OS application storage for global coding agent session logs.
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

	candidateDirs := []string{
		// 1. WrongStack (Native Flagship)
		filepath.Join(home, ".wrongstack"),
		filepath.Join(home, ".wrongstack", "projects"),

		// 2. Google Antigravity & Gemini CLI
		filepath.Join(home, ".gemini", "antigravity-cli", "brain"),
		filepath.Join(home, ".gemini", "antigravity-cli"),
		filepath.Join(home, ".gemini"),

		// 3. Claude Code (Anthropic)
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".claude", "logs"),
		filepath.Join(home, ".claude"),

		// 4. Cursor (Anysphere)
		filepath.Join(appData, "Cursor", "User", "workspaceStorage"),
		filepath.Join(appData, "Cursor", "User", "globalStorage"),
		filepath.Join(appData, "Cursor"),
		filepath.Join(home, ".cursor"),

		// 5. Windsurf (Codeium)
		filepath.Join(appData, "Windsurf", "User", "workspaceStorage"),
		filepath.Join(appData, "Windsurf", "User", "globalStorage"),
		filepath.Join(appData, "Windsurf"),
		filepath.Join(home, ".codeium", "windsurf"),
		filepath.Join(home, ".windsurf"),

		// 6. ByteDance Trae IDE
		filepath.Join(appData, "Trae", "User", "workspaceStorage"),
		filepath.Join(appData, "Trae", "User", "globalStorage"),
		filepath.Join(appData, "Trae"),
		filepath.Join(home, ".trae"),

		// 7. GitHub Copilot
		filepath.Join(appData, "Code", "User", "globalStorage", "github.copilot-chat"),
		filepath.Join(appData, "Code", "User", "globalStorage", "github.copilot"),
		filepath.Join(localAppData, "github-copilot"),
		filepath.Join(home, ".copilot", "logs"),
		filepath.Join(home, ".copilot"),

		// 8. Cline / Roo Code / Roo-Cline
		filepath.Join(appData, "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
		filepath.Join(appData, "Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "tasks"),
		filepath.Join(appData, "Cursor", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
		filepath.Join(appData, "Cursor", "User", "globalStorage", "rooveterinaryinc.roo-cline", "tasks"),
		filepath.Join(appData, "Windsurf", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
		filepath.Join(home, ".config", "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
		filepath.Join(home, ".config", "Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "tasks"),

		// 9. MiniMax Code & Kimi Code (Moonshot) & ZCode
		filepath.Join(home, ".minimax", "sessions"),
		filepath.Join(home, ".minimax"),
		filepath.Join(home, ".kimi", "sessions"),
		filepath.Join(home, ".kimi"),
		filepath.Join(home, ".moonshot"),
		filepath.Join(home, ".zcode", "tasks"),
		filepath.Join(home, ".zcode"),

		// 10. Continue.dev & Aider
		filepath.Join(home, ".continue", "sessions"),
		filepath.Join(home, ".continue"),

		// 11. Replit Agent & Zed AI
		filepath.Join(home, ".replit", "agent"),
		filepath.Join(home, ".replit"),
		filepath.Join(appData, "Zed", "conversations"),
		filepath.Join(home, ".config", "zed", "conversations"),

		// 12. Devin & Goose & OpenHands
		filepath.Join(home, ".devin", "sessions"),
		filepath.Join(home, ".devin"),
		filepath.Join(home, ".goose", "sessions"),
		filepath.Join(home, ".goose"),
		filepath.Join(localAppData, "goose"),
		filepath.Join(home, ".local", "share", "goose", "sessions"),
		filepath.Join(home, ".openhands", "conversations"),
		filepath.Join(home, ".openhands"),
	}

	for _, cd := range candidateDirs {
		if dirExists(cd) {
			sw.AddWatchDir(cd)
		}
	}
}

// PollOnce inspects watched directories and processes new or modified transcript files.
func (sw *SessionWatcher) PollOnce() {
	sw.mu.Lock()
	paths := make([]string, len(sw.watchPaths))
	copy(paths, sw.watchPaths)
	sw.mu.Unlock()

	for _, dir := range paths {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}

			ext := filepath.Ext(path)
			base := filepath.Base(path)

			if ext != ".jsonl" && ext != ".json" && base != ".aider.chat.history.md" {
				return nil
			}

			sw.mu.Lock()
			lastSize, seen := sw.seenFiles[path]
			currentSize := info.Size()
			if seen && lastSize == currentSize {
				sw.mu.Unlock()
				return nil
			}
			sw.seenFiles[path] = currentSize
			sw.mu.Unlock()

			// Parse file
			var events []ToolCallEvent
			var parseErr error

			if ext == ".jsonl" {
				events, parseErr = ParseJSONLTranscript(path)
			} else if ext == ".json" && (filepath.Base(filepath.Dir(path)) == "tasks" || filepath.Base(filepath.Dir(path)) == "cline") {
				events, parseErr = ParseClineTask(path)
			} else if base == ".aider.chat.history.md" {
				events, parseErr = ParseAiderHistory(path)
			}

			if parseErr != nil {
				log.Printf("ingest: parse %s: %v", path, parseErr)
				return nil
			}

			for _, ev := range events {
				if sw.onToolCall != nil {
					sw.onToolCall(ev)
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
