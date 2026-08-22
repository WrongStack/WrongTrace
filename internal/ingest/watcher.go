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

// DiscoverAgentDirs automatically detects standard directories where coding agents store session logs.
func (sw *SessionWatcher) DiscoverAgentDirs(workspaceDir string) {
	// 1. Current workspace .claude or .aider
	if p := filepath.Join(workspaceDir, ".claude"); dirExists(p) {
		sw.AddWatchDir(p)
	}
	if p := filepath.Join(workspaceDir, ".aider.chat.history.md"); fileExists(p) {
		sw.AddWatchDir(workspaceDir)
	}

	// 2. User home directories
	if home, err := os.UserHomeDir(); err == nil {
		claudeHome := filepath.Join(home, ".claude", "logs")
		if dirExists(claudeHome) {
			sw.AddWatchDir(claudeHome)
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
