package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wrongstack/wrongtrace/internal/webhook"
)

// GuardrailResult indicates whether an agent should proceed editing a file.
type GuardrailResult struct {
	Allowed              bool      `json:"allowed"`
	HealthScore          int       `json:"health_score"`
	RecentThrashingCount int       `json:"recent_thrashing_count"`
	IsFragile            bool      `json:"is_fragile"`
	IsLocked             bool      `json:"is_locked"`
	LockReason           string    `json:"lock_reason,omitempty"`
	Recommendation       string    `json:"recommendation"`
	CheckedAt            time.Time `json:"checked_at"`
}

// LockFile locks a file from agent modifications.
func (e *Engine) LockFile(path, reason string) {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	if e.lockedFiles == nil {
		e.lockedFiles = make(map[string]string)
	}
	norm := strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(path))))
	if reason == "" {
		reason = "file is explicitly locked by administrator guardrail"
	}
	e.lockedFiles[norm] = reason
}

// UnlockFile removes a lock on a file.
func (e *Engine) UnlockFile(path string) {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	if e.lockedFiles == nil {
		return
	}
	norm := strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(path))))
	delete(e.lockedFiles, norm)
	for k := range e.lockedFiles {
		if k == norm || strings.HasSuffix(norm, "/"+k) || strings.HasSuffix(k, "/"+norm) {
			delete(e.lockedFiles, k)
		}
	}
}

// IsFileLocked checks if a file is currently locked.
func (e *Engine) IsFileLocked(path string) (bool, string) {
	e.lockMu.RLock()
	defer e.lockMu.RUnlock()
	if len(e.lockedFiles) == 0 {
		return false, ""
	}
	norm := strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(path))))
	if r, ok := e.lockedFiles[norm]; ok {
		return true, r
	}
	for k, r := range e.lockedFiles {
		if k == norm || strings.HasSuffix(norm, "/"+k) || strings.HasSuffix(k, "/"+norm) {
			return true, r
		}
	}
	return false, ""
}

// CheckGuardrail assesses file safety before an AI agent attempts to edit it.
func (e *Engine) CheckGuardrail(path string) (GuardrailResult, error) {
	locked, reason := e.IsFileLocked(path)
	if locked {
		if e.webhooks != nil {
			e.webhooks.Dispatch(webhook.Payload{
				EventType: webhook.EventGuardrailBlock,
				Severity:  "critical",
				Message:   fmt.Sprintf("Guardrail blocked modification on locked file: %s (%s)", path, reason),
				Details:   map[string]interface{}{"file_path": path, "reason": reason},
			})
		}
		return GuardrailResult{
			Allowed:        false,
			IsLocked:       true,
			LockReason:     reason,
			Recommendation: "BLOCKED: File is locked against automated agent changes.",
			CheckedAt:      time.Now().UTC(),
		}, nil
	}

	h, err := e.FileHealth(path)
	if err != nil {
		return GuardrailResult{
			Allowed:        true,
			HealthScore:    100,
			Recommendation: "Allowed: No previous churn history.",
			CheckedAt:      time.Now().UTC(),
		}, nil
	}

	allowed := true
	rec := "Safe to modify."

	if h.IsFragile || h.HealthScore < 40 {
		allowed = false
		rec = fmt.Sprintf("GUARDRAIL WARNING: File %s has high churn (Health Score: %d/100, %d thrash events). Consider human review.", path, h.HealthScore, h.RecentThrashingCount)
		if e.webhooks != nil {
			e.webhooks.Dispatch(webhook.Payload{
				EventType: webhook.EventThrashingAlert,
				Severity:  "warning",
				Message:   rec,
				Details:   map[string]interface{}{"file_path": path, "health_score": h.HealthScore, "thrash_count": h.RecentThrashingCount},
			})
		}
	} else if h.HealthScore < 70 {
		rec = fmt.Sprintf("Caution: File health score is %d/100. Apply minimal localized diffs.", h.HealthScore)
	}

	return GuardrailResult{
		Allowed:              allowed,
		HealthScore:          h.HealthScore,
		RecentThrashingCount: h.RecentThrashingCount,
		IsFragile:            h.IsFragile,
		IsLocked:             false,
		Recommendation:       rec,
		CheckedAt:            time.Now().UTC(),
	}, nil
}
