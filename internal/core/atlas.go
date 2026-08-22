package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// AtlasSnapshot represents the entire repository's structure:
// Packages -> Files -> Symbols with their health and churn metrics.
type AtlasSnapshot struct {
	Repo        string         `json:"repo"`
	GeneratedAt time.Time      `json:"generated_at"`
	Packages    []AtlasPackage `json:"packages"`
	TotalFiles  int            `json:"total_files"`
	TotalLOC    int            `json:"total_loc"`
	TotalNodes  int            `json:"total_nodes"`
}

// AtlasPackage groups files belonging to a specific directory or package module.
type AtlasPackage struct {
	Path      string      `json:"path"` // e.g. "internal/ast", "web/src/components", "."
	Name      string      `json:"name"` // e.g. "ast", "components", "root"
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
	if e.cfg.AST == nil {
		return
	}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			for _, ig := range []string{".git", "node_modules", "vendor", "dist", "build", "target", ".wrongtrace"} {
				if base == ig {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !e.shouldSkip(path) {
			src, rerr := os.ReadFile(path)
			if rerr == nil {
				snap, perr := e.cfg.AST.Parse(path, src)
				if perr == nil && snap != nil {
					e.cfg.AST.SetSnapshot(snap)
				}
			}
		}
		return nil
	})
}

// Atlas aggregates the full Code Atlas graph containing packages, files, and symbols.
func (e *Engine) Atlas() (AtlasSnapshot, error) {
	snap := AtlasSnapshot{
		Repo:        e.cfg.RepoName,
		GeneratedAt: time.Now().UTC(),
		Packages:    []AtlasPackage{},
	}

	var nodeStats map[string]db.NodeStat
	if e.cfg.Store != nil {
		nodeStats, _ = e.cfg.Store.AllNodeStats()
	}
	if nodeStats == nil {
		nodeStats = make(map[string]db.NodeStat)
	}

	var snapshots map[string]*ast.FileSnapshot
	if e.cfg.AST != nil {
		snapshots = e.cfg.AST.AllSnapshots()
	}
	if snapshots == nil {
		snapshots = make(map[string]*ast.FileSnapshot)
	}

	// Group files by directory / package
	pkgMap := make(map[string]*AtlasPackage)

	for path, fileSnap := range snapshots {
		relPath := filepath.ToSlash(path)
		dir := filepath.ToSlash(filepath.Dir(path))
		if dir == "" || dir == "." {
			dir = "root"
		}
		pkgName := filepath.Base(dir)
		if pkgName == "." || pkgName == "/" || pkgName == "\\" {
			pkgName = "root"
		}

		pkg, exists := pkgMap[dir]
		if !exists {
			pkg = &AtlasPackage{
				Path:     dir,
				Name:     pkgName,
				Files:    []AtlasFile{},
				TotalLOC: 0,
			}
			pkgMap[dir] = pkg
		}

		lang := ast.DetectLanguage(path).String()
		af := AtlasFile{
			Path:        relPath,
			Name:        filepath.Base(path),
			Language:    lang,
			HealthScore: 100,
			Symbols:     []AtlasSymbol{},
		}

		// Calculate file health from store if available
		if e.cfg.Store != nil {
			if h, err := e.cfg.Store.FileHealth(relPath); err == nil {
				af.HealthScore = h.HealthScore
				af.IsFragile = h.IsFragile
				af.RecentThrashingCount = h.RecentThrashingCount
				if h.IsFragile {
					pkg.IsFragile = true
				}
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

	for _, p := range pkgPaths {
		pkg := pkgMap[p]
		sort.Slice(pkg.Files, func(i, j int) bool {
			return pkg.Files[i].Name < pkg.Files[j].Name
		})
		snap.Packages = append(snap.Packages, *pkg)
	}

	return snap, nil
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
