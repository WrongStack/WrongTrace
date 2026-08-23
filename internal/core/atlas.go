package core

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// IndexProgress represents the realtime or final indexing completeness of the workspace AST cache.
type IndexProgress struct {
	IsIndexing      bool      `json:"is_indexing"`
	TotalDiscovered int       `json:"total_discovered"`
	EligibleFiles   int       `json:"eligible_files"`
	IndexedFiles    int       `json:"indexed_files"`
	SkippedFiles    int       `json:"skipped_files"`
	FailedFiles     int       `json:"failed_files"`
	Percentage      float64   `json:"percentage"`
	CurrentFile     string    `json:"current_file,omitempty"`
	DurationMs      int64     `json:"duration_ms"`
	LastIndexedAt   time.Time `json:"last_indexed_at"`
}

// AtlasSnapshot represents the entire repository's structure:
// Packages -> Files -> Symbols with their health and churn metrics.
type AtlasSnapshot struct {
	Repo        string         `json:"repo"`
	GeneratedAt time.Time      `json:"generated_at"`
	IsMonorepo  bool           `json:"is_monorepo"`
	Workspaces  []string       `json:"workspaces,omitempty"`
	Packages    []AtlasPackage `json:"packages"`
	TotalFiles  int            `json:"total_files"`
	TotalLOC    int            `json:"total_loc"`
	TotalNodes  int            `json:"total_nodes"`
	IndexStatus IndexProgress  `json:"index_status"`
}

// AtlasPackage groups files belonging to a specific directory or package module.
type AtlasPackage struct {
	Path      string      `json:"path"`                // e.g. "internal/ast", "web/src/components", "."
	Name      string      `json:"name"`                // e.g. "ast", "components", "root"
	Workspace string      `json:"workspace,omitempty"` // Monorepo scope e.g. "apps/web", "packages/core", "internal"
	Files     []AtlasFile `json:"files"`
	TotalLOC  int         `json:"total_loc"`
	IsFragile bool        `json:"is_fragile"`
}

// AtlasFile represents a single source file and its symbols.
type AtlasFile struct {
	Path                 string        `json:"path"`
	Name                 string        `json:"name"`
	Language             string        `json:"language"`
	HealthScore          int           `json:"health_score"`
	IsFragile            bool          `json:"is_fragile"`
	RecentThrashingCount int           `json:"recent_thrashing_count"`
	TotalLOC             int           `json:"total_loc"`
	Symbols              []AtlasSymbol `json:"symbols"`
}

// AtlasSymbol represents an individual AST node (function, method, class, struct, arrow function).
type AtlasSymbol struct {
	Signature   string    `json:"node_signature"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	StartLine   uint32    `json:"start_line"`
	EndLine     uint32    `json:"end_line"`
	LOC         int       `json:"lines_of_code"`
	Status      string    `json:"status"` // ACTIVE, MODIFIED, ADDED, DELETED
	EditCount   int       `json:"edit_count"`
	LastAction  string    `json:"last_action"`
	LastModel   string    `json:"last_model"`
	LastEventAt time.Time `json:"last_event_time"`
	Hash        string    `json:"ast_content_hash"`
}

// PrimeDirectory recursively parses all supported files in dir so they are immediately available in the AST cache without generating diff events.
func (e *Engine) PrimeDirectory(dir string) {
	if e.cfg.AST == nil || dir == "" {
		return
	}
	start := time.Now()

	e.indexMu.Lock()
	e.indexStatus = IndexProgress{
		IsIndexing: true,
	}
	e.indexMu.Unlock()

	var discovered, eligible, indexed, skipped, failed int

	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			if path == dir {
				return nil
			}
			if isIgnoredDir(filepath.Base(path)) {
				return filepath.SkipDir
			}
			return nil
		}

		discovered++
		if !e.parseEligible(path) {
			skipped++
			return nil
		}

		eligible++
		e.indexMu.Lock()
		e.indexStatus.TotalDiscovered = discovered
		e.indexStatus.EligibleFiles = eligible
		e.indexStatus.CurrentFile = filepath.Base(path)
		e.indexMu.Unlock()

		src, rerr := os.ReadFile(path)
		if rerr != nil {
			failed++
			return nil
		}
		snap, perr := e.cfg.AST.Parse(path, src)
		if perr != nil || snap == nil {
			failed++
			return nil
		}
		e.cfg.AST.SetSnapshot(snap)
		indexed++

		e.indexMu.Lock()
		e.indexStatus.IndexedFiles = indexed
		e.indexStatus.SkippedFiles = skipped
		e.indexStatus.FailedFiles = failed
		if eligible > 0 {
			e.indexStatus.Percentage = math.Round((float64(indexed)/float64(eligible))*10000) / 100
		}
		e.indexMu.Unlock()

		return nil
	})

	e.indexMu.Lock()
	e.indexStatus.IsIndexing = false
	e.indexStatus.DurationMs = time.Since(start).Milliseconds()
	e.indexStatus.LastIndexedAt = time.Now().UTC()
	e.indexStatus.CurrentFile = ""
	e.indexStatus.TotalDiscovered = discovered
	e.indexStatus.EligibleFiles = eligible
	e.indexStatus.IndexedFiles = indexed
	e.indexStatus.SkippedFiles = skipped
	e.indexStatus.FailedFiles = failed
	if eligible > 0 {
		e.indexStatus.Percentage = math.Round((float64(indexed)/float64(eligible))*10000) / 100
	} else if discovered > 0 {
		e.indexStatus.Percentage = 100.0
	}
	e.indexMu.Unlock()

	if e.hub != nil {
		e.hub.Broadcast(WSEvent{
			Type:    "index_progress",
			Payload: e.IndexStatus(),
		})
	}
}

// Atlas aggregates the full Code Atlas graph containing packages, files, and symbols, optionally filtered by repo_name.
func (e *Engine) Atlas(repoFilter ...string) (AtlasSnapshot, error) {
	var filter string
	if len(repoFilter) > 0 && repoFilter[0] != "" {
		filter = repoFilter[0]
	} else if active := e.GetActiveProject(); active != nil && active.Name != "" {
		filter = active.Name
	} else {
		filter = e.cfg.RepoName
	}

	snap := AtlasSnapshot{
		Repo:        filter,
		GeneratedAt: time.Now().UTC(),
		Packages:    []AtlasPackage{},
		IndexStatus: e.IndexStatus(),
	}

	var nodeStats map[string]db.NodeStat
	var allHealth map[string]db.FileHealth
	if e.cfg.Store != nil {
		nodeStats, _ = e.cfg.Store.AllNodeStats(filter)
		allHealth, _ = e.cfg.Store.AllFilesHealth(filter)
	}
	if nodeStats == nil {
		nodeStats = make(map[string]db.NodeStat)
	}
	if allHealth == nil {
		allHealth = make(map[string]db.FileHealth)
	}

	var snapshots map[string]*ast.FileSnapshot
	if e.cfg.AST != nil {
		snapshots = e.cfg.AST.AllSnapshots()
	}
	if snapshots == nil {
		snapshots = make(map[string]*ast.FileSnapshot)
	}

	activeProj := e.GetActiveProject()
	var activePath string
	if filter != "" {
		for _, p := range e.ListProjects() {
			if strings.EqualFold(p.Name, filter) || strings.EqualFold(p.ID, filter) {
				activePath = filepath.Clean(p.Path)
				snap.Repo = p.Name
				break
			}
		}
	}
	if activePath == "" && activeProj != nil && activeProj.Path != "" {
		activePath = filepath.Clean(activeProj.Path)
	}

	// Check if activePath matches loaded snapshots
	var hasActivePathMatch bool
	if activePath != "" {
		for p := range snapshots {
			if strings.HasPrefix(strings.ToLower(filepath.Clean(p)), strings.ToLower(activePath)) {
				hasActivePathMatch = true
				break
			}
		}
	}

	// Group files by top-level package scope (prevents granular subfolder explosion)
	pkgMap := make(map[string]*AtlasPackage)

	for path, fileSnap := range snapshots {
		cleanPath := filepath.Clean(path)
		if hasActivePathMatch && !strings.HasPrefix(strings.ToLower(cleanPath), strings.ToLower(activePath)) {
			continue
		}

		relPath := cleanPath
		if hasActivePathMatch {
			if r, err := filepath.Rel(activePath, cleanPath); err == nil && !strings.HasPrefix(r, "..") {
				relPath = r
			}
		}
		relPath = filepath.ToSlash(relPath)
		pkgPath, pkgName, ws := resolvePackageScope(relPath)

		pkg, exists := pkgMap[pkgPath]
		if !exists {
			pkg = &AtlasPackage{
				Path:      pkgPath,
				Name:      pkgName,
				Workspace: ws,
				Files:     []AtlasFile{},
				TotalLOC:  0,
			}
			pkgMap[pkgPath] = pkg
		}

		lang := ast.DetectLanguage(path).String()
		af := AtlasFile{
			Path:        relPath,
			Name:        filepath.Base(path),
			Language:    lang,
			HealthScore: 100,
			Symbols:     []AtlasSymbol{},
		}

		// Fast in-memory health lookup from batch query with normalized and relative path fallback
		h, ok := allHealth[relPath]
		if !ok {
			h, ok = allHealth[cleanPath]
		}
		if !ok {
			h, ok = allHealth[filepath.ToSlash(cleanPath)]
		}
		if !ok {
			h, ok = allHealth[path]
		}
		if ok {
			af.HealthScore = h.HealthScore
			af.IsFragile = h.IsFragile
			af.RecentThrashingCount = h.RecentThrashingCount
			if h.IsFragile {
				pkg.IsFragile = true
			}
		}

		// Collect symbols
		for _, sig := range fileSnap.SortedSignatures() {
			n := fileSnap.Nodes[sig]
			sym := AtlasSymbol{
				Signature: sig,
				Name:      symbolShortName(sig),
				Kind:      string(n.Kind),
				StartLine: n.StartLine,
				EndLine:   n.EndLine,
				LOC:       n.LOC,
				Status:    "ACTIVE",
				Hash:      n.Hash,
			}

			if stat, ok := nodeStats[sig]; ok {
				sym.EditCount = stat.EditCount
				sym.LastAction = stat.LastAction
				sym.LastModel = stat.LastModel
				sym.LastEventAt = stat.LastEventAt
				if stat.LastAction == "MODIFIED" {
					sym.Status = "MODIFIED"
				} else if stat.LastAction == "DELETED" {
					sym.Status = "DELETED"
				}
			}

			af.TotalLOC += sym.LOC
			af.Symbols = append(af.Symbols, sym)
		}

		pkg.TotalLOC += af.TotalLOC
		pkg.Files = append(pkg.Files, af)
		snap.TotalFiles++
		snap.TotalLOC += af.TotalLOC
		snap.TotalNodes += len(af.Symbols)
	}

	// Sort packages and files deterministically
	pkgPaths := make([]string, 0, len(pkgMap))
	for p := range pkgMap {
		pkgPaths = append(pkgPaths, p)
	}
	sort.Strings(pkgPaths)

	workspaceSet := make(map[string]struct{})
	for _, p := range pkgPaths {
		pkg := pkgMap[p]
		sort.Slice(pkg.Files, func(i, j int) bool {
			return pkg.Files[i].Name < pkg.Files[j].Name
		})
		if pkg.Workspace != "" && pkg.Workspace != "root" {
			workspaceSet[pkg.Workspace] = struct{}{}
		}
		snap.Packages = append(snap.Packages, *pkg)
	}

	if len(workspaceSet) >= 2 {
		snap.IsMonorepo = true
		snap.Workspaces = make([]string, 0, len(workspaceSet))
		for ws := range workspaceSet {
			snap.Workspaces = append(snap.Workspaces, ws)
		}
		sort.Strings(snap.Workspaces)
	}

	return snap, nil
}

func resolvePackageScope(filePath string) (pkgPath string, pkgName string, workspace string) {
	dir := filepath.ToSlash(filepath.Dir(filePath))
	clean := filepath.ToSlash(filepath.Clean(dir))
	if clean == "." || clean == "" || clean == "root" {
		return "root", "root", "root"
	}

	parts := strings.Split(clean, "/")
	if len(parts) == 1 {
		return parts[0], parts[0], parts[0]
	}

	first := parts[0]
	// Monorepo containers: packages/xyz, apps/xyz, services/xyz, libs/xyz, modules/xyz
	if first == "packages" || first == "apps" || first == "services" || first == "libs" || first == "modules" {
		ws := first + "/" + parts[1]
		return ws, parts[1], ws
	}

	// Standard Go layout: internal/<subpkg>, cmd/<subpkg>, pkg/<subpkg>
	if first == "internal" || first == "cmd" || first == "pkg" {
		return first + "/" + parts[1], parts[1], first
	}

	// Web / Frontend apps: web/src/components, web/src/pages -> group into "web"
	if first == "web" || first == "frontend" || first == "client" || first == "ui" {
		return first, first, first
	}

	// Default: 2 levels max (e.g. docs/api, test/e2e)
	return parts[0] + "/" + parts[1], parts[1], parts[0]
}

func symbolShortName(sig string) string {
	if idx := strings.LastIndex(sig, "::"); idx != -1 {
		return sig[idx+2:]
	}
	if idx := strings.LastIndex(sig, ":"); idx != -1 {
		return sig[idx+1:]
	}
	return sig
}
