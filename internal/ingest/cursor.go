package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	offsetCheckpointVersion  = 1
	offsetSaveInterval       = 5 * time.Minute
	maxOffsetCheckpointBytes = 16 * 1024 * 1024
)

type offsetCheckpoint struct {
	Version int              `json:"version"`
	Offsets map[string]int64 `json:"offsets"`
}

// EnablePersistentOffsets restores transcript cursors from path. Missing
// checkpoints are normal on first run. The first completed poll is persisted
// immediately; later active-tail updates are coalesced to limit disk churn.
func (sw *SessionWatcher) EnablePersistentOffsets(path string) error {
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	sw.mu.Lock()
	sw.cursorPath = clean
	sw.mu.Unlock()
	data, err := os.ReadFile(clean)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", clean, err)
	}
	if len(data) > maxOffsetCheckpointBytes {
		return fmt.Errorf("checkpoint %s exceeds %d bytes", clean, maxOffsetCheckpointBytes)
	}

	loaded := offsetCheckpoint{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &loaded); err != nil {
			return fmt.Errorf("decode %s: %w", clean, err)
		}
		if loaded.Version != offsetCheckpointVersion {
			return fmt.Errorf("unsupported checkpoint version %d", loaded.Version)
		}
	}

	sw.mu.Lock()
	// A continuous observer should not replay an unbounded lifetime of agent
	// logs for paths absent from its checkpoint. PollOnce will baseline old
	// files and retain a bounded tail of recently active JSONL transcripts.
	sw.baselineBefore = time.Now()
	for file, offset := range loaded.Offsets {
		if offset >= 0 {
			sw.seenOffsets[file] = offset
		}
	}
	sw.mu.Unlock()
	return nil
}

func (sw *SessionWatcher) saveOffsets(force bool) error {
	sw.mu.Lock()
	if sw.cursorPath == "" || !sw.cursorDirty || (!force && !sw.lastCursorSave.IsZero() && time.Since(sw.lastCursorSave) < offsetSaveInterval) {
		sw.mu.Unlock()
		return nil
	}
	path := sw.cursorPath
	version := sw.cursorVersion
	offsets := make(map[string]int64, len(sw.seenOffsets))
	for file, offset := range sw.seenOffsets {
		offsets[file] = offset
	}
	sw.mu.Unlock()

	data, err := json.Marshal(offsetCheckpoint{Version: offsetCheckpointVersion, Offsets: offsets})
	if err != nil {
		return err
	}
	if len(data) > maxOffsetCheckpointBytes {
		return fmt.Errorf("checkpoint grew to %d bytes", len(data))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Cursor state is a performance hint, not authoritative telemetry. A direct
	// bounded rewrite avoids Windows rename-over-existing failures; corruption
	// after a crash merely causes one conservative historical rescan.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}

	sw.mu.Lock()
	if sw.cursorVersion == version {
		sw.cursorDirty = false
	}
	sw.lastCursorSave = time.Now()
	sw.mu.Unlock()
	return nil
}
