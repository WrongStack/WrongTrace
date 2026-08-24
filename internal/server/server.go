// Package server exposes the embedded React dashboard and JSON API. It binds
// chi routes for /api/*, upgrades /api/ws to WebSocket via gorilla, and serves
// web/dist/* with SPA fallback so client-side routes work on refresh.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ingest"
	"github.com/wrongstack/wrongtrace/internal/ipc"
	"github.com/wrongstack/wrongtrace/internal/models"
	"github.com/wrongstack/wrongtrace/internal/profiler"
	"github.com/wrongstack/wrongtrace/internal/proxy"
)

// EngineAPI is the slice of *core.Engine the HTTP layer needs. Declared as an
// interface so handlers can be exercised with a fake in tests.
type EngineAPI interface {
	Metrics(repoFilter ...string) (core.MetricsSnapshot, error)
	Atlas(repoFilter ...string) (core.AtlasSnapshot, error)
	FileHealth(path string) (core.IPCHealth, error)
	CheckGuardrail(path string) (core.GuardrailResult, error)
	LockFile(path, reason string) core.LockInfo
	LockFileWithOptions(path, reason, owner, ownerRunID string, ttl time.Duration) core.LockInfo
	UnlockFile(path string)
	IsFileLocked(path string) (bool, core.LockInfo)
	ListLocks() []core.LockInfo
	ReportRun(p ipc.TelemetryReport) error
	ModelCatalog() []models.ModelInfo
	ProviderCatalog() []models.ProviderInfo
	UpsertModel(m models.ModelInfo)
	CalculateCost(model string, promptTokens, completionTokens int64) float64
	SyncModelsDev() (int, error)
	ListProjects() []core.Project
	GetProject(id string) (core.ProjectProfile, error)
	AddProject(name, path string) (core.Project, error)
	PreviewFromWrongStack() (core.PreviewFromWrongStackResult, error)
	ImportFromWrongStack(roots []string) (core.ImportFromWrongStackResult, error)
	UpdateProject(p core.ProjectProfile) (core.ProjectProfile, error)
	SwitchActiveProject(id string) (*core.ProjectProfile, error)
	RescanProject(id string) (*core.ProjectProfile, error)
	RescanAllProjects() []core.ProjectProfile
	RemoveProject(id string) error
	GetSettings() core.AppSettings
	UpdateSettings(s core.AppSettings) core.AppSettings
	VacuumDB() error
	ClearStale(days int) (int64, error)
	GetRecentEvents(limit int, repoFilter ...string) ([]db.EventRecord, error)
	GetRecentEventsFiltered(limit int, repo string, filePath string, since time.Time) ([]db.EventRecord, error)
	GetSymbolHistory(filePath, signature string, limit int) ([]db.SymbolHistoryRecord, error)
	GetFileModelActivity(filePath string) ([]db.ModelActivitySummary, error)
	GetAllFileModelActivity(limit int) ([]db.ModelActivitySummary, error)
	GetModelFrictionReport(limit int) (*db.InterAgentFrictionReport, error)
	GetFileReadStats(filePath string) (db.FileReadStats, error)
	GetRecentFileReads(limit int, repoFilter ...string) ([]db.FileReadRecord, error)
	GetRecentFileEvents(filePath string, limit int) ([]db.EventRecord, error)
	GetFileReadHeatmap(filePath string) ([]db.LineReadHeatmap, error)
	GetIPCTraffic() []ipc.IPCTrafficRecord
	IndexStatus() core.IndexProgress
	Hub() *core.Hub
	Store() *db.Store
	Repo() string
}

// Config configures a Server.
type Config struct {
	Port   int
	Engine *core.Engine
	// SocketPath is the IPC endpoint (UDS / named pipe) the daemon bound, if
	// any. Reported via /api/health so the dashboard can show agents the real
	// connect path instead of guessing platform defaults.
	SocketPath string
}

// Server bundles the HTTP listener and chi router.
type Server struct {
	cfg    Config
	router chi.Router
	hs     *http.Server
	hsMu   sync.Mutex
}

// New constructs a Server with all routes wired.
func New(cfg Config) *Server {
	if cfg.Engine == nil {
		panic("server: engine is required")
	}
	s := &Server{cfg: cfg}
	s.router = s.buildRouter()
	return s
}

// Start begins listening. It returns when the listener fails or ctx-style
// shutdown is initiated via Shutdown.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.cfg.Port)
	hs := &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.setHS(hs)
	log.Printf("http: listening on http://localhost%s", addr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully drains in-flight requests. Safe to call before Start
// (returns nil) or concurrently with it.
func (s *Server) Shutdown(ctx context.Context) error {
	hs := s.currentHS()
	if hs == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return hs.Shutdown(ctx)
}

// setHS/currentHS guard the *http.Server handoff between Start (writer) and
// Shutdown (reader). The unsynchronized field access was a genuine data race
// surfaced by -race on the CI runner (Start writing s.hs at the same moment
// Shutdown read it).
func (s *Server) setHS(hs *http.Server) {
	s.hsMu.Lock()
	s.hs = hs
	s.hsMu.Unlock()
}

func (s *Server) currentHS() *http.Server {
	s.hsMu.Lock()
	defer s.hsMu.Unlock()
	return s.hs
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	var store *db.Store
	if s.cfg.Engine != nil {
		store = s.cfg.Engine.Store()
	}
	var hub *core.Hub
	if s.cfg.Engine != nil {
		hub = s.cfg.Engine.Hub()
	}

	h := &Handlers{
		Engine:     s.cfg.Engine,
		SocketPath: s.cfg.SocketPath,
		Profiler: profiler.NewCollector(profiler.Config{
			Store: store,
			GetStore: func() *db.Store {
				if s.cfg.Engine != nil {
					return s.cfg.Engine.Store()
				}
				return store
			},
			OnTrace: func(ev profiler.TraceEvent) {
				if hub != nil {
					hub.Broadcast(core.WSEvent{
						Type:    "profiler_trace",
						Payload: ev,
					})
				}
			},
		}),
		Proxy: proxy.NewGatewayProxy(proxy.Config{
			Reporter: s.cfg.Engine,
			OnTraffic: func(rec proxy.ProxyTrafficRecord) {
				if s.cfg.Engine != nil {
					for _, tc := range rec.ToolCalls {
						if tc.TargetFile != "" && ingest.IsFileReadingTool(tc.Name) {
							var args map[string]interface{}
							_ = json.Unmarshal([]byte(tc.Arguments), &args)
							sLine, eLine, lCount := ingest.ExtractLineRange(args)
							_ = s.cfg.Engine.RecordReadEvent(db.FileReadRecord{
								ReadID:         fmt.Sprintf("gw-read-%s-%s", rec.ID, tc.ID),
								SessionID:      rec.RunID,
								RunID:          rec.RunID,
								RepoName:       rec.ProjectSlug,
								FilePath:       tc.TargetFile,
								AgentName:      rec.AgentName,
								ModelName:      rec.Model,
								Provider:       rec.Provider,
								ToolName:       tc.Name,
								StartLine:      sLine,
								EndLine:        eLine,
								LinesReadCount: lCount,
								PromptTokens:   rec.PromptTokens,
								CachedTokens:   rec.CachedTokens,
								CostUSD:        rec.CostUSD,
								Intent:         rec.AssistantReply,
								ReadTime:       rec.Timestamp,
							})
						}
					}
					if s.cfg.Engine.Hub() != nil {
						s.cfg.Engine.Hub().Broadcast(core.WSEvent{
							Type:    "proxy_traffic",
							Payload: rec,
						})
					}
				}
			},
		}),
	}
	r.Route("/api", func(r chi.Router) {
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "route not found: "+r.Method+" "+r.URL.Path)
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed: "+r.Method+" "+r.URL.Path)
		})

		r.Get("/health", h.Health)
		r.Post("/telemetry", h.ReportTelemetry)
		r.Post("/telemetry/report", h.ReportTelemetry)
		r.Get("/metrics/overview", h.Overview)
		r.Get("/metrics/thrashing", h.Thrashing)
		r.Get("/metrics/models", h.Models)
		r.Get("/metrics/recent", h.RecentEvents)
		r.Get("/events/recent", h.RecentEvents)
		r.Get("/events", h.RecentEvents)
		r.Get("/metrics/friction", h.ModelFriction)
		r.Get("/metrics/cross-thrash", h.ModelFriction)
		r.Get("/cross-thrash", h.ModelFriction)
		r.Get("/friction", h.ModelFriction)
		r.Get("/atlas", h.Atlas)
		r.Get("/atlas/status", h.AtlasStatus)
		r.Get("/file/health", h.FileHealth)
		r.Get("/guardrail/check", h.CheckGuardrail)
		r.Get("/guardrail/locks", h.ListLocks)
		r.Get("/guardrails/locks", h.ListLocks)
		r.Post("/guardrail/lock", h.LockFile)
		r.Post("/guardrail/unlock", h.UnlockFile)

		// File Read Tracing & Context Hotspots
		r.Get("/reads/recent", h.GetRecentReads)
		r.Get("/files/reads", h.GetFileReadStats)
		r.Get("/file/reads", h.GetFileReadStats)
		r.Get("/files/heatmap", h.GetFileReadHeatmap)
		r.Get("/file/heatmap", h.GetFileReadHeatmap)
		r.Get("/files/activity", h.FileModelActivity)
		r.Get("/file/activity", h.FileModelActivity)
		r.Get("/ipc/traffic", h.GetIPCTraffic)

		// AST Symbol History & Evolution Lineage
		r.Get("/symbol/history", h.SymbolHistory)
		r.Get("/symbols/history", h.SymbolHistory)
		r.Get("/node/history", h.SymbolHistory)
		r.Get("/nodes/history", h.SymbolHistory)

		r.Get("/proxy/routes", h.ListProxyRoutes)
		r.Post("/proxy/routes", h.UpsertProxyRoute)
		r.Delete("/proxy/routes/{id}", h.DeleteProxyRoute)
		r.Get("/proxy/traffic", h.ListProxyTraffic)
		r.Delete("/proxy/traffic", h.ClearProxyTraffic)

		r.Get("/projects", h.ListProjects)
		r.Post("/projects", h.AddProject)
		r.Get("/projects/import/wrongstack", h.PreviewFromWrongStack)
		r.Post("/projects/import/wrongstack", h.ImportFromWrongStack)
		r.Post("/projects/rescan", h.RescanAllProjects)
		r.Get("/projects/{id}", h.GetProject)
		r.Put("/projects/{id}", h.UpdateProject)
		r.Post("/projects/{id}/activate", h.SwitchActiveProject)
		r.Post("/projects/{id}/rescan", h.RescanProject)
		r.Delete("/projects/{id}", h.RemoveProject)

		r.Get("/settings", h.GetSettings)
		r.Post("/settings", h.UpdateSettings)
		r.Post("/settings/vacuum", h.VacuumDB)
		r.Post("/settings/clear-stale", h.ClearStale)

		r.Get("/models/catalog", h.ModelCatalog)
		r.Get("/models/providers", h.ProviderCatalog)
		r.Post("/models/catalog", h.UpsertModel)
		r.Post("/models/sync", h.SyncModels)
		r.Post("/models/calculate-cost", h.CalculateCost)

		// Universal Runtime & Profiler Telemetry endpoints
		r.Post("/profiler/ingest", h.IngestProfiler)
		r.Post("/profiler/otlp/v1/traces", h.IngestOTLPTraces)
		r.Get("/profiler/traces", h.GetProfilerTraces)
		r.Get("/profiler/hotspots", h.GetProfilerHotspots)
		r.Get("/profiler/overview", h.GetProfilerOverview)

		r.Get("/ws", h.WebSocket)
	})

	// Standard OpenTelemetry OTLP endpoint support at root /v1/traces
	r.Post("/v1/traces", h.IngestOTLPTraces)

	// Transparent AI Gateway routes for LLM proxies and OpenAI / Anthropic / Gemini endpoints.
	r.Handle("/proxy/*", h.Proxy)
	r.Handle("/proxy", h.Proxy)
	r.Handle("/v1/*", h.Proxy)
	r.Handle("/v1", h.Proxy)

	// Static React assets with SPA fallback. WebDistFS lives in the root
	// package's embed.go and returns the web/dist sub-filesystem.
	if distFS, err := WebDistFS(); err == nil {
		fileServer := http.FileServer(http.FS(distFS))
		r.Handle("/*", spaHandler(distFS, fileServer))
	} else {
		log.Printf("http: web/dist not embedded: %v (dashboard disabled)", err)
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("WrongTrace daemon running; web/dist not embedded.\n"))
		})
	}

	return r
}

// spaHandler serves a file if it exists, otherwise falls back to index.html so
// React Router URLs do not 404 on hard refresh.
func spaHandler(distFS fs.FS, fileServer http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "" || path == "/" {
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		probe := path[1:]
		if _, err := fs.Stat(distFS, probe); err != nil {
			ext := strings.ToLower(filepath.Ext(probe))
			if ext != "" && ext != ".html" {
				http.NotFound(w, r)
				return
			}
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	}
}

// requestLogger logs proxy traffic and errors by default, and all HTTP requests if WRONGTRACE_LOG_ALL_HTTP=1.
func requestLogger(next http.Handler) http.Handler {
	logAll := os.Getenv("WRONGTRACE_LOG_ALL_HTTP") == "1" || os.Getenv("WRONGTRACE_VERBOSE") == "true"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		duration := time.Since(start)
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}

		path := r.URL.Path

		category := "HTTP"
		switch {
		case strings.HasPrefix(path, "/proxy") || strings.HasPrefix(path, "/v1/chat") || strings.HasPrefix(path, "/v1/messages") || (strings.HasPrefix(path, "/v1/") && !strings.HasPrefix(path, "/v1/traces")):
			category = "PROXY"
		case strings.HasPrefix(path, "/api/ws") || path == "/ws":
			category = "WS"
		case strings.HasPrefix(path, "/api"):
			category = "API"
		case strings.HasPrefix(path, "/v1/traces") || strings.HasPrefix(path, "/profiler"):
			category = "OTLP"
		}

		// PROXY handles its own detailed lifecycle logging with internal Request IDs.
		if category == "PROXY" {
			if status >= 500 {
				log.Printf("[%s] %d %s %s (%v, %d bytes)", category, status, r.Method, path, duration.Round(time.Millisecond/10), ww.BytesWritten())
			}
			return
		}

		// Only error responses (status >= 400) or explicit WRONGTRACE_LOG_ALL_HTTP=1 are logged for API/OTLP/WS.
		if !logAll && status < 400 {
			return
		}

		if (path == "/api/health" || path == "/api/atlas/status" || path == "/favicon.ico") && status == http.StatusOK {
			return
		}

		log.Printf("[%s] %d %s %s (%v, %d bytes)", category, status, r.Method, path, duration.Round(time.Millisecond/10), ww.BytesWritten())
	})
}
