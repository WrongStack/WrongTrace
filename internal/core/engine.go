// Package core wires the AST engine, filesystem watcher, IPC telemetry stream,
// and database together. It owns the active-run correlation window and
// broadcasts events to the WebSocket hub for the dashboard.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
	"github.com/wrongstack/wrongtrace/internal/models"
	"github.com/wrongstack/wrongtrace/internal/webhook"
)

// IPCHealth is exported as an alias so the server package can reference the
// engine's health type in its EngineAPI interface without importing ipc.
type IPCHealth = ipc.FileHealthReply

func ipcHealthFromDB(h db.FileHealth) IPCHealth {
	return IPCHealth{
		FilePath:             h.FilePath,
		HealthScore:          h.HealthScore,
		IsFragile:            h.IsFragile,
		RecentThrashingCount: h.RecentThrashingCount,
		Warning:              h.Warning,
	}
}

// Config configures an Engine.
type Config struct {
	RepoName string
	Store    *db.Store
	AST      *ast.Engine
	// WatchDir is the root the filesystem watcher observes. Ignore-directory
	// filtering is evaluated relative to it; leaving it empty defers that
	// filtering entirely to the watcher. PrimeDirectory back-fills it.
	WatchDir string
}

// Engine is the heart of WrongTrace. It is safe for concurrent use and exposes
// the surface both the watcher (filesystem events) and the IPC/MCP layers
// (telemetry events) need to drive.
type Engine struct {
	cfg Config
	hub *Hub

	runMu      sync.Mutex
	activeRuns map[string]runMeta
	correlate  time.Duration
	opMu       sync.Mutex
	pendingOps map[string][]pendingFileOperation

	lockMu          sync.RWMutex
	lockedFiles     map[string]LockInfo
	projects        map[string]ProjectProfile
	activeProjectID string
	watcher         WatcherAPI
	webhooks        *webhook.Dispatcher

	indexMu     sync.RWMutex
	indexStatus IndexProgress

	primeMu     sync.Mutex
	primeCancel context.CancelFunc

	ipcMu      sync.RWMutex
	ipcTraffic []ipc.IPCTrafficRecord

	rootMu    sync.RWMutex
	watchRoot string

	cacheMu      sync.RWMutex
	cacheGen     uint64
	atlasCache   map[string]cachedAtlas
	metricsCache map[string]cachedMetrics
	recentCache  map[string]cachedRecent
}

// BumpCacheGen increments the cache generation counter, invalidating in-memory Atlas, Metrics, and RecentEvents caches.
func (e *Engine) BumpCacheGen() {
	e.cacheMu.Lock()
	e.cacheGen++
	// Cached atlas snapshots can retain several megabytes of source-derived
	// symbol data per project. A generation bump makes every entry unusable, so
	// drop the maps immediately instead of retaining stale payloads until each
	// individual key happens to be requested and overwritten again.
	e.atlasCache = nil
	e.metricsCache = nil
	e.recentCache = nil
	e.cacheMu.Unlock()
}

// runMeta is the metadata kept for an active (or recently-seen) agent run —
// just enough to back-fill run_id on subsequent AST events.
type runMeta struct {
	AgentName   string
	ModelName   string
	ProjectID   string
	ProjectSlug string
	StartedAt   time.Time
	LastSeen    time.Time
	TaskID      string
}

type pendingFileOperation struct {
	RunID      string
	ObservedAt time.Time
}

// NewEngine constructs an Engine. Pass a nil AST to skip file parsing (the MCP
// subcommand uses this since it never touches the filesystem).
func NewEngine(cfg Config) *Engine {
	loadedProjects := LoadProjectsIndex()
	settingsMu.RLock()
	settings := globalSettings
	settingsMu.RUnlock()

	activeProjID := ""
	for id, p := range loadedProjects {
		if cfg.RepoName != "" && cfg.RepoName != "default" {
			if strings.EqualFold(p.Name, cfg.RepoName) || strings.EqualFold(p.ID, cfg.RepoName) {
				activeProjID = id
				break
			}
		} else if p.IsActive {
			activeProjID = id
			cfg.RepoName = p.Name
			break
		}
	}
	if activeProjID != "" {
		changed := false
		for k, p := range loadedProjects {
			expectedActive := (k == activeProjID)
			if p.IsActive != expectedActive {
				p.IsActive = expectedActive
				loadedProjects[k] = p
				changed = true
			}
		}
		if changed {
			SaveProjectsIndex(loadedProjects)
		}
	}
	if cfg.RepoName == "" {
		cfg.RepoName = "default"
	}

	dispatcher := webhook.NewDispatcher(webhook.Config{
		SlackURL:      settings.SlackWebhookURL,
		DiscordURL:    settings.DiscordWebhookURL,
		GenericURL:    settings.CustomWebhookURL,
		SigningSecret: os.Getenv("WRONGTRACE_WEBHOOK_SECRET"),
	})

	return &Engine{
		cfg:             cfg,
		watchRoot:       absClean(cfg.WatchDir),
		hub:             NewHub(),
		activeRuns:      make(map[string]runMeta),
		correlate:       10 * time.Minute,
		pendingOps:      make(map[string][]pendingFileOperation),
		lockedFiles:     make(map[string]LockInfo),
		projects:        loadedProjects,
		activeProjectID: activeProjID,
		webhooks:        dispatcher,
	}
}

// Hub exposes the WebSocket broadcaster. The server package reads from it.
func (e *Engine) Hub() *Hub { return e.hub }

// Store exposes the underlying analytical database store.
func (e *Engine) Store() *db.Store {
	e.lockMu.RLock()
	defer e.lockMu.RUnlock()
	return e.cfg.Store
}

// repoName snapshots cfg.RepoName under lockMu. SwitchActiveProject writes the
// field under the same mutex while watcher goroutines and HTTP handlers read
// it concurrently, so an unlocked read is a data race on the string header.
func (e *Engine) repoName() string {
	e.lockMu.RLock()
	defer e.lockMu.RUnlock()
	return e.cfg.RepoName
}

// Repo returns the active repository or project name.
func (e *Engine) Repo() string {
	name := e.repoName()
	if name != "" && name != "default" {
		return name
	}
	if active := e.GetActiveProject(); active != nil && active.Name != "" {
		return active.Name
	}
	return name
}

// HandleFileChange is invoked by the watcher after the debounce timer fires.
// It re-parses the file, computes the semantic diff against the cached
// snapshot, persists each event, and broadcasts it to the WebSocket hub.
func (e *Engine) HandleFileChange(ctx context.Context, path string) {
	if e.cfg.AST == nil {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			e.handleFileGone(ctx, path)
			return
		}
		log.Printf("engine: stat %s: %v", path, err)
		return
	}
	// Skip directories, files over 5MB (avoids parsing huge logs/binaries), and ignored paths
	if info.IsDir() || info.Size() > 5*1024*1024 || e.shouldSkip(path) {
		return
	}

	repoName := e.repoName()
	if proj, ok := e.FindProjectForFile(path); ok && proj.Name != "" {
		repoName = proj.Name
	}

	src, err := os.ReadFile(path)
	if err != nil {
		log.Printf("engine: read %s: %v", path, err)
		return
	}

	// Fast-path: if file was touched without content changes, skip AST parse and Diff entirely
	prev, _ := e.cfg.AST.Snapshot(path)
	if prev != nil && prev.Hash == ast.HashBytes(src) {
		return
	}

	snap, err := e.cfg.AST.Parse(path, src)
	if err != nil || snap == nil {
		return
	}
	res := ast.Diff(repoName, prev, snap)
	e.cfg.AST.SetSnapshot(snap)

	if len(res.Events) == 0 {
		return
	}
	e.persistAndBroadcast(res)
}

// handleFileGone emits a DELETED event for every cached node in the now-gone
// file, then drops the snapshot.
func (e *Engine) handleFileGone(_ context.Context, path string) {
	if e.cfg.AST == nil {
		return
	}
	repoName := e.repoName()
	if proj, ok := e.FindProjectForFile(path); ok && proj.Name != "" {
		repoName = proj.Name
	}
	prev, ok := e.cfg.AST.Snapshot(path)
	if !ok || prev == nil {
		return
	}
	e.cfg.AST.Forget(path)
	res := ast.Diff(repoName, prev, nil)
	e.persistAndBroadcast(res)
}

// persistAndBroadcast writes each event to the DB and pushes it to the WS hub.
// run_id is back-filled from the most recent active run when one exists within
// the correlation window.
func (e *Engine) persistAndBroadcast(res ast.DiffResult) {
	var runID, attributionSource string
	var attributionConfidence float64
	if len(res.Events) > 0 {
		runID = e.fileOperationRunID(res.Events[0].FilePath, res.Events[0].OccurredAt)
	}
	if runID != "" {
		attributionSource = "tool_path"
		attributionConfidence = 0.95
	} else {
		runID = e.recentRunID()
		if runID != "" {
			attributionSource = "single_active_run"
			attributionConfidence = 0.60
		} else {
			attributionSource = "unknown"
		}
	}
	for _, ev := range res.Events {
		if runID != "" && ev.RunID == "" {
			ev.RunID = runID
		}
		repo := ev.RepoName
		if repo == "" {
			repo = e.repoName()
		}
		if p, ok := e.FindProjectForFile(ev.FilePath); ok && p.Name != "" {
			repo = p.Name
		}
		rec := db.EventRecord{
			EventID:               newID(),
			RunID:                 ev.RunID,
			RepoName:              repo,
			FilePath:              ev.FilePath,
			Signature:             ev.Signature,
			NodeType:              string(ev.NodeType),
			Action:                string(ev.Action),
			BodyHash:              ev.BodyHash,
			LOC:                   ev.LOC,
			StartLine:             ev.StartLine,
			EndLine:               ev.EndLine,
			DiffSnippet:           ev.DiffSnippet,
			AddedLines:            ev.AddedLines,
			DeletedLines:          ev.DeletedLines,
			AttributionSource:     attributionSource,
			AttributionConfidence: attributionConfidence,
			OccurredAt:            ev.OccurredAt,
		}
		store := e.Store()
		if store != nil {
			if err := store.InsertEvent(rec); err != nil {
				log.Printf("engine: insert event %s: %v", rec.EventID, err)
				continue
			}
		}
		e.hub.Broadcast(WSEvent{Type: "code_event", Payload: ev, EventID: rec.EventID})
	}
	if len(res.Events) > 0 {
		e.BumpCacheGen()
	}
}

// absClean normalizes a directory into an absolute, cleaned path. An empty or
// unresolvable input yields "", which callers read as "no root known".
func absClean(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

// adoptWatchRoot records the directory the engine observes unless one is
// already known. PrimeDirectory calls it so the daemon and the tests share
// the same ignore scoping without extra wiring.
func (e *Engine) adoptWatchRoot(dir string) {
	root := absClean(dir)
	if root == "" {
		return
	}
	e.rootMu.Lock()
	defer e.rootMu.Unlock()
	if e.watchRoot == "" {
		e.watchRoot = root
	}
}

// WatchRoot returns the directory ignore rules are scoped to, or "" when the
// engine has not been given one.
func (e *Engine) WatchRoot() string {
	e.rootMu.RLock()
	defer e.rootMu.RUnlock()
	return e.watchRoot
}

// ignoredPathSegment reports whether any segment of the path names an
// ignored directory (case-insensitive, via the shared isIgnoredDir
// predicate). Callers must pass a path already scoped to a project root --
// see shouldSkip.
func ignoredPathSegment(path string) bool {
	norm := filepath.ToSlash(path)
	for len(norm) > 0 {
		idx := strings.IndexByte(norm, '/')
		var seg string
		if idx == -1 {
			seg = norm
			norm = ""
		} else {
			seg = norm[:idx]
			norm = norm[idx+1:]
		}
		if isIgnoredDir(seg) {
			return true
		}
	}
	return false
}

// parseEligible reports whether a file should be parsed into the AST cache:
// supported language and not a pathological bundle. Directory-ignore
// filtering is deliberately NOT part of this — PrimeDirectory's walk already
// prunes ignored subtrees with filepath.SkipDir, and re-checking segments
// here would false-positive on workspaces whose own root (or an ancestor)
// happens to be named like an ignore entry (e.g. a workspace named "bin").
func (e *Engine) parseEligible(path string) bool {
	if ast.DetectLanguage(path) == ast.LangUnknown {
		return false
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 4*1024*1024 {
		return false
	}
	return true
}

// shouldSkip filters files we never want to watch: ignored directories,
// unsupported languages, and pathologies like very large generated bundles.
//
// The ignore check runs against the path RELATIVE to the watch root. Matching
// the absolute path made every ancestor segment count, so a checkout living
// under /tmp, ~/build or C:in matched the ignore list at its own root and
// every file in the project was silently dropped -- exactly the false
// positive parseEligible documents. Without a known root (and for paths
// outside it) the watcher, which prunes ignored subtrees at registration
// time, remains the only filter.
func (e *Engine) shouldSkip(path string) bool {
	if !e.parseEligible(path) {
		return true
	}
	root := e.WatchRoot()
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return ignoredPathSegment(rel)
}

// Run performs lightweight in-memory cleanup and low-frequency retention
// maintenance until ctx is done. Go's runtime owns GC pacing; this loop does
// not force collections or memory scavenges.
func (e *Engine) Run(ctx context.Context) {
	runTicker := time.NewTicker(2 * time.Minute)
	retentionTicker := time.NewTicker(24 * time.Hour)
	defer runTicker.Stop()
	defer retentionTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-runTicker.C:
			e.pruneActiveRuns()
		case <-retentionTicker.C:
			days := e.GetSettings().AutoPruneDays
			if days <= 0 {
				continue
			}
			deleted, err := e.ClearStale(days)
			if err != nil {
				log.Printf("retention: prune telemetry older than %d days: %v", days, err)
			} else if deleted > 0 {
				log.Printf("retention: pruned %d telemetry rows older than %d days", deleted, days)
			}
		}
	}
}

func (e *Engine) pruneActiveRuns() {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	cutoff := time.Now().Add(-e.correlate)
	for id, meta := range e.activeRuns {
		if meta.LastSeen.Before(cutoff) {
			delete(e.activeRuns, id)
		}
	}
}

// ----------------------------------------------------
// Telemetry sink (IPC + MCP)
// ----------------------------------------------------

// ReportRun is invoked by the IPC server for "telemetry/report_run". It writes
// the agent_runs row, records the run in the in-memory active set, and
// broadcasts a "run" event for the dashboard badge.
func (e *Engine) ReportRun(p ipc.TelemetryReport) error {
	if p.RunID == "" {
		return errors.New("run_id is required")
	}

	if p.AgentName == "" {
		if p.ProjectSlug != "" || p.ProjectID != "" {
			p.AgentName = "WrongStack"
		} else {
			p.AgentName = "Agent"
		}
	}

	if p.ModelName == "" || models.IsJunkModel(p.ModelName) {
		p.ModelName = "unknown-model"
	}

	// Auto-compute cost if not explicitly passed by agent but tokens are provided
	if p.CostUSD <= 0 && (p.PromptTokens > 0 || p.CompletionTokens > 0) {
		p.CostUSD = models.Global.CalculateCost(p.ModelName, p.PromptTokens, p.CompletionTokens)
	}

	rec := db.RunRecord{
		RunID:            p.RunID,
		TaskID:           p.TaskID,
		AgentName:        p.AgentName,
		ModelName:        p.ModelName,
		Provider:         p.Provider,
		PromptTokens:     p.PromptTokens,
		CompletionTokens: p.CompletionTokens,
		CostUSD:          p.CostUSD,
		Intent:           p.Intent,
		CreatedAt:        time.Now().UTC(),
	}
	st := e.Store()
	if st == nil {
		return fmt.Errorf("active store is not initialized")
	}
	if err := st.UpsertRun(rec); err != nil {
		return fmt.Errorf("upsert run: %w", err)
	}
	e.runMu.Lock()
	if len(e.activeRuns) > 250 {
		cutoff := time.Now().Add(-e.correlate)
		for id, m := range e.activeRuns {
			if m.LastSeen.Before(cutoff) {
				delete(e.activeRuns, id)
			}
		}
		// If still exceeding cap, evict oldest entries
		if len(e.activeRuns) > 250 {
			var oldestID string
			var oldestTime time.Time
			for id, m := range e.activeRuns {
				if oldestTime.IsZero() || m.LastSeen.Before(oldestTime) {
					oldestTime = m.LastSeen
					oldestID = id
				}
			}
			if oldestID != "" {
				delete(e.activeRuns, oldestID)
			}
		}
	}
	existing, exists := e.activeRuns[p.RunID]
	startedAt := rec.CreatedAt
	if exists && !existing.StartedAt.IsZero() {
		startedAt = existing.StartedAt
	}
	e.activeRuns[p.RunID] = runMeta{
		AgentName:   p.AgentName,
		ModelName:   p.ModelName,
		ProjectID:   p.ProjectID,
		ProjectSlug: p.ProjectSlug,
		StartedAt:   startedAt,
		LastSeen:    rec.CreatedAt,
		TaskID:      p.TaskID,
	}
	e.runMu.Unlock()
	e.BumpCacheGen()
	e.hub.Broadcast(WSEvent{Type: "run_reported", Payload: rec})
	return nil
}

// ModelCatalog returns all available AI models and their token pricing specs.
func (e *Engine) ModelCatalog() []models.ModelInfo {
	return models.Global.AllModels()
}

// ProviderCatalog returns all AI providers, their API endpoints, SDK adapters, and hosted models.
func (e *Engine) ProviderCatalog() []models.ProviderInfo {
	return models.Global.AllProviders()
}

// UpsertModel updates or adds a custom model into the catalog.
func (e *Engine) UpsertModel(m models.ModelInfo) {
	models.Global.Upsert(m)
}

// CalculateCost computes total dollar spend from tokens for a specific model.
func (e *Engine) CalculateCost(model string, promptTokens, completionTokens int64) float64 {
	return models.Global.CalculateCost(model, promptTokens, completionTokens)
}

// SyncModelsDev fetches live model pricing from models.dev/api.json and merges it into the registry.
func (e *Engine) SyncModelsDev() (int, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get("https://models.dev/api.json")
	if err != nil {
		return 0, fmt.Errorf("fetch models.dev: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("models.dev returned status %d", resp.StatusCode)
	}

	data, err := readModelsDevResponse(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read response body: %w", err)
	}

	return models.Global.ImportModelsDevJSON(data)
}

const maxModelsDevResponseBytes = 64 * 1024 * 1024

func readModelsDevResponse(r io.Reader) ([]byte, error) {
	return readBoundedResponse(r, maxModelsDevResponseBytes)
}

func readBoundedResponse(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("models.dev response exceeds %d bytes", limit)
	}
	return data, nil
}

// ReportRunMCP adapts the MCP tool's flat arguments into a full run record,
// generating a run_id when the agent did not supply one. It returns the run_id
// so the MCP tool response can surface it.
func (e *Engine) ReportRunMCP(model, provider, taskID, intent string, promptTokens, completionTokens int64, cost float64) (string, error) {
	runID := newID()
	err := e.ReportRun(ipc.TelemetryReport{
		RunID:            runID,
		TaskID:           taskID,
		AgentName:        "MCP",
		ModelName:        model,
		Provider:         provider,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CostUSD:          cost,
		Intent:           intent,
	})
	if err != nil {
		return "", err
	}
	return runID, nil
}

// FileHealth is invoked by IPC and MCP clients for the fragility guardrail.
func (e *Engine) FileHealth(path string) (IPCHealth, error) {
	store := e.Store()
	if store == nil {
		return IPCHealth{FilePath: path, HealthScore: 100}, nil
	}
	h, err := store.FileHealth(path)
	if err != nil {
		return IPCHealth{}, err
	}
	res := ipcHealthFromDB(h)
	if locked, lockInfo := e.IsFileLocked(path); locked {
		res.IsLocked = true
		res.LockReason = lockInfo.Reason
		res.LockOwner = lockInfo.Owner
		res.LockOwnerRunID = lockInfo.OwnerRunID
		if !lockInfo.ExpiresAt.IsZero() {
			exp := lockInfo.ExpiresAt
			res.LockExpiresAt = &exp
		}
	}
	return res, nil
}

// Ping verifies the daemon is alive.
func (e *Engine) Ping() error { return nil }

// RecordReadEvent writes a file read/inspection event into analytical storage and broadcasts it.
func (e *Engine) RecordReadEvent(rec db.FileReadRecord) error {
	if rec.FilePath == "" {
		return nil
	}
	if rec.ReadID == "" {
		rec.ReadID = newID()
	}
	if rec.RepoName == "" {
		rec.RepoName = e.repoName()
		if proj, ok := e.FindProjectForFile(rec.FilePath); ok {
			rec.RepoName = proj.Name
		}
	}
	if rec.ModelName == "" || models.IsJunkModel(rec.ModelName) {
		rec.ModelName = "unknown-model"
	}
	if rec.CostUSD <= 0 && rec.PromptTokens > 0 {
		rec.CostUSD = models.Global.CalculateCost(rec.ModelName, rec.PromptTokens, 0)
	}
	if rec.ReadTime.IsZero() {
		rec.ReadTime = time.Now().UTC()
	}
	if rec.RunID == "" {
		rec.RunID = e.recentRunID()
	}

	store := e.Store()
	if store != nil {
		if err := store.InsertReadEvent(rec); err != nil {
			return fmt.Errorf("insert read event: %w", err)
		}
	}
	e.BumpCacheGen()
	e.hub.Broadcast(WSEvent{Type: "file_read_event", Payload: rec, EventID: rec.ReadID})
	return nil
}

// GetFileReadStats returns aggregated read metrics for a given file.
func (e *Engine) GetFileReadStats(filePath string) (db.FileReadStats, error) {
	store := e.Store()
	if store == nil {
		return db.FileReadStats{FilePath: filePath}, nil
	}
	return store.GetFileReadStats(filePath)
}

// GetRecentEvents returns the most recent code mutation and diff events.
func (e *Engine) GetRecentEvents(limit int, repoFilter ...string) ([]db.EventRecord, error) {
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	var filter string
	if len(repoFilter) > 0 && repoFilter[0] != "" {
		filter = repoFilter[0]
	} else if active := e.GetActiveProject(); active != nil && active.Name != "" {
		filter = active.Name
	}
	return store.RecentEvents(limit, filter)
}

// cachedRecent memoizes one since-less, file-less RecentEventsFiltered result
// per (limit, repo) key, mirroring the Metrics/Atlas gen+TTL pattern: the
// dashboard's every-10s recent-events polls stop re-running the 500-row SQL
// while nothing changed. Cursored (since) or file-scoped queries always hit
// the store directly so incremental fetches stay exact.
type cachedRecent struct {
	gen      uint64
	cachedAt time.Time
	events   []db.EventRecord
}

const recentCacheTTL = 2 * time.Second

// GetRecentEventsFiltered queries recent events with flexible repository, file, and timestamp constraints.
func (e *Engine) GetRecentEventsFiltered(limit int, repo string, filePath string, since time.Time) ([]db.EventRecord, error) {
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	cacheable := filePath == "" && since.IsZero()
	key := fmt.Sprintf("%d\x00%s", limit, repo)
	if cacheable {
		e.cacheMu.RLock()
		if cached, ok := e.recentCache[key]; ok && cached.gen == e.cacheGen && time.Since(cached.cachedAt) < recentCacheTTL {
			e.cacheMu.RUnlock()
			return cached.events, nil
		}
		e.cacheMu.RUnlock()
	}
	events, err := store.RecentEventsFiltered(limit, repo, filePath, since)
	if err != nil {
		return nil, err
	}
	if cacheable {
		e.cacheMu.Lock()
		if e.recentCache == nil {
			e.recentCache = make(map[string]cachedRecent)
		}
		e.recentCache[key] = cachedRecent{gen: e.cacheGen, cachedAt: time.Now(), events: events}
		e.cacheMu.Unlock()
	}
	return events, nil
}

// GetRecentFileEvents returns recent diff and AST events specifically matching a file.
func (e *Engine) GetRecentFileEvents(filePath string, limit int) ([]db.EventRecord, error) {
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	return store.RecentFileEvents(filePath, limit)
}

// GetRecentFileReads returns the most recent read records across the system, optionally filtered by repo_name.
func (e *Engine) GetRecentFileReads(limit int, repoFilter ...string) ([]db.FileReadRecord, error) {
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	var filter string
	if len(repoFilter) > 0 && repoFilter[0] != "" {
		filter = repoFilter[0]
	} else if active := e.GetActiveProject(); active != nil && active.Name != "" {
		filter = active.Name
	}
	return store.GetRecentFileReads(limit, filter)
}

// GetFileReadHeatmap returns line range frequencies for a file.
func (e *Engine) GetFileReadHeatmap(filePath string) ([]db.LineReadHeatmap, error) {
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	return store.GetFileReadHeatmap(filePath)
}

// GetSymbolHistory returns the chronological evolution and revision history of an AST symbol.
func (e *Engine) GetSymbolHistory(filePath, signature string, limit int) ([]db.SymbolHistoryRecord, error) {
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	return store.SymbolHistory(filePath, signature, limit)
}

// GetFileModelActivity returns per-model read vs write activity breakdown for a file.
func (e *Engine) GetFileModelActivity(filePath string) ([]db.ModelActivitySummary, error) {
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	return store.FileModelActivity(filePath)
}

// GetAllFileModelActivity returns aggregated model activity across all files.
func (e *Engine) GetAllFileModelActivity(limit int) ([]db.ModelActivitySummary, error) {
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	return store.AllFileModelActivity(limit)
}

// GetModelFrictionReport returns inter-agent cross-thrashing and collision analytics.
func (e *Engine) GetModelFrictionReport(limit int) (*db.InterAgentFrictionReport, error) {
	store := e.Store()
	if store == nil {
		return nil, nil
	}
	return store.ModelFrictionMatrix(limit)
}

// IndexStatus returns the current codebase indexing progress and stats.
func (e *Engine) IndexStatus() IndexProgress {
	e.indexMu.RLock()
	defer e.indexMu.RUnlock()
	return e.indexStatus
}

// RecordIPCTraffic stores and broadcasts a live JSON-RPC interaction from an IPC client (e.g. WrongStack).
func (e *Engine) RecordIPCTraffic(rec ipc.IPCTrafficRecord) {
	rec = compactIPCTraffic(rec)
	e.ipcMu.Lock()
	if e.ipcTraffic == nil {
		e.ipcTraffic = make([]ipc.IPCTrafficRecord, 0, 100)
	}
	if len(e.ipcTraffic) >= 100 {
		copy(e.ipcTraffic, e.ipcTraffic[1:])
		e.ipcTraffic[len(e.ipcTraffic)-1] = rec
	} else {
		e.ipcTraffic = append(e.ipcTraffic, rec)
	}
	e.ipcMu.Unlock()

	if e.hub != nil {
		e.hub.Broadcast(WSEvent{
			Type:    "ipc_traffic",
			Payload: ipcTrafficSummary(rec),
		})
	}
}

// GetIPCTraffic returns recent recorded IPC interactions in reverse chronological order.
func (e *Engine) GetIPCTraffic() []ipc.IPCTrafficRecord {
	e.ipcMu.RLock()
	defer e.ipcMu.RUnlock()
	if len(e.ipcTraffic) == 0 {
		return []ipc.IPCTrafficRecord{}
	}
	out := make([]ipc.IPCTrafficRecord, len(e.ipcTraffic))
	copy(out, e.ipcTraffic)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// recentRunID returns a run only when exactly one run is active in the
// correlation window. With concurrent agents, "last seen wins" silently
// assigns another model's edit to the newest telemetry report; ambiguous
// events must remain unattributed unless a path-scoped tool hint exists.
func (e *Engine) recentRunID() string {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	cutoff := time.Now().Add(-e.correlate)
	var onlyID string
	active := 0
	for id, meta := range e.activeRuns {
		if meta.LastSeen.Before(cutoff) {
			delete(e.activeRuns, id)
			continue
		}
		active++
		onlyID = id
	}
	if active == 1 {
		return onlyID
	}
	return ""
}

// newID returns a random 16-byte hex identifier for DB primary keys.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}
