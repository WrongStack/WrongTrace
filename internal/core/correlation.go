package core

import (
	"path/filepath"
	"strings"
	"time"
)

const fileOperationWindow = 2 * time.Minute

// ActiveRun is the dashboard-facing view of a currently-tracked agent run.
// The correlation window is short (10 minutes by default), so anything older
// is pruned on read.
type ActiveRun struct {
	RunID       string    `json:"run_id"`
	AgentName   string    `json:"agent_name"`
	ModelName   string    `json:"model_name"`
	TaskID      string    `json:"task_id"`
	ProjectID   string    `json:"project_id,omitempty"`
	ProjectSlug string    `json:"project_slug,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	LastSeen    time.Time `json:"last_seen"`
}

// ActiveRuns returns the active agent runs, pruning any whose last_seen is
// older than the correlation window. Used by the dashboard's agent badge.
func (e *Engine) ActiveRuns() []ActiveRun {
	activeProj := e.GetActiveProject()
	e.runMu.Lock()
	defer e.runMu.Unlock()
	cutoff := time.Now().Add(-e.correlate)
	out := make([]ActiveRun, 0, len(e.activeRuns))
	for id, m := range e.activeRuns {
		if m.LastSeen.Before(cutoff) {
			delete(e.activeRuns, id)
			continue
		}
		// If an active project is set, filter runs that explicitly belong to other projects
		if activeProj != nil {
			if m.ProjectID != "" && m.ProjectID != activeProj.ID {
				continue
			}
			if m.ProjectSlug != "" && !strings.EqualFold(m.ProjectSlug, activeProj.Name) && !strings.EqualFold(m.ProjectSlug, activeProj.ID) {
				continue
			}
		}
		out = append(out, ActiveRun{
			RunID:       id,
			AgentName:   m.AgentName,
			ModelName:   m.ModelName,
			TaskID:      m.TaskID,
			ProjectID:   m.ProjectID,
			ProjectSlug: m.ProjectSlug,
			StartedAt:   m.StartedAt,
			LastSeen:    m.LastSeen,
		})
	}
	return out
}

// RegisterFileOperation records a path-scoped tool intent. Filesystem events
// use this hint instead of attributing an edit to whichever unrelated agent
// happened to report telemetry most recently.
func (e *Engine) RegisterFileOperation(filePath, runID string, observedAt time.Time) {
	if strings.TrimSpace(filePath) == "" || strings.TrimSpace(runID) == "" {
		return
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	key := e.correlationPath(filePath)
	if key == "" {
		return
	}

	e.opMu.Lock()
	defer e.opMu.Unlock()
	// Expiry of unrelated paths is handled by prunePendingOps on the engine's
	// maintenance ticker; a per-entry scan here cost O(pendingOps) on every
	// proxy tool call during bursts. The per-key cap below still bounds this
	// path's own slice.
	if len(e.pendingOps) >= 2048 {
		var oldestPath string
		var oldest time.Time
		for path, ops := range e.pendingOps {
			if len(ops) > 0 && (oldest.IsZero() || ops[0].ObservedAt.Before(oldest)) {
				oldestPath, oldest = path, ops[0].ObservedAt
			}
		}
		delete(e.pendingOps, oldestPath)
	}
	ops := e.pendingOps[key]
	// Drop this key's own stale entries so a re-registered path cannot match
	// hints from outside the correlation window.
	cutoff := time.Now().UTC().Add(-fileOperationWindow)
	fresh := ops[:0]
	for _, op := range ops {
		if !op.ObservedAt.Before(cutoff) {
			fresh = append(fresh, op)
		}
	}
	ops = fresh
	if len(ops) >= 8 {
		copy(ops, ops[len(ops)-7:])
		ops = ops[:7]
	}
	e.pendingOps[key] = append(ops, pendingFileOperation{RunID: runID, ObservedAt: observedAt.UTC()})
}

// prunePendingOps drops stale path hints across the whole map. Stale entries
// are harmless to correctness — fileOperationRunID filters by cutoff — so this
// runs on the maintenance ticker rather than on every registration.
func (e *Engine) prunePendingOps() {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	cutoff := time.Now().UTC().Add(-fileOperationWindow)
	for path, ops := range e.pendingOps {
		fresh := ops[:0]
		for _, op := range ops {
			if !op.ObservedAt.Before(cutoff) {
				fresh = append(fresh, op)
			}
		}
		if len(fresh) == 0 {
			delete(e.pendingOps, path)
		} else {
			e.pendingOps[path] = fresh
		}
	}
}

func (e *Engine) fileOperationRunID(filePath string, eventAt time.Time) string {
	key := e.correlationPath(filePath)
	if key == "" {
		return ""
	}
	if eventAt.IsZero() {
		eventAt = time.Now().UTC()
	}

	e.opMu.Lock()
	defer e.opMu.Unlock()
	cutoff := time.Now().UTC().Add(-fileOperationWindow)
	ops := e.pendingOps[key]
	delete(e.pendingOps, key) // a filesystem diff consumes the path hint
	var matchedRun string
	for _, op := range ops {
		if op.ObservedAt.Before(cutoff) || absDuration(eventAt.Sub(op.ObservedAt)) > fileOperationWindow {
			continue
		}
		if matchedRun == "" {
			matchedRun = op.RunID
			continue
		}
		if matchedRun != op.RunID {
			// Multiple agents announced the same path before a filesystem event.
			// Choosing the latest would fabricate authorship, so leave it unknown.
			return ""
		}
	}
	return matchedRun
}

func (e *Engine) correlationPath(filePath string) string {
	p := strings.TrimSpace(strings.ReplaceAll(filePath, "\\", "/"))
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if root := e.WatchRoot(); root != "" {
			p = filepath.Join(root, filepath.FromSlash(p))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return strings.ToLower(filepath.ToSlash(filepath.Clean(p)))
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
