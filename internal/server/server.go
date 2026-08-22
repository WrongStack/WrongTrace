// Package server exposes the embedded React dashboard and JSON API. It binds
// chi routes for /api/*, upgrades /api/ws to WebSocket via gorilla, and serves
// web/dist/* with SPA fallback so client-side routes work on refresh.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/models"
	"github.com/wrongstack/wrongtrace/internal/proxy"
)

// EngineAPI is the slice of *core.Engine the HTTP layer needs. Declared as an
// interface so handlers can be exercised with a fake in tests.
type EngineAPI interface {
	Metrics() (core.MetricsSnapshot, error)
	Atlas() (core.AtlasSnapshot, error)
	FileHealth(path string) (core.IPCHealth, error)
	CheckGuardrail(path string) (core.GuardrailResult, error)
	LockFile(path, reason string)
	UnlockFile(path string)
	IsFileLocked(path string) (bool, string)
	ModelCatalog() []models.ModelInfo
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
	RemoveProject(id string) error
	GetSettings() core.AppSettings
	UpdateSettings(s core.AppSettings) core.AppSettings
	VacuumDB() error
	ClearStale(days int) (int64, error)
	Hub() *core.Hub
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
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	h := &Handlers{
		Engine:     s.cfg.Engine,
		SocketPath: s.cfg.SocketPath,
		Proxy:      proxy.NewGatewayProxy(proxy.Config{Reporter: s.cfg.Engine}),
	}
	r.Route("/api", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Get("/metrics/overview", h.Overview)
		r.Get("/metrics/thrashing", h.Thrashing)
		r.Get("/metrics/models", h.Models)
		r.Get("/metrics/recent", h.RecentEvents)
		r.Get("/atlas", h.Atlas)
		r.Get("/file/health", h.FileHealth)
		r.Get("/guardrail/check", h.CheckGuardrail)
		r.Post("/guardrail/lock", h.LockFile)
		r.Post("/guardrail/unlock", h.UnlockFile)

		r.Get("/proxy/routes", h.ListProxyRoutes)
		r.Post("/proxy/routes", h.UpsertProxyRoute)
		r.Delete("/proxy/routes/{id}", h.DeleteProxyRoute)

		r.Get("/projects", h.ListProjects)
		r.Post("/projects", h.AddProject)
		r.Get("/projects/import/wrongstack", h.PreviewFromWrongStack)
		r.Post("/projects/import/wrongstack", h.ImportFromWrongStack)
		r.Get("/projects/{id}", h.GetProject)
		r.Put("/projects/{id}", h.UpdateProject)
		r.Post("/projects/{id}/activate", h.SwitchActiveProject)
		r.Delete("/projects/{id}", h.RemoveProject)

		r.Get("/settings", h.GetSettings)
		r.Post("/settings", h.UpdateSettings)
		r.Post("/settings/vacuum", h.VacuumDB)
		r.Post("/settings/clear-stale", h.ClearStale)

		r.Get("/models/catalog", h.ModelCatalog)
		r.Post("/models/catalog", h.UpsertModel)
		r.Post("/models/sync", h.SyncModels)
		r.Post("/models/calculate-cost", h.CalculateCost)
		r.Get("/ws", h.WebSocket)
	})

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
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	}
}
