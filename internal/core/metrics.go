package core

import (
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// MetricsSnapshot is the dashboard's top-level summary. It is built from a
// small set of DB queries (overview, recent events, thrashing, model
// comparison) so the React UI can hit a single endpoint on page load.
type MetricsSnapshot struct {
	Repo         string           `json:"repo"`
	GeneratedAt  time.Time        `json:"generated_at"`
	Overview     db.Overview      `json:"overview"`
	Thrashing    []db.ThrashingRow `json:"thrashing"`
	Models       []db.ModelRow    `json:"models"`
	RecentEvents []db.EventRecord `json:"recent_events"`
	ActiveRuns   []ActiveRun      `json:"active_runs"`
}

// Metrics assembles a fresh snapshot from the underlying store. The queries
// are independent; they run sequentially to respect SQLite's single-writer
// model and keep the query planner simple.
func (e *Engine) Metrics() (MetricsSnapshot, error) {
	overview, err := e.cfg.Store.Overview()
	if err != nil {
		return MetricsSnapshot{}, err
	}
	thrashing, err := e.cfg.Store.Thrashing(3, 7)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	models, err := e.cfg.Store.ModelComparison()
	if err != nil {
		return MetricsSnapshot{}, err
	}
	recent, err := e.cfg.Store.RecentEvents(50)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	return MetricsSnapshot{
		Repo:         e.cfg.RepoName,
		GeneratedAt:  time.Now().UTC(),
		Overview:     overview,
		Thrashing:    thrashing,
		Models:       models,
		RecentEvents: recent,
		ActiveRuns:   e.ActiveRuns(),
	}, nil
}

// WSEvent is the wire format pushed to all dashboard WebSocket clients. The
// Type discriminator lets the UI switch on payload shape without reflection.
type WSEvent struct {
	Type    string      `json:"type"`                 // "code_event", "run_reported", "hello"
	Payload interface{} `json:"payload,omitempty"`    // ast.Event, db.RunRecord, ...
	EventID string      `json:"event_id,omitempty"`
	At      time.Time   `json:"at"`
}
