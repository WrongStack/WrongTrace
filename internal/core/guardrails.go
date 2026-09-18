package core

import (
	"errors"
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

// LockConflictError is returned by TryLockFile when another owner holds the lock.
type LockConflictError = ipc.LockConflictError

// ErrLockConflict matches every *LockConflictError via errors.Is.
var ErrLockConflict = ipc.ErrLockConflict

// LockFileWithOptions locks a file with explicit owner, run ID, and duration.
// It is the unconditional (administrative/force) form: it replaces any
// existing lock. Agent-facing surfaces must use TryLockFile, which refuses to
// steal a lock held by a different owner.
func (e *Engine) LockFileWithOptions(path, reason, owner, ownerRunID string, ttl time.Duration) LockInfo {
	info, _ := e.TryLockFile(path, reason, owner, ownerRunID, ttl, true)
	return info
}

// TryLockFile acquires a lock like LockFileWithOptions, but — unless force is
// set — returns a *LockConflictError (matching ErrLockConflict) when an
// unexpired lock on the same or a nesting path is held by a different named
// owner. The check and the write happen under one critical section, so two
// agents racing for the same file cannot both succeed. Same-owner re-locks
// refresh the lock; anonymous (owner-less) and expired locks stay takeable.
func (e *Engine) TryLockFile(path, reason, owner, ownerRunID string, ttl time.Duration, force bool) (LockInfo, error) {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	if e.lockedFiles == nil {
		e.lockedFiles = make(map[string]LockInfo)
	}
	norm := normalizeLockPath(path)
	if !force {
		now := time.Now().UTC()
		for k, existing := range e.lockedFiles {
			if now.After(existing.ExpiresAt) || !lockPathMatch(k, norm) {
				continue
			}
			if existing.Owner != "" && existing.Owner != owner {
				return existing, &LockConflictError{Path: path, Existing: existing}
			}
		}
	}
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
	return info, nil
}

// ErrNotLockOwner is returned by UnlockFile when the caller does not own the lock.
var ErrNotLockOwner = errors.New("unlock denied: caller does not own the lock")

// UnlockFile removes a lock on a file. It returns ErrNotLockOwner when
// ownerRunID is non-empty and does not match the lock's stored OwnerRunID,
// preventing one agent from deleting another agent's lock. A call with an
// empty ownerRunID (legacy / force-unlock) is still allowed so that an
// agent can remove its own stale lock without needing to know its run ID.
func (e *Engine) UnlockFile(path string, ownerRunID string) error {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	if e.lockedFiles == nil {
		return nil
	}
	norm := normalizeLockPath(path)
	// Verify ownership of EVERY nesting match before deleting anything, using
	// the same lockPathMatch resolution as the delete loop below. The guard
	// used to look up only the exact key, so a caller spelling the file as a
	// nesting alias ("main.go" for a lock held as "src/main.go") missed the
	// guard yet still deleted the lock through the nesting loop. An empty
	// ownerRunID stays a legacy/force unlock, and expired entries stay
	// clearable by anyone.
	if ownerRunID != "" {
		now := time.Now().UTC()
		for k, info := range e.lockedFiles {
			if !lockPathMatch(k, norm) || now.After(info.ExpiresAt) {
				continue
			}
			if info.OwnerRunID != "" && info.OwnerRunID != ownerRunID {
				return ErrNotLockOwner
			}
		}
	}
	delete(e.lockedFiles, norm)
	for k := range e.lockedFiles {
		if lockPathMatch(k, norm) {
			delete(e.lockedFiles, k)
		}
	}
	return nil
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
			Allowed:    false,
			IsLocked:   true,
			LockReason: lockInfo.Reason,
			LockOwner:  lockInfo.Owner,
			// LockOwnerRunID is deliberately NOT carried: it is the
			// credential Engine.UnlockFile verifies, so publishing it
			// from a READ surface lets any caller that merely checks
			// safety harvest another agent's credential and present it
			// back to unlock_file. The holder's name and reason stay for
			// diagnostics; only the authorization material is withheld.
			LockExpiresAt: expPtr,
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
