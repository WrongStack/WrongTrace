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

	lockMu          sync.RWMutex
	lockedFiles     map[string]bool
	projects        map[string]ProjectProfile
	activeProjectID string
	watcher         WatcherAPI
	webhooks        *webhook.Dispatcher

	indexMu     sync.RWMutex
	indexStatus IndexProgress
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

// NewEngine constructs an Engine. Pass a nil AST to skip file parsing (the MCP
// subcommand uses this since it never touches the filesystem).
func NewEngine(cfg Config) *Engine {
	loadedProjects := LoadProjectsIndex()
	settings := globalSettings

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
	if cfg.RepoName == "" {
		cfg.RepoName = "default"
	}

	dispatcher := webhook.NewDispatcher(webhook.Config{
		SlackURL:   settings.SlackWebhookURL,
		DiscordURL: settings.DiscordWebhookURL,
		GenericURL: settings.CustomWebhookURL,
	})

	return &Engine{
		cfg:             cfg,
		hub:             NewHub(),
		activeRuns:      make(map[string]runMeta),
		correlate:       10 * time.Minute,
		lockedFiles:     make(map[string]bool),
		projects:        loadedProjects,
		activeProjectID: activeProjID,
		webhooks:        dispatcher,
	}
}

// Hub exposes the WebSocket broadcaster. The server package reads from it.
func (e *Engine) Hub() *Hub { return e.hub }

// Store exposes the underlying analytical database store.
func (e *Engine) Store() *db.Store { return e.cfg.Store }

// Repo returns the active repository or project name.
func (e *Engine) Repo() string {
	if e.cfg.RepoName != "" && e.cfg.RepoName != "default" {
		return e.cfg.RepoName
	}
	if active := e.GetActiveProject(); active != nil && active.Name != "" {
		return active.Name
	}
	return e.cfg.RepoName
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
	if info.IsDir() || e.shouldSkip(path) {
		return
	}

	repoName := e.cfg.RepoName
	if proj, ok := e.FindProjectForFile(path); ok && proj.Name != "" {
		repoName = proj.Name
	}

	src, err := os.ReadFile(path)
	if err != nil {
		log.Printf("engine: read %s: %v", path, err)
		return
	}
	snap, err := e.cfg.AST.Parse(path, src)
	if err != nil || snap == nil {
		return
	}
	prev, _ := e.cfg.AST.Snapshot(path)
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
	repoName := e.cfg.RepoName
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
	runID := e.recentRunID()
	for _, ev := range res.Events {
		if runID != "" && ev.RunID == "" {
			ev.RunID = runID
		}
		repo := ev.RepoName
		if repo == "" {
			repo = e.cfg.RepoName
		}
		if p, ok := e.FindProjectForFile(ev.FilePath); ok && p.Name != "" {
			repo = p.Name
		}
		rec := db.EventRecord{
			EventID:      newID(),
			RunID:        ev.RunID,
			RepoName:     repo,
			FilePath:     ev.FilePath,
			Signature:    ev.Signature,
			NodeType:     string(ev.NodeType),
			Action:       string(ev.Action),
			BodyHash:     ev.BodyHash,
			LOC:          ev.LOC,
			StartLine:    ev.StartLine,
			EndLine:      ev.EndLine,
			DiffSnippet:  ev.DiffSnippet,
			AddedLines:   ev.AddedLines,
			DeletedLines: ev.DeletedLines,
			OccurredAt:   ev.OccurredAt,
		}
		if err := e.cfg.Store.InsertEvent(rec); err != nil {
			log.Printf("engine: insert event %s: %v", rec.EventID, err)
			continue
		}
		e.hub.Broadcast(WSEvent{Type: "code_event", Payload: ev, EventID: rec.EventID})
	}
}

// ignoredPathSegment reports whether any segment of the path names an
// ignored directory (case-insensitive, via the shared isIgnoredDir
// predicate). Used for watcher-delivered absolute paths as defense in depth.
func ignoredPathSegment(path string) bool {
	norm := filepath.ToSlash(path)
	for _, seg := range strings.Split(norm, "/") {
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

// shouldSkip filters files we never want to watch: temporary files, ignored directories,
// unsupported languages, and pathologies like very large generated bundles.
func (e *Engine) shouldSkip(path string) bool {
	return ignoredPathSegment(path) || !e.parseEligible(path)
}

// Run periodically prunes expired active runs until ctx is done.
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.pruneActiveRuns()
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
		lowerAgent := strings.ToLower(p.AgentName)
		switch {
		case strings.Contains(lowerAgent, "antigravity") || strings.Contains(lowerAgent, "gemini"):
			p.ModelName = "gemini-3.7-flash"
		case strings.Contains(lowerAgent, "claude"):
			p.ModelName = "claude-3-7-sonnet"
		case strings.Contains(lowerAgent, "aider"):
			p.ModelName = "gpt-4o"
		case strings.Contains(lowerAgent, "cline") || strings.Contains(lowerAgent, "roo"):
			p.ModelName = "claude-3-7-sonnet"
		case strings.Contains(lowerAgent, "cursor") || strings.Contains(lowerAgent, "windsurf") || strings.Contains(lowerAgent, "trae") || strings.Contains(lowerAgent, "wrongstack"):
			p.ModelName = "claude-3-7-sonnet"
		default:
			p.ModelName = "claude-3-7-sonnet"
		}
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
	if err := e.cfg.Store.UpsertRun(rec); err != nil {
		return fmt.Errorf("upsert run: %w", err)
	}
	e.runMu.Lock()
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

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read response body: %w", err)
	}

	return models.Global.ImportModelsDevJSON(data)
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
	h, err := e.cfg.Store.FileHealth(path)
	if err != nil {
		return IPCHealth{}, err
	}
	return ipcHealthFromDB(h), nil
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
		rec.RepoName = e.cfg.RepoName
		if proj, ok := e.FindProjectForFile(rec.FilePath); ok {
			rec.RepoName = proj.Name
		}
	}
	if rec.ModelName == "" || models.IsJunkModel(rec.ModelName) {
		rec.ModelName = "claude-3-7-sonnet"
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

	if err := e.cfg.Store.InsertReadEvent(rec); err != nil {
		return fmt.Errorf("insert read event: %w", err)
	}
	e.hub.Broadcast(WSEvent{Type: "file_read_event", Payload: rec, EventID: rec.ReadID})
	return nil
}

// GetFileReadStats returns aggregated read metrics for a given file.
func (e *Engine) GetFileReadStats(filePath string) (db.FileReadStats, error) {
	return e.cfg.Store.GetFileReadStats(filePath)
}

// GetRecentEvents returns the most recent code mutation and diff events.
func (e *Engine) GetRecentEvents(limit int, repoFilter ...string) ([]db.EventRecord, error) {
	var filter string
	if len(repoFilter) > 0 && repoFilter[0] != "" {
		filter = repoFilter[0]
	} else if active := e.GetActiveProject(); active != nil && active.Name != "" {
		filter = active.Name
	}
	return e.cfg.Store.RecentEvents(limit, filter)
}

// GetRecentFileReads returns the most recent read records across the system, optionally filtered by repo_name.
func (e *Engine) GetRecentFileReads(limit int, repoFilter ...string) ([]db.FileReadRecord, error) {
	var filter string
	if len(repoFilter) > 0 && repoFilter[0] != "" {
		filter = repoFilter[0]
	} else if active := e.GetActiveProject(); active != nil && active.Name != "" {
		filter = active.Name
	}
	return e.cfg.Store.GetRecentFileReads(limit, filter)
}

// GetFileReadHeatmap returns line range frequencies for a file.
func (e *Engine) GetFileReadHeatmap(filePath string) ([]db.LineReadHeatmap, error) {
	return e.cfg.Store.GetFileReadHeatmap(filePath)
}

// IndexStatus returns the current codebase indexing progress and stats.
func (e *Engine) IndexStatus() IndexProgress {
	e.indexMu.RLock()
	defer e.indexMu.RUnlock()
	return e.indexStatus
}

// recentRunID returns the most recently seen run_id within the correlation
// window. "Last-seen wins" is the right heuristic: watcher events arrive
// serially and the window is short.
func (e *Engine) recentRunID() string {
	e.runMu.Lock()
	defer e.runMu.Unlock()
	cutoff := time.Now().Add(-e.correlate)
	var bestID string
	var bestTS time.Time
	for id, meta := range e.activeRuns {
		if meta.LastSeen.Before(cutoff) {
			delete(e.activeRuns, id)
			continue
		}
		if meta.LastSeen.After(bestTS) {
			bestTS = meta.LastSeen
			bestID = id
		}
	}
	return bestID
}

// newID returns a random 16-byte hex identifier for DB primary keys.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}
