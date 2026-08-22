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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/wrongstack/wrongtrace/internal/core"
)

// EngineAPI is the slice of *core.Engine the HTTP layer needs. Declared as an
// interface so handlers can be exercised with a fake in tests.
type EngineAPI interface {
	Metrics() (core.MetricsSnapshot, error)
	FileHealth(path string) (core.IPCHealth, error)
	Hub() *core.Hub
	Repo() string
}

// Config configures a Server.
type Config struct {
	Port   int
	Engine *core.Engine
}

// Server bundles the HTTP listener and chi router.
type Server struct {
	cfg    Config
	router chi.Router
	hs     *http.Server
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
	s.hs = hs
	log.Printf("http: listening on http://localhost%s", addr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully drains in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.hs == nil {
		return nil
	}
	return s.hs.Shutdown(ctx)
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

	h := &Handlers{Engine: s.cfg.Engine} // *core.Engine satisfies EngineAPI
	r.Route("/api", func(r chi.Router) {
		r.Get("/health", h.Health)
		r.Get("/metrics/overview", h.Overview)
		r.Get("/metrics/thrashing", h.Thrashing)
		r.Get("/metrics/models", h.Models)
		r.Get("/metrics/recent", h.RecentEvents)
		r.Get("/file/health", h.FileHealth)
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
