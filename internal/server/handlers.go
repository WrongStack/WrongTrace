package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Handlers holds the dependencies the route handlers need.
type Handlers struct {
	Engine EngineAPI
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
// "did the HTTP listener come up?" plus live diagnostics.
func (h *Handlers) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "ok",
		"repo":       h.Engine.Repo(),
		"timestamp":  time.Now().UTC(),
		"ws_clients": h.Engine.Hub().ClientCount(),
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
