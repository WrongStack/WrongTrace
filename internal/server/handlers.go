package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"io"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/models"
	"github.com/wrongstack/wrongtrace/internal/profiler"
	"github.com/wrongstack/wrongtrace/internal/proxy"
)

// Handlers holds the dependencies the route handlers need.
type Handlers struct {
	Engine     EngineAPI
	Profiler   *profiler.Collector
	Proxy      *proxy.GatewayProxy
	SocketPath string
}

// writeJSON serializes v and writes it with the appropriate content type.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emits a structured {"error": "..."} body with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// Health is a cheap readiness probe: no DB hit, no fsnotify check. It answers
// "did the HTTP listener come up?" plus live diagnostics, including the IPC
// endpoint agents should connect to (empty when IPC is disabled).
func (h *Handlers) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"repo":        h.Engine.Repo(),
		"timestamp":   time.Now().UTC(),
		"ws_clients":  h.Engine.Hub().ClientCount(),
		"socket_path": h.SocketPath,
	})
}

func (h *Handlers) getProjectFilter(r *http.Request) string {
	if repo := r.URL.Query().Get("repo"); repo != "" {
		return repo
	}
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		if p, err := h.Engine.GetProject(pid); err == nil && p.Name != "" {
			return p.Name
		}
	}
	return ""
}

// Overview returns the full MetricsSnapshot — the dashboard hits this on
// first render to populate every widget in one round trip.
func (h *Handlers) Overview(w http.ResponseWriter, r *http.Request) {
	snap, err := h.Engine.Metrics(h.getProjectFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// Thrashing returns the thrashing panel standalone for tooling that wants
// just the fragile-node list.
func (h *Handlers) Thrashing(w http.ResponseWriter, r *http.Request) {
	snap, err := h.Engine.Metrics(h.getProjectFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap.Thrashing)
}

// Models returns the per-model survival and ROI comparison.
func (h *Handlers) Models(w http.ResponseWriter, r *http.Request) {
	snap, err := h.Engine.Metrics(h.getProjectFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap.Models)
}

// RecentEvents returns the most recent AST events for the live feed, optionally filtered by file_path or repo.
func (h *Handlers) RecentEvents(w http.ResponseWriter, r *http.Request) {
	if filePath := r.URL.Query().Get("file_path"); filePath != "" {
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			if val, err := strconv.Atoi(l); err == nil && val > 0 {
				limit = val
			}
		}
		events, err := h.Engine.GetRecentFileEvents(filePath, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if events == nil {
			events = []db.EventRecord{}
		}
		writeJSON(w, http.StatusOK, events)
		return
	}

	snap, err := h.Engine.Metrics(h.getProjectFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap.RecentEvents)
}

// Atlas returns the full repository Code Atlas graph (packages, files, symbols).
func (h *Handlers) Atlas(w http.ResponseWriter, r *http.Request) {
	atlas, err := h.Engine.Atlas(h.getProjectFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, atlas)
}

// AtlasStatus returns the percentage and live progress of codebase indexing.
func (h *Handlers) AtlasStatus(w http.ResponseWriter, _ *http.Request) {
	status := h.Engine.IndexStatus()
	writeJSON(w, http.StatusOK, status)
}

// FileHealth is the agent-facing guardrail endpoint (mirrors the MCP tool).
func (h *Handlers) FileHealth(w http.ResponseWriter, r *http.Request) {
	path := chi.URLParam(r, "path")
	if path == "" {
		path = r.URL.Query().Get("path")
	}
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	hl, err := h.Engine.FileHealth(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hl)
}

// ModelCatalog returns all available LLM models, specs, and token pricing.
func (h *Handlers) ModelCatalog(w http.ResponseWriter, _ *http.Request) {
	catalog := h.Engine.ModelCatalog()
	writeJSON(w, http.StatusOK, catalog)
}

// ProviderCatalog returns all available AI providers and their hosted models.
func (h *Handlers) ProviderCatalog(w http.ResponseWriter, _ *http.Request) {
	providers := h.Engine.ProviderCatalog()
	writeJSON(w, http.StatusOK, providers)
}

// UpsertModel allows adding or overriding a custom model spec in the catalog.
func (h *Handlers) UpsertModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID                 string  `json:"id"`
		Name               string  `json:"name"`
		Provider           string  `json:"provider"`
		InputPricePerM     float64 `json:"input_price_per_m"`
		OutputPricePerM    float64 `json:"output_price_per_m"`
		CacheReadPricePerM float64 `json:"cache_read_price_per_m"`
		ContextWindow      int     `json:"context_window"`
		Description        string  `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.Name == "" {
		req.Name = req.ID
	}
	if req.Provider == "" {
		req.Provider = "Custom"
	}

	modelInfo := req
	_ = modelInfo
	// Call engine
	h.Engine.UpsertModel(reqToModelInfo(req.ID, req.Name, req.Provider, req.Description, req.InputPricePerM, req.OutputPricePerM, req.CacheReadPricePerM, req.ContextWindow))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"message": "model spec updated",
		"model":   req.ID,
	})
}

// CalculateCost computes total dollar spend given model and token counts.
func (h *Handlers) CalculateCost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model            string `json:"model"`
		PromptTokens     int64  `json:"prompt_tokens"`
		CompletionTokens int64  `json:"completion_tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	cost := h.Engine.CalculateCost(req.Model, req.PromptTokens, req.CompletionTokens)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"model":             req.Model,
		"prompt_tokens":     req.PromptTokens,
		"completion_tokens": req.CompletionTokens,
		"total_cost_usd":    cost,
	})
}

func reqToModelInfo(id, name, provider, desc string, inPrice, outPrice, cachePrice float64, ctxWin int) models.ModelInfo {
	return models.ModelInfo{
		ID:                 id,
		Name:               name,
		Provider:           provider,
		Description:        desc,
		InputPricePerM:     inPrice,
		OutputPricePerM:    outPrice,
		CacheReadPricePerM: cachePrice,
		ContextWindow:      ctxWin,
		IsCustom:           true,
	}
}

// CheckGuardrail assesses file safety before an AI agent modifies it.
func (h *Handlers) CheckGuardrail(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	res, err := h.Engine.CheckGuardrail(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// LockFile locks a file against agent modification.
func (h *Handlers) LockFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required in body")
		return
	}
	h.Engine.LockFile(req.Path, req.Reason)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "locked",
		"path":   req.Path,
		"reason": req.Reason,
	})
}

// UnlockFile removes a lock on a file.
func (h *Handlers) UnlockFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required in body")
		return
	}
	h.Engine.UnlockFile(req.Path)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "unlocked",
		"path":   req.Path,
	})
}

// ListProxyRoutes returns all configured dynamic gateway routes.
func (h *Handlers) ListProxyRoutes(w http.ResponseWriter, _ *http.Request) {
	if h.Proxy == nil || h.Proxy.Routes == nil {
		writeJSON(w, http.StatusOK, []proxy.ProxyRoute{})
		return
	}
	writeJSON(w, http.StatusOK, h.Proxy.Routes.AllRoutes())
}

// UpsertProxyRoute creates or updates a dynamic gateway route.
func (h *Handlers) UpsertProxyRoute(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil || h.Proxy.Routes == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy service not initialized")
		return
	}
	var route proxy.ProxyRoute
	if err := json.NewDecoder(r.Body).Decode(&route); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if route.Name == "" || route.PathPrefix == "" || route.TargetUpstream == "" {
		writeError(w, http.StatusBadRequest, "name, path_prefix, and target_upstream are required")
		return
	}
	saved := h.Proxy.Routes.UpsertRoute(route)
	writeJSON(w, http.StatusOK, saved)
}

// DeleteProxyRoute removes a dynamic gateway route.
func (h *Handlers) DeleteProxyRoute(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil || h.Proxy.Routes == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy service not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "route id is required")
		return
	}
	deleted := h.Proxy.Routes.DeleteRoute(id)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted,
		"id":      id,
	})
}

// ListProxyTraffic returns captured raw proxy traffic logs.
func (h *Handlers) ListProxyTraffic(w http.ResponseWriter, _ *http.Request) {
	if h.Proxy == nil {
		writeJSON(w, http.StatusOK, []proxy.ProxyTrafficRecord{})
		return
	}
	writeJSON(w, http.StatusOK, h.Proxy.AllTraffic(100))
}

// ClearProxyTraffic clears captured proxy traffic logs.
func (h *Handlers) ClearProxyTraffic(w http.ResponseWriter, _ *http.Request) {
	if h.Proxy != nil {
		h.Proxy.ClearTraffic()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

// ListProjects returns all registered project profiles.
func (h *Handlers) ListProjects(w http.ResponseWriter, _ *http.Request) {
	projects := h.Engine.ListProjects()
	writeJSON(w, http.StatusOK, projects)
}

// AddProject registers a workspace directory to observe.
func (h *Handlers) AddProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	p, err := h.Engine.AddProject(req.Name, req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// PreviewFromWrongStack lists every workspace in
// ~/.wrongstack/projects.json with what importing it would do, so the
// dashboard can render a choose-what-to-import view before committing.
// Same error mapping as the import: 404 when the source file is missing,
// 422 when it is malformed.
func (h *Handlers) PreviewFromWrongStack(w http.ResponseWriter, _ *http.Request) {
	res, err := h.Engine.PreviewFromWrongStack()
	if err != nil {
		if errors.Is(err, core.ErrWrongStackSourceMissing) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ImportFromWrongStack bulk-registers workspaces listed in
// ~/.wrongstack/projects.json that WrongTrace does not already monitor.
// The body is optional: {"roots":["D:\\path", ...]} imports only the listed
// roots (case-insensitive match against the registry entries); an absent or
// empty body imports everything available, the original one-click behavior.
// The source file's absence is a 404 (nothing to import from), a malformed
// file is a 422; per-entry problems (missing root, add failure) are reported
// in the result body rather than failing the batch.
func (h *Handlers) ImportFromWrongStack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Roots []string `json:"roots"`
	}
	if r.Body != nil {
		// A body that decodes to zero roots (absent, null, or {}) means
		// "import all"; only a present-but-unparseable body is rejected.
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}
	res, err := h.Engine.ImportFromWrongStack(req.Roots)
	if err != nil {
		if errors.Is(err, core.ErrWrongStackSourceMissing) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// UpdateProject updates metadata or log paths of a project. The project id
// comes from the URL ({id}); a body id is optional and must agree with it.
// The dashboard's edit form sends only name/description/*_logs_path, so
// requiring a body id (as this handler once did) broke every edit.
func (h *Handlers) UpdateProject(w http.ResponseWriter, r *http.Request) {
	var p core.ProjectProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		id = p.ID
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if p.ID != "" && p.ID != id {
		writeError(w, http.StatusConflict, fmt.Sprintf("body id %q does not match URL id %q", p.ID, id))
		return
	}
	p.ID = id
	updated, err := h.Engine.UpdateProject(p)
	if err != nil {
		if errors.Is(err, core.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// GetProject returns a single project profile by ID.
func (h *Handlers) GetProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	p, err := h.Engine.GetProject(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// SwitchActiveProject marks a project as the primary active workspace.
func (h *Handlers) SwitchActiveProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	p, err := h.Engine.SwitchActiveProject(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ActivateProject is an alias for SwitchActiveProject.
func (h *Handlers) ActivateProject(w http.ResponseWriter, r *http.Request) {
	h.SwitchActiveProject(w, r)
}

// RescanProject triggers session rediscovery for a specific project.
func (h *Handlers) RescanProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	p, err := h.Engine.RescanProject(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// RescanAllProjects triggers session rediscovery across all projects.
func (h *Handlers) RescanAllProjects(w http.ResponseWriter, _ *http.Request) {
	projs := h.Engine.RescanAllProjects()
	writeJSON(w, http.StatusOK, projs)
}

// RemoveProject stops monitoring a workspace.
func (h *Handlers) RemoveProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	if err := h.Engine.RemoveProject(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "removed", "id": id})
}

// GetSettings returns current application settings.
func (h *Handlers) GetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.Engine.GetSettings())
}

// UpdateSettings updates application settings.
func (h *Handlers) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var s core.AppSettings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	updated := h.Engine.UpdateSettings(s)
	writeJSON(w, http.StatusOK, updated)
}

// SyncModels syncs model definitions and live pricing from models.dev/api.json.
func (h *Handlers) SyncModels(w http.ResponseWriter, _ *http.Request) {
	count, err := h.Engine.SyncModelsDev()
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to sync models from models.dev: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"synced":  count,
		"message": "Model catalog synchronized successfully from models.dev",
	})
}

// VacuumDB optimizes and defragments the SQLite database.
func (h *Handlers) VacuumDB(w http.ResponseWriter, _ *http.Request) {
	if err := h.Engine.VacuumDB(); err != nil {
		writeError(w, http.StatusInternalServerError, "vacuum failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "database vacuum completed"})
}

// ClearStale removes old telemetry events older than N days. The days value
// may arrive as a JSON body {"days":30} (what the dashboard sends) or the
// ?days= query param (documented REST form); omitted means 30. Invalid
// values are rejected with 400 rather than silently rewritten — a caller
// asking to prune "abc" days needs to know, not get 30.
func (h *Handlers) ClearStale(w http.ResponseWriter, r *http.Request) {
	days := 30
	daysStr := r.URL.Query().Get("days")
	if daysStr == "" {
		var body struct {
			Days *int `json:"days"`
		}
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.Days != nil {
				days = *body.Days
			}
		}
	} else {
		d, err := strconv.Atoi(daysStr)
		if err != nil || d <= 0 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid days value %q: must be a positive integer", daysStr))
			return
		}
		days = d
	}
	if days <= 0 {
		writeError(w, http.StatusBadRequest, "days must be a positive integer")
		return
	}
	deleted, err := h.Engine.ClearStale(days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clear stale failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"deleted": deleted,
		"days":    days,
	})
}

// IngestProfiler accepts structured runtime, benchmark, and profiler test reports.
func (h *Handlers) IngestProfiler(w http.ResponseWriter, r *http.Request) {
	if h.Profiler == nil {
		writeError(w, http.StatusServiceUnavailable, "profiler collector not available")
		return
	}
	var payload profiler.ProfilerReportPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	ev, err := h.Profiler.IngestReport(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, ev)
}

// IngestOTLPTraces parses OpenTelemetry traces (OTLP JSON) over standard HTTP.
func (h *Handlers) IngestOTLPTraces(w http.ResponseWriter, r *http.Request) {
	if h.Profiler == nil {
		writeError(w, http.StatusServiceUnavailable, "profiler collector not available")
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read request body: "+err.Error())
		return
	}
	count, err := h.Profiler.IngestOTLP(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "parse otlp traces: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "ok",
		"spans_ingested": count,
		"message":       fmt.Sprintf("%d OTLP spans ingested into WrongTrace", count),
	})
}

// GetProfilerTraces returns recent runtime traces.
func (h *Handlers) GetProfilerTraces(w http.ResponseWriter, r *http.Request) {
	if h.Profiler == nil {
		writeJSON(w, http.StatusOK, []profiler.TraceEvent{})
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}
	traces, err := h.Profiler.Recent(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, traces)
}

// GetProfilerHotspots returns hotspot functions ranked by execution time and errors.
func (h *Handlers) GetProfilerHotspots(w http.ResponseWriter, r *http.Request) {
	if h.Profiler == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	limit := 25
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}
	hotspots, err := h.Profiler.Hotspots(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hotspots)
}

// GetProfilerOverview returns summary runtime telemetry metrics.
func (h *Handlers) GetProfilerOverview(w http.ResponseWriter, _ *http.Request) {
	if h.Profiler == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{})
		return
	}
	ov, err := h.Profiler.Overview()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

// GetRecentReads returns recent file reading events.
func (h *Handlers) GetRecentReads(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	reads, err := h.Engine.GetRecentFileReads(limit, h.getProjectFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if reads == nil {
		reads = []db.FileReadRecord{}
	}
	writeJSON(w, http.StatusOK, reads)
}

// GetFileReadStats returns aggregated read metrics, model breakdown, and recent reads for a single file.
func (h *Handlers) GetFileReadStats(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	stats, err := h.Engine.GetFileReadStats(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// GetFileReadHeatmap returns line-range read frequencies for hot code region visualization.
func (h *Handlers) GetFileReadHeatmap(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	heatmap, err := h.Engine.GetFileReadHeatmap(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if heatmap == nil {
		heatmap = []db.LineReadHeatmap{}
	}
	writeJSON(w, http.StatusOK, heatmap)
}



