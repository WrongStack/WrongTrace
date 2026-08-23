package core

import (
	"strings"
	"time"
)

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
	e.runMu.Lock()
	defer e.runMu.Unlock()
	cutoff := time.Now().Add(-e.correlate)
	activeProj := e.GetActiveProject()
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
