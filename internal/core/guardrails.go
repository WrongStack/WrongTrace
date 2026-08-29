package core

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ipc"
	"github.com/wrongstack/wrongtrace/internal/webhook"
)

// LockInfo records guardrail lock metadata including ownership and expiry TTL.
type LockInfo = ipc.LockInfo

// GuardrailResult indicates whether an agent should proceed editing a file.
type GuardrailResult = ipc.GuardrailResult

// normalizeLockPath canonicalizes a path for lock bookkeeping. Both separator
// styles are folded to "/" explicitly rather than through filepath.ToSlash,
// which is a no-op on Linux and left a Windows-style path as one opaque
// segment there -- so a lock taken as "internal\core\engine.go" matched
// nothing, not even itself under a different spelling.
func normalizeLockPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	return strings.ToLower(path.Clean(p))
}

// LockFile locks a file from agent modifications with default 15-minute TTL.
func (e *Engine) LockFile(path, reason string) LockInfo {
	return e.LockFileWithOptions(path, reason, "", "", 15*time.Minute)
}

// LockFileWithOptions locks a file with explicit owner, run ID, and duration.
func (e *Engine) LockFileWithOptions(path, reason, owner, ownerRunID string, ttl time.Duration) LockInfo {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	if e.lockedFiles == nil {
		e.lockedFiles = make(map[string]LockInfo)
	}
	norm := normalizeLockPath(path)
	if reason == "" {
		reason = "file is explicitly locked by administrator guardrail"
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	now := time.Now().UTC()
	info := LockInfo{
		Path:       norm,
		Reason:     reason,
		Owner:      owner,
		OwnerRunID: ownerRunID,
		LockedAt:   now,
		ExpiresAt:  now.Add(ttl),
	}
	e.lockedFiles[norm] = info
	return info
}

// UnlockFile removes a lock on a file.
func (e *Engine) UnlockFile(path string) {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	if e.lockedFiles == nil {
		return
	}
	norm := normalizeLockPath(path)
	delete(e.lockedFiles, norm)
	for k := range e.lockedFiles {
		if lockPathMatch(k, norm) {
			delete(e.lockedFiles, k)
		}
	}
}

// lockPathMatch reports whether two normalized lock paths refer to the same
// file or a directory/file nesting of each other. It is the allocation-free
// equivalent of the earlier "a == b || suffix(a, "/"+b) || suffix(b, "/"+a)"
// form, which built two concatenated strings per entry on the per-edit
// guardrail path.
func lockPathMatch(a, b string) bool {
	if a == b {
		return true
	}
	la, lb := len(a), len(b)
	if la > lb && a[la-lb-1] == '/' && a[la-lb:] == b {
		return true
	}
	if lb > la && b[lb-la-1] == '/' && b[lb-la:] == a {
		return true
	}
	return false
}

// IsFileLocked checks if a file is currently locked and unexpired. It runs
// under a read lock: expired entries are simply ignored here and left for
// sweepExpiredLocks, so concurrent guardrail checks never serialize behind a
// writer or mutate the map on the hot path.
func (e *Engine) IsFileLocked(path string) (bool, LockInfo) {
	e.lockMu.RLock()
	defer e.lockMu.RUnlock()
	if len(e.lockedFiles) == 0 {
		return false, LockInfo{}
	}
	now := time.Now().UTC()
	norm := normalizeLockPath(path)
	if info, ok := e.lockedFiles[norm]; ok {
		if !now.After(info.ExpiresAt) {
			return true, info
		}
	}
	for k, info := range e.lockedFiles {
		if now.After(info.ExpiresAt) {
			continue
		}
		if lockPathMatch(k, norm) {
			return true, info
		}
	}
	return false, LockInfo{}
}

// sweepExpiredLocks drops expired entries so their memory cannot accumulate
// between checks. Called from the engine's maintenance ticker.
func (e *Engine) sweepExpiredLocks() {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	now := time.Now().UTC()
	for k, info := range e.lockedFiles {
		if now.After(info.ExpiresAt) {
			delete(e.lockedFiles, k)
		}
	}
}

// ListLocks returns all active, non-expired file locks.
func (e *Engine) ListLocks() []LockInfo {
	e.lockMu.RLock()
	defer e.lockMu.RUnlock()
	if len(e.lockedFiles) == 0 {
		return []LockInfo{}
	}
	now := time.Now().UTC()
	out := make([]LockInfo, 0, len(e.lockedFiles))
	for _, info := range e.lockedFiles {
		if now.After(info.ExpiresAt) {
			continue
		}
		out = append(out, info)
	}
	return out
}

// CheckGuardrail assesses file safety before an AI agent attempts to edit it.
func (e *Engine) CheckGuardrail(path string) (GuardrailResult, error) {
	locked, lockInfo := e.IsFileLocked(path)
	if locked {
		if e.webhooks != nil {
			e.webhooks.Dispatch(webhook.Payload{
				EventType: webhook.EventGuardrailBlock,
				Severity:  "critical",
				Message:   fmt.Sprintf("Guardrail blocked modification on locked file: %s (%s)", path, lockInfo.Reason),
				Details:   map[string]interface{}{"file_path": path, "reason": lockInfo.Reason, "owner": lockInfo.Owner},
			})
		}
		var expPtr *time.Time
		if !lockInfo.ExpiresAt.IsZero() {
			exp := lockInfo.ExpiresAt
			expPtr = &exp
		}
		return GuardrailResult{
			Allowed:        false,
			IsLocked:       true,
			LockReason:     lockInfo.Reason,
			LockOwner:      lockInfo.Owner,
			LockOwnerRunID: lockInfo.OwnerRunID,
			LockExpiresAt:  expPtr,
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
