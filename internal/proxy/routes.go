package proxy

import (
	"strings"
	"sync"
	"time"
)

// ProxyRoute defines an upstream mapping configured by the user.
type ProxyRoute struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	PathPrefix     string    `json:"path_prefix"`     // e.g. "/proxy/ollama", "/v1/groq", "/gemini"
	TargetUpstream string    `json:"target_upstream"` // e.g. "http://localhost:11434/v1", "https://api.groq.com/openai/v1"
	ProtocolType   string    `json:"protocol_type"`   // "openai", "openai-compatible", "anthropic", "gemini", "custom"
	DefaultModel   string    `json:"default_model,omitempty"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
}

// RouteManager manages user-configured dynamic gateway routes.
type RouteManager struct {
	mu     sync.RWMutex
	routes map[string]ProxyRoute
}

// NewRouteManager initializes a clean dynamic route manager.
func NewRouteManager() *RouteManager {
	return &RouteManager{
		routes: make(map[string]ProxyRoute),
	}
}

// AllRoutes returns a snapshot list of all configured routes.
func (rm *RouteManager) AllRoutes() []ProxyRoute {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	out := make([]ProxyRoute, 0, len(rm.routes))
	for _, r := range rm.routes {
		out = append(out, r)
	}
	return out
}

// UpsertRoute saves or updates a route.
func (rm *RouteManager) UpsertRoute(r ProxyRoute) ProxyRoute {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if r.ID == "" {
		r.ID = "route-" + randomID("")
	}
	if !strings.HasPrefix(r.PathPrefix, "/") {
		r.PathPrefix = "/" + r.PathPrefix
	}
	r.CreatedAt = time.Now().UTC()
	rm.routes[r.ID] = r
	return r
}

// DeleteRoute removes a route by ID.
func (rm *RouteManager) DeleteRoute(id string) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if _, ok := rm.routes[id]; ok {
		delete(rm.routes, id)
		return true
	}
	return false
}

// MatchRoute matches an incoming URL path against configured routes.
func (rm *RouteManager) MatchRoute(path string) (*ProxyRoute, string) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	cleanPath := path
	for _, r := range rm.routes {
		if !r.Enabled {
			continue
		}
		if strings.HasPrefix(cleanPath, r.PathPrefix) {
			remaining := strings.TrimPrefix(cleanPath, r.PathPrefix)
			if remaining == "" {
				remaining = "/"
			}
			return &r, remaining
		}
	}
	return nil, cleanPath
}
