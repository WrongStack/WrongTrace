package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProxyRoute defines an upstream mapping configured by the user.
type ProxyRoute struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	PathPrefix     string    `json:"path_prefix"`     // e.g. "/proxy/zai", "/proxy/ollama", "/v1/groq"
	TargetUpstream string    `json:"target_upstream"` // e.g. "https://api.z.ai/api/coding/paas/v4", "http://localhost:11434/v1"
	ProtocolType   string    `json:"protocol_type"`   // "openai", "openai-compatible", "anthropic", "gemini", "custom"
	DefaultModel   string    `json:"default_model,omitempty"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
}

// RouteManager manages user-configured dynamic gateway routes with disk persistence.
type RouteManager struct {
	mu     sync.RWMutex
	routes map[string]ProxyRoute
}

// NewRouteManager initializes a dynamic route manager and loads saved routes from disk.
func NewRouteManager() *RouteManager {
	rm := &RouteManager{
		routes: make(map[string]ProxyRoute),
	}
	rm.loadDisk()
	return rm
}

// AllRoutes returns a snapshot list of all configured routes.
func (rm *RouteManager) AllRoutes() []ProxyRoute {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.sortedRoutesLocked()
}

func (rm *RouteManager) sortedRoutesLocked() []ProxyRoute {
	out := make([]ProxyRoute, 0, len(rm.routes))
	for _, r := range rm.routes {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		left := len(strings.Trim(out[i].PathPrefix, "/"))
		right := len(strings.Trim(out[j].PathPrefix, "/"))
		if left != right {
			return left > right
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// UpsertRoute saves or updates a route and persists it to disk.
func (rm *RouteManager) UpsertRoute(r ProxyRoute) ProxyRoute {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if r.ID == "" {
		r.ID = randomID("route")
	}
	if !strings.HasPrefix(r.PathPrefix, "/") {
		r.PathPrefix = "/" + r.PathPrefix
	}
	r.CreatedAt = time.Now().UTC()
	rm.routes[r.ID] = r
	rm.saveDisk()
	return r
}

// DeleteRoute removes a route by ID and updates disk persistence.
func (rm *RouteManager) DeleteRoute(id string) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if _, ok := rm.routes[id]; ok {
		delete(rm.routes, id)
		rm.saveDisk()
		return true
	}
	return false
}

// MatchRoute matches an incoming URL path against configured routes flexibly.
func (rm *RouteManager) MatchRoute(path string) (*ProxyRoute, string) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	normPath := "/" + strings.Trim(filepath.ToSlash(path), "/")
	lowerPath := strings.ToLower(normPath)

	for _, r := range rm.sortedRoutesLocked() {
		if !r.Enabled {
			continue
		}

		pfx := "/" + strings.Trim(filepath.ToSlash(r.PathPrefix), "/")
		lowerPfx := strings.ToLower(pfx)

		// 1. Direct or prefix match with configured PathPrefix (e.g. /proxy/zai or /proxy/zai/...)
		if lowerPath == lowerPfx {
			return &r, "/"
		}
		if strings.HasPrefix(lowerPath, lowerPfx+"/") {
			remaining := normPath[len(pfx):]
			if remaining == "" || !strings.HasPrefix(remaining, "/") {
				remaining = "/" + strings.TrimPrefix(remaining, "/")
			}
			return &r, remaining
		}

		// 2. Flexible /proxy/<prefix> match if route was configured as /<name> or <name>
		slug := strings.TrimPrefix(lowerPfx, "/proxy")
		slug = "/" + strings.Trim(slug, "/")
		if slug != "/" {
			proxySlug := "/proxy" + slug
			if lowerPath == proxySlug {
				return &r, "/"
			}
			if strings.HasPrefix(lowerPath, proxySlug+"/") {
				remaining := normPath[len(proxySlug):]
				if remaining == "" || !strings.HasPrefix(remaining, "/") {
					remaining = "/" + strings.TrimPrefix(remaining, "/")
				}
				return &r, remaining
			}
		}

		// 3. Match by Route Name (e.g. /proxy/zai matches route named "zai" or "ZAI")
		trimmedName := strings.ToLower(strings.TrimSpace(r.Name))
		if trimmedName != "" {
			nameSlug := "/proxy/" + trimmedName
			if lowerPath == nameSlug {
				return &r, "/"
			}
			if strings.HasPrefix(lowerPath, nameSlug+"/") {
				remaining := normPath[len(nameSlug):]
				if remaining == "" || !strings.HasPrefix(remaining, "/") {
					remaining = "/" + strings.TrimPrefix(remaining, "/")
				}
				return &r, remaining
			}
		}
	}
	return nil, path
}

func routesJSONPath() string {
	if dir := os.Getenv("WRONGTRACE_HOME"); dir != "" {
		return filepath.Join(dir, "proxy_routes.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".wrongtrace", "proxy_routes.json")
	}
	return filepath.Join(home, ".wrongtrace", "proxy_routes.json")
}

func (rm *RouteManager) saveDisk() {
	p := routesJSONPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)

	list := rm.sortedRoutesLocked()

	data, err := json.MarshalIndent(map[string]interface{}{"routes": list}, "", "  ")
	if err == nil {
		if os.WriteFile(p, data, 0o600) == nil {
			_ = os.Chmod(p, 0o600)
		}
	}
}

func (rm *RouteManager) loadDisk() {
	p := routesJSONPath()
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}

	var parsed struct {
		Routes []ProxyRoute `json:"routes"`
	}
	if err := json.Unmarshal(data, &parsed); err == nil {
		for _, r := range parsed.Routes {
			if r.ID != "" {
				rm.routes[r.ID] = r
			}
		}
	}
}
