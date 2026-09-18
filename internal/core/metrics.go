package core

import (
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// MetricsSnapshot is the dashboard's top-level summary. It is built from a
// small set of DB queries (overview, recent events, thrashing, model
// comparison) so the React UI can hit a single endpoint on page load.
type MetricsSnapshot struct {
	Repo         string            `json:"repo"`
	GeneratedAt  time.Time         `json:"generated_at"`
	Overview     db.Overview       `json:"overview"`
	Thrashing    []db.ThrashingRow `json:"thrashing"`
	Models       []db.ModelRow     `json:"models"`
	RecentEvents []db.EventRecord  `json:"recent_events"`
	ActiveRuns   []ActiveRun       `json:"active_runs"`
}

type cachedMetrics struct {
	gen      uint64
	cachedAt time.Time
	snapshot MetricsSnapshot
}

// metricsFilter resolves the repo filter shared by the metrics endpoints:
// explicit filter first, then the active project, then the configured repo.
func (e *Engine) metricsFilter(repoFilter ...string) string {
	if len(repoFilter) > 0 && repoFilter[0] != "" {
		return repoFilter[0]
	}
	if active := e.GetActiveProject(); active != nil && active.Name != "" {
		return active.Name
	}
	return e.repoName()
}

// metricsCacheTTL bounds how long a snapshot is reused while its generation
// is still current. Every in-process write the snapshot is derived from bumps
// the generation, so the TTL only has to cover writes the generation cannot
// see -- a separate `wrongtrace mcp` process sharing the database -- and the
// drift of the time-windowed aggregates. At 2s it expired between the
// dashboard's parallel fetches and re-ran the heavy scans for each of them.
const metricsCacheTTL = 30 * time.Second

// metricsCall is one in-flight snapshot build that concurrent callers share.
type metricsCall struct {
	done chan struct{}
	res  MetricsSnapshot
	err  error
}

// metricsCacheFresh returns the cached snapshot for filter when it is current.
// ActiveRuns is time-dependent and cheap, so it is always re-read live rather
// than served from the cache.
func (e *Engine) metricsCacheFresh(filter string) (MetricsSnapshot, bool) {
	e.cacheMu.RLock()
	cached, ok := e.metricsCache[filter]
	ok = ok && cached.gen == e.cacheGen && time.Since(cached.cachedAt) < metricsCacheTTL
	e.cacheMu.RUnlock()
	if !ok {
		return MetricsSnapshot{}, false
	}
	snap := cached.snapshot
	snap.ActiveRuns = e.ActiveRuns()
	return snap, true
}

// Metrics assembles a fresh snapshot from the underlying store, optionally filtered by repo_name.
//
// The dashboard fetches overview, thrashing and models in parallel right after
// every invalidation. Concurrent callers for the same filter share one build
// instead of each running the full set of aggregate scans.
func (e *Engine) Metrics(repoFilter ...string) (MetricsSnapshot, error) {
	filter := e.metricsFilter(repoFilter...)

	if cached, ok := e.metricsCacheFresh(filter); ok {
		return cached, nil
	}

	e.cacheMu.Lock()
	if call, ok := e.metricsCalls[filter]; ok {
		e.cacheMu.Unlock()
		<-call.done
		return call.res, call.err
	}
	call := &metricsCall{done: make(chan struct{})}
	if e.metricsCalls == nil {
		e.metricsCalls = make(map[string]*metricsCall)
	}
	e.metricsCalls[filter] = call
	gen := e.cacheGen
	e.cacheMu.Unlock()

	defer func() {
		e.cacheMu.Lock()
		delete(e.metricsCalls, filter)
		e.cacheMu.Unlock()
		close(call.done)
	}()
	call.res, call.err = e.buildMetrics(filter, gen)
	return call.res, call.err
}

// buildMetrics runs the snapshot queries and caches the result under gen, the
// generation observed before the queries started, so a write that lands
// mid-build leaves the entry stale instead of caching pre-write data as current.
func (e *Engine) buildMetrics(filter string, gen uint64) (MetricsSnapshot, error) {
	store := e.Store()
	if store == nil {
		return MetricsSnapshot{
			Repo:        filter,
			GeneratedAt: time.Now().UTC(),
			ActiveRuns:  e.ActiveRuns(),
		}, nil
	}

	overview, err := store.Overview(filter)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	thrashing, err := store.Thrashing(3, 7, filter)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	models, err := store.ModelComparison(filter)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	recent, err := store.RecentEvents(50, filter)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	res := MetricsSnapshot{
		Repo:         filter,
		GeneratedAt:  time.Now().UTC(),
		Overview:     overview,
		Thrashing:    thrashing,
		Models:       models,
		RecentEvents: recent,
		ActiveRuns:   e.ActiveRuns(),
	}

	e.cacheMu.Lock()
	// The filter comes from the ?repo= query string; clear wholesale past the
	// cap so arbitrary values cannot grow the map without bound.
	if len(e.metricsCache) >= 64 {
		e.metricsCache = make(map[string]cachedMetrics)
	}
	if e.metricsCache == nil {
		e.metricsCache = make(map[string]cachedMetrics)
	}
	e.metricsCache[filter] = cachedMetrics{
		gen:      gen,
		cachedAt: time.Now(),
		snapshot: res,
	}
	e.cacheMu.Unlock()

	return res, nil
}

// metricsInFlight waits for a snapshot build already running for filter and
// returns it. It reports false when none is running.
func (e *Engine) metricsInFlight(filter string) (MetricsSnapshot, bool) {
	e.cacheMu.RLock()
	call, ok := e.metricsCalls[filter]
	e.cacheMu.RUnlock()
	if !ok {
		return MetricsSnapshot{}, false
	}
	<-call.done
	return call.res, call.err == nil
}

// ThrashingRows returns just the fragile-node panel. Standalone tooling polls
// this endpoint without needing the full snapshot, whose Overview and
// ModelComparison CTEs are the most expensive scans in the store — so this
// skips them entirely and only reuses the snapshot cache when it is warm.
func (e *Engine) ThrashingRows(repoFilter ...string) ([]db.ThrashingRow, error) {
	filter := e.metricsFilter(repoFilter...)
	if cached, ok := e.metricsCacheFresh(filter); ok {
		return cached.Thrashing, nil
	}
	if snap, ok := e.metricsInFlight(filter); ok {
		return snap.Thrashing, nil
	}
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	return store.Thrashing(3, 7, filter)
}

// ModelRows returns just the per-model survival and ROI comparison, without
// assembling the rest of the metrics snapshot.
func (e *Engine) ModelRows(repoFilter ...string) ([]db.ModelRow, error) {
	filter := e.metricsFilter(repoFilter...)
	if cached, ok := e.metricsCacheFresh(filter); ok {
		return cached.Models, nil
	}
	if snap, ok := e.metricsInFlight(filter); ok {
		return snap.Models, nil
	}
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	return store.ModelComparison(filter)
}

// WSEvent is the wire format pushed to all dashboard WebSocket clients. The
// Type discriminator lets the UI switch on payload shape without reflection.
type WSEvent struct {
	Type    string      `json:"type"`              // "code_event", "run_reported", "hello"
	Payload interface{} `json:"payload,omitempty"` // ast.Event, db.RunRecord, ...
	EventID string      `json:"event_id,omitempty"`
	At      time.Time   `json:"at"`
	// Wire carries the pre-serialized JSON frame set by Hub.Broadcast so N
	// subscribers share one encode instead of marshaling identical payloads
	// per connection. Consumers fall back to marshaling when it is nil.
	Wire []byte `json:"-"`
}
