package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wrongstack/wrongtrace/internal/models"
)

// Handlers holds the dependencies the route handlers need.
type Handlers struct {
	Engine EngineAPI
	// SocketPath is the daemon's IPC endpoint as configured at startup;
	// empty when IPC is disabled. Surfaced by /api/health.
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
