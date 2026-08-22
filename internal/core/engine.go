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
	"log"
	"os"
	"sync"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
	"github.com/wrongstack/wrongtrace/internal/models"
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

	runMu     sync.Mutex
	activeRuns map[string]runMeta
	correlate  time.Duration
}

// runMeta is the metadata kept for an active (or recently-seen) agent run —
// just enough to back-fill run_id on subsequent AST events.
type runMeta struct {
	AgentName string
	ModelName string
	StartedAt time.Time
	LastSeen  time.Time
	TaskID    string
}

// NewEngine constructs an Engine. Pass a nil AST to skip file parsing (the MCP
// subcommand uses this since it never touches the filesystem).
func NewEngine(cfg Config) *Engine {
	if cfg.RepoName == "" {
		cfg.RepoName = "default"
	}
	return &Engine{
		cfg:        cfg,
		hub:        NewHub(),
		activeRuns: make(map[string]runMeta),
		correlate:  10 * time.Minute,
	}
}

// Hub exposes the WebSocket broadcaster. The server package reads from it.
func (e *Engine) Hub() *Hub { return e.hub }

// Repo returns the configured repository name (used by handlers).
func (e *Engine) Repo() string { return e.cfg.RepoName }

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
	res := ast.Diff(e.cfg.RepoName, prev, snap)
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
	prev, ok := e.cfg.AST.Snapshot(path)
	if !ok || prev == nil {
		return
	}
	e.cfg.AST.Forget(path)
	res := ast.Diff(e.cfg.RepoName, prev, nil)
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
		rec := db.EventRecord{
			EventID:      newID(),
			RunID:        ev.RunID,
			RepoName:     ev.RepoName,
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

// shouldSkip filters files we never want to watch: unsupported languages and
// pathologies like very large generated bundles.
func (e *Engine) shouldSkip(path string) bool {
	if ast.DetectLanguage(path) == ast.LangUnknown {
		return true
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 4*1024*1024 {
		return true
	}
	return false
}

// Run parks until ctx is done. Reserved for future background work.
func (e *Engine) Run(ctx context.Context) {
	<-ctx.Done()
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
	e.activeRuns[p.RunID] = runMeta{
		AgentName: p.AgentName,
		ModelName: p.ModelName,
		StartedAt: rec.CreatedAt,
		LastSeen:  rec.CreatedAt,
		TaskID:    p.TaskID,
	}
	e.runMu.Unlock()
	e.hub.Broadcast(WSEvent{Type: "run_reported", Payload: rec})
	return nil
}

// ModelCatalog returns all available AI models and their token pricing specs.
func (e *Engine) ModelCatalog() []models.ModelInfo {
	return models.Global.AllModels()
}

// UpsertModel updates or adds a custom model into the catalog.
func (e *Engine) UpsertModel(m models.ModelInfo) {
	models.Global.Upsert(m)
}

// CalculateCost computes total dollar spend from tokens for a specific model.
func (e *Engine) CalculateCost(model string, promptTokens, completionTokens int64) float64 {
	return models.Global.CalculateCost(model, promptTokens, completionTokens)
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
