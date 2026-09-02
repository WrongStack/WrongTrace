package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
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

// writeError emits a structured {"error": "...", "message": "..."} body with the given status.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{
		"error":   msg,
		"message": msg,
	})
}

// decodeJSON reads and decodes JSON from r.Body bounded by a 10MB memory limit to protect against RAM exhaustion.
func decodeJSON(w http.ResponseWriter, r *http.Request, v interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024)
	return json.NewDecoder(r.Body).Decode(v)
}

// Health is a cheap readiness probe: no DB hit, no fsnotify check. It answers
// "did the HTTP listener come up?" plus live diagnostics, including the IPC
// endpoint agents should connect to (empty when IPC is disabled).
func (h *Handlers) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
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
// just the fragile-node list. It queries only the thrashing rows instead of
// assembling the full metrics snapshot.
func (h *Handlers) Thrashing(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Engine.ThrashingRows(h.getProjectFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []db.ThrashingRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// Models returns the per-model survival and ROI comparison. It queries only
// the model rows instead of assembling the full metrics snapshot.
func (h *Handlers) Models(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Engine.ModelRows(h.getProjectFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []db.ModelRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// ReportTelemetry accepts run and token telemetry from AI agents.
func (h *Handlers) ReportTelemetry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID            string  `json:"run_id"`
		TaskID           string  `json:"task_id"`
		ProjectID        string  `json:"project_id"`
		ProjectSlug      string  `json:"project_slug"`
		AgentName        string  `json:"agent_name"`
		ModelName        string  `json:"model_name"`
		Provider         string  `json:"provider"`
		PromptTokens     int64   `json:"prompt_tokens"`
		CompletionTokens int64   `json:"completion_tokens"`
		TokensUsed       int64   `json:"tokens_used"`
		CostUSD          float64 `json:"cost_usd"`
		Cost             float64 `json:"cost"`
		Intent           string  `json:"intent"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	runID := req.RunID
	if runID == "" {
		runID = "run-" + time.Now().UTC().Format("20060102150405") + "-" + fmt.Sprintf("%04x", time.Now().UnixNano()%0xffff)
	}
	promptTokens := req.PromptTokens
	if promptTokens == 0 && req.TokensUsed > 0 {
		promptTokens = req.TokensUsed
	}
	cost := req.CostUSD
	if cost == 0 && req.Cost > 0 {
		cost = req.Cost
	}

	report := ipc.TelemetryReport{
		RunID:            runID,
		TaskID:           req.TaskID,
		ProjectID:        req.ProjectID,
		ProjectSlug:      req.ProjectSlug,
		AgentName:        req.AgentName,
		ModelName:        req.ModelName,
		Provider:         req.Provider,
		PromptTokens:     promptTokens,
		CompletionTokens: req.CompletionTokens,
		CostUSD:          cost,
		Intent:           req.Intent,
	}

	if err := h.Engine.ReportRun(report); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":       true,
		"status":   "ok",
		"event_id": runID,
		"run_id":   runID,
	})
}

// parseSince parses an ISO8601, RFC3339, DateTime, or Unix timestamp.
func parseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	s = strings.TrimSpace(s)
	if epoch, err := strconv.ParseInt(s, 10, 64); err == nil && epoch > 0 {
		if epoch > 1e11 { // milliseconds
			return time.UnixMilli(epoch).UTC(), nil
		}
		return time.Unix(epoch, 0).UTC(), nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		time.DateTime,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid since timestamp: %s", s)
}

// maxRecentEventsLimit bounds the client-supplied limit: the recent-events
// cache is keyed by limit, so an unbounded value would let one request pin
// megabytes of records in memory and serialize them on every poll.
const maxRecentEventsLimit = 1000

// RecentEvents returns the most recent AST events for the live feed, optionally filtered by file_path, repo, or since.
func (h *Handlers) RecentEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	} else if strings.Contains(r.URL.Path, "/metrics/") {
		limit = 500
	}
	if limit > maxRecentEventsLimit {
		limit = maxRecentEventsLimit
	}

	var since time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := parseSince(s)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		since = t
	}

	filePath := r.URL.Query().Get("file_path")
	if filePath == "" {
		filePath = r.URL.Query().Get("path")
	}

	repoFilter := r.URL.Query().Get("repo")
	if repoFilter == "" && r.URL.Query().Get("project_id") != "" {
		repoFilter = h.getProjectFilter(r)
	}

	events, err := h.Engine.GetRecentEventsFiltered(limit, repoFilter, filePath, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []db.EventRecord{}
	}
	writeJSON(w, http.StatusOK, events)
}

// SymbolHistory returns the revision history and model lineage of an AST node.
func (h *Handlers) SymbolHistory(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("file_path")
	if filePath == "" {
		filePath = r.URL.Query().Get("path")
	}
	signature := r.URL.Query().Get("signature")
	if signature == "" {
		signature = r.URL.Query().Get("symbol")
	}
	if signature == "" {
		signature = r.URL.Query().Get("name")
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}
	history, err := h.Engine.GetSymbolHistory(filePath, signature, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if history == nil {
		history = []db.SymbolHistoryRecord{}
	}
	writeJSON(w, http.StatusOK, history)
}

// FileModelActivity returns per-model read vs write stats for a file or all monitored files.
func (h *Handlers) FileModelActivity(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("file_path")
	if filePath == "" {
		filePath = r.URL.Query().Get("path")
	}
	if filePath == "" {
		filePath = r.URL.Query().Get("file")
	}
	if filePath == "" {
		activity, err := h.Engine.GetAllFileModelActivity(50)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if activity == nil {
			activity = []db.ModelActivitySummary{}
		}
		writeJSON(w, http.StatusOK, activity)
		return
	}
	activity, err := h.Engine.GetFileModelActivity(filePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if activity == nil {
		activity = []db.ModelActivitySummary{}
	}
	writeJSON(w, http.StatusOK, activity)
}

// ModelFriction returns the inter-agent friction and cross-model code collision report.
func (h *Handlers) ModelFriction(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}
	report, err := h.Engine.GetModelFrictionReport(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if report == nil {
		report = &db.InterAgentFrictionReport{
			Edges:            []db.ModelFrictionEdge{},
			RecentCollisions: []db.CrossThrashEvent{},
		}
	}
	writeJSON(w, http.StatusOK, report)
}

// Atlas returns the repository Code Atlas graph (packages, files, symbols) with optional scoping, summary mode, and pagination.
func (h *Handlers) Atlas(w http.ResponseWriter, r *http.Request) {
	atlas, err := h.Engine.Atlas(h.getProjectFilter(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	wsFilter := r.URL.Query().Get("workspace")
	prefixFilter := r.URL.Query().Get("prefix")
	summaryMode := r.URL.Query().Get("summary") == "true" || r.URL.Query().Get("mode") == "summary"
	includeSymbols := true
	if incSym := r.URL.Query().Get("include_symbols"); incSym == "false" || incSym == "0" {
		includeSymbols = false
	} else if sym := r.URL.Query().Get("symbols"); sym == "false" || sym == "0" {
		includeSymbols = false
	}

	filteredPackages := make([]core.AtlasPackage, 0, len(atlas.Packages))
	for _, pkg := range atlas.Packages {
		if wsFilter != "" && !strings.EqualFold(pkg.Workspace, wsFilter) && !strings.Contains(strings.ToLower(pkg.Workspace), strings.ToLower(wsFilter)) {
			continue
		}
		if prefixFilter != "" {
			// Trim a trailing slash so the boundary check below cannot become
			// "prefix//" and silently match nothing.
			normPrefix := strings.TrimSuffix(strings.ToLower(filepath.ToSlash(prefixFilter)), "/")
			normPkg := strings.ToLower(filepath.ToSlash(pkg.Path))
			if !atlasPathWithin(normPkg, normPrefix) {
				var matchingFiles []core.AtlasFile
				for _, f := range pkg.Files {
					if atlasPathWithin(strings.ToLower(filepath.ToSlash(f.Path)), normPrefix) {
						matchingFiles = append(matchingFiles, f)
					}
				}
				if len(matchingFiles) == 0 {
					continue
				}
				pkg.Files = matchingFiles
			}
		}
		if summaryMode {
			pkg.Files = nil
		} else if !includeSymbols {
			for i := range pkg.Files {
				pkg.Files[i].Symbols = nil
			}
		}
		filteredPackages = append(filteredPackages, pkg)
	}

	atlas.TotalPackages = len(filteredPackages)
	atlas.Packages = filteredPackages

	// Pagination
	if l := r.URL.Query().Get("limit"); l != "" {
		if limit, err := strconv.Atoi(l); err == nil && limit > 0 {
			offset := 0
			if o := r.URL.Query().Get("offset"); o != "" {
				if off, err := strconv.Atoi(o); err == nil && off >= 0 {
					offset = off
				}
			}
			atlas.Limit = limit
			atlas.Offset = offset
			if offset >= len(atlas.Packages) {
				atlas.Packages = []core.AtlasPackage{}
			} else {
				end := offset + limit
				if end > len(atlas.Packages) {
					end = len(atlas.Packages)
				}
				atlas.Packages = atlas.Packages[offset:end]
			}
		}
	}

	writeJSON(w, http.StatusOK, atlas)
}

// atlasPathWithin reports whether candidate is prefix itself or lives under
// it at a path-separator boundary: "api" and "api/nested/c.go" are within
// "api", but the sibling "api-v2" is not. The filter previously used a bare
// strings.HasPrefix, so a sibling directory sharing a string prefix satisfied
// a ?prefix= query and bled into the filtered atlas — the same leak fixed
// inside core.Atlas with pathIsWithin (TestAtlas_SiblingPrefixProjectIsolation):
// a path prefix is a containment test only when the next character is a
// separator.
func atlasPathWithin(candidate, prefix string) bool {
	return candidate == prefix || strings.HasPrefix(candidate, prefix+"/")
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
	if err := decodeJSON(w, r, &req); err != nil {
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
	if err := decodeJSON(w, r, &req); err != nil {
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

// LockFile locks a file against agent modification with optional ownership and TTL.
func (h *Handlers) LockFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path       string `json:"path"`
		FilePath   string `json:"file_path"`
		Reason     string `json:"reason"`
		Owner      string `json:"owner"`
		AgentName  string `json:"agent_name"`
		ModelName  string `json:"model_name"`
		OwnerRunID string `json:"owner_run_id"`
		RunID      string `json:"run_id"`
		TTLSeconds int    `json:"ttl_seconds"`
		TTLMinutes int    `json:"ttl_minutes"`
		TTL        string `json:"ttl"`
		Force      bool   `json:"force"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	targetPath := req.Path
	if targetPath == "" {
		targetPath = req.FilePath
	}
	if targetPath == "" {
		writeError(w, http.StatusBadRequest, "path or file_path is required in body")
		return
	}
	owner := req.Owner
	if owner == "" {
		if req.AgentName != "" && req.ModelName != "" {
			owner = req.AgentName + " (" + req.ModelName + ")"
		} else if req.AgentName != "" {
			owner = req.AgentName
		} else if req.ModelName != "" {
			owner = req.ModelName
		}
	}
	ownerRunID := req.OwnerRunID
	if ownerRunID == "" {
		ownerRunID = req.RunID
	}

	if locked, existing := h.Engine.IsFileLocked(targetPath); locked {
		if existing.Owner != "" && existing.Owner != owner && !req.Force {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"ok":           false,
				"status":       "conflict",
				"error":        "file is already locked",
				"message":      fmt.Sprintf("file %s is already locked by %s", targetPath, existing.Owner),
				"path":         existing.Path,
				"reason":       existing.Reason,
				"owner":        existing.Owner,
				"owner_run_id": existing.OwnerRunID,
				"locked_at":    existing.LockedAt,
				"expires_at":   existing.ExpiresAt,
			})
			return
		}
	}

	ttl := 15 * time.Minute
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	} else if req.TTLMinutes > 0 {
		ttl = time.Duration(req.TTLMinutes) * time.Minute
	} else if req.TTL != "" {
		if d, err := time.ParseDuration(req.TTL); err == nil && d > 0 {
			ttl = d
		}
	}

	info := h.Engine.LockFileWithOptions(targetPath, req.Reason, owner, ownerRunID, ttl)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"status":       "locked",
		"path":         info.Path,
		"reason":       info.Reason,
		"owner":        info.Owner,
		"owner_run_id": info.OwnerRunID,
		"locked_at":    info.LockedAt,
		"expires_at":   info.ExpiresAt,
	})
}

// ListLocks returns all active guardrail locks.
func (h *Handlers) ListLocks(w http.ResponseWriter, _ *http.Request) {
	locks := h.Engine.ListLocks()
	if locks == nil {
		locks = []core.LockInfo{}
	}
	writeJSON(w, http.StatusOK, locks)
}

// UnlockFile removes a lock on a file.
func (h *Handlers) UnlockFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	targetPath := req.Path
	if targetPath == "" {
		targetPath = req.FilePath
	}
	if targetPath == "" {
		writeError(w, http.StatusBadRequest, "path or file_path is required in body")
		return
	}
	h.Engine.UnlockFile(targetPath)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"status": "unlocked",
		"path":   targetPath,
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
	if err := decodeJSON(w, r, &route); err != nil {
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
func (h *Handlers) ListProxyTraffic(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeJSON(w, http.StatusOK, []proxy.ProxyTrafficRecord{})
		return
	}
	limit := proxyTrafficLimit(r)
	if r.URL.Query().Get("detail") == "false" {
		writeJSON(w, http.StatusOK, h.Proxy.TrafficSummaries(limit))
		return
	}
	writeJSON(w, http.StatusOK, h.Proxy.AllTraffic(limit))
}

// GetProxyTraffic returns the full wire payload for one selected traffic row.
func (h *Handlers) GetProxyTraffic(w http.ResponseWriter, r *http.Request) {
	if h.Proxy == nil {
		writeError(w, http.StatusNotFound, "proxy traffic record not found")
		return
	}
	record, ok := h.Proxy.Traffic(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "proxy traffic record not found")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func proxyTrafficLimit(r *http.Request) int {
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = min(parsed, 100)
		}
	}
	return limit
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
	if err := decodeJSON(w, r, &req); err != nil || req.Path == "" {
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
		if err := decodeJSON(w, r, &req); err != nil && err != io.EOF {
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
	if err := decodeJSON(w, r, &p); err != nil {
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
	if err := decodeJSON(w, r, &s); err != nil {
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
			if err := decodeJSON(w, r, &body); err == nil && body.Days != nil {
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
	if err := decodeJSON(w, r, &payload); err != nil {
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
	data, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024))
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
		"status":         "ok",
		"spans_ingested": count,
		"message":        fmt.Sprintf("%d OTLP spans ingested into WrongTrace", count),
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

// GetIPCTraffic returns recent recorded IPC interactions from connected AI agents.
func (h *Handlers) GetIPCTraffic(w http.ResponseWriter, r *http.Request) {
	traffic := h.Engine.GetIPCTraffic()
	if r.URL.Query().Get("detail") == "false" {
		traffic = h.Engine.GetIPCTrafficSummaries()
	}
	if traffic == nil {
		traffic = []ipc.IPCTrafficRecord{}
	}
	writeJSON(w, http.StatusOK, traffic)
}

// GetIPCTrafficRecord returns one bounded request/response pair on demand.
func (h *Handlers) GetIPCTrafficRecord(w http.ResponseWriter, r *http.Request) {
	record, ok := h.Engine.GetIPCTrafficRecord(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "IPC traffic record not found")
		return
	}
	writeJSON(w, http.StatusOK, record)
}
