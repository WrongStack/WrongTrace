package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/models"
	"github.com/wrongstack/wrongtrace/internal/proxy"
)

// Handlers holds the dependencies the route handlers need.
type Handlers struct {
	Engine     EngineAPI
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

// Overview returns the full MetricsSnapshot — the dashboard hits this on
// first render to populate every widget in one round trip.
func (h *Handlers) Overview(w http.ResponseWriter, _ *http.Request) {
	snap, err := h.Engine.Metrics()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// Thrashing returns the thrashing panel standalone for tooling that wants
// just the fragile-node list.
func (h *Handlers) Thrashing(w http.ResponseWriter, _ *http.Request) {
	snap, err := h.Engine.Metrics()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap.Thrashing)
}

// Models returns the per-model survival and ROI comparison.
func (h *Handlers) Models(w http.ResponseWriter, _ *http.Request) {
	snap, err := h.Engine.Metrics()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap.Models)
}

// RecentEvents returns the most recent AST events for the live feed.
func (h *Handlers) RecentEvents(w http.ResponseWriter, _ *http.Request) {
	snap, err := h.Engine.Metrics()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap.RecentEvents)
}

// Atlas returns the full repository Code Atlas graph (packages, files, symbols).
func (h *Handlers) Atlas(w http.ResponseWriter, _ *http.Request) {
	atlas, err := h.Engine.Atlas()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, atlas)
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

// UpdateProject updates metadata or log paths of a project.
func (h *Handlers) UpdateProject(w http.ResponseWriter, r *http.Request) {
	var p core.ProjectProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.ID == "" {
		writeError(w, http.StatusBadRequest, "project id is required")
		return
	}
	updated, err := h.Engine.UpdateProject(p)
	if err != nil {
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

// ClearStale removes old telemetry events older than N days.
func (h *Handlers) ClearStale(w http.ResponseWriter, r *http.Request) {
	daysStr := r.URL.Query().Get("days")
	days := 30
	if daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 {
			days = d
		}
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

