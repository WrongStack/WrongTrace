package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// ErrProjectNotFound is returned by project operations that reference an
// unknown project ID. HTTP callers map it to 404 (see server handlers).
var ErrProjectNotFound = errors.New("project not found")

// ErrWrongStackSourceMissing is returned by ImportFromWrongStack when
// ~/.wrongstack/projects.json does not exist. HTTP callers map it to 404.
var ErrWrongStackSourceMissing = errors.New("wrongstack projects.json not found")

// ProjectProfile represents a monitored workspace/repository with its dedicated SQLite DB,
// agent session log paths, and auto-discovered agent statistics.
type ProjectProfile struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	Path               string         `json:"path"`
	DBPath             string         `json:"db_path"`
	PrimaryLanguage    string         `json:"primary_language"`
	ClaudeLogsPath     string         `json:"claude_logs_path"`
	CursorLogsPath     string         `json:"cursor_logs_path"`
	ClineLogsPath      string         `json:"cline_logs_path"`
	AiderLogsPath      string         `json:"aider_logs_path"`
	WrongStackLogsPath string         `json:"wrongstack_logs_path"`
	CustomLogsPath     string         `json:"custom_logs_path"`
	DiscoveredSessions map[string]int `json:"discovered_sessions"`
	CreatedAt          time.Time      `json:"created_at"`
	IsActive           bool           `json:"is_active"`
}

// Project is an alias for backward compatibility.
type Project = ProjectProfile

// WatcherAPI decouples the core engine from concrete watcher types.
type WatcherAPI interface {
	AddWatchDir(dir string) error
	RemoveWatchDir(dir string) error
}

// SetWatcher links a file watcher instance to the engine.
func (e *Engine) SetWatcher(w WatcherAPI) {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	e.watcher = w
}

// ListProjects returns all currently registered project profiles.
func (e *Engine) ListProjects() []ProjectProfile {
	e.lockMu.RLock()
	defer e.lockMu.RUnlock()
	out := make([]ProjectProfile, 0, len(e.projects))
	for _, p := range e.projects {
		out = append(out, p)
	}
	return out
}

// GetProject returns a single project profile by ID.
func (e *Engine) GetProject(id string) (ProjectProfile, error) {
	e.lockMu.RLock()
	defer e.lockMu.RUnlock()
	p, ok := e.projects[id]
	if !ok {
		return ProjectProfile{}, fmt.Errorf("project not found: %s", id)
	}
	return p, nil
}

// GetActiveProject returns the currently active project profile.
func (e *Engine) GetActiveProject() *ProjectProfile {
	e.lockMu.RLock()
	defer e.lockMu.RUnlock()
	if e.cfg.RepoName != "" && e.cfg.RepoName != "default" {
		for _, p := range e.projects {
			if strings.EqualFold(p.Name, e.cfg.RepoName) || strings.EqualFold(p.ID, e.cfg.RepoName) {
				cp := p
				return &cp
			}
		}
		return nil
	}
	if e.activeProjectID != "" {
		if p, ok := e.projects[e.activeProjectID]; ok {
			cp := p
			return &cp
		}
	}
	for _, p := range e.projects {
		if p.IsActive {
			cp := p
			return &cp
		}
	}
	return nil
}

// FindProjectForFile finds the registered project whose root directory contains the given file path.
func (e *Engine) FindProjectForFile(filePath string) (ProjectProfile, bool) {
	if filePath == "" {
		return ProjectProfile{}, false
	}
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}
	normFile := strings.ToLower(filepath.Clean(absPath))

	e.lockMu.RLock()
	defer e.lockMu.RUnlock()

	var bestMatch ProjectProfile
	var bestMatchLen int

	for _, p := range e.projects {
		if p.Path == "" {
			continue
		}
		absRoot, err := filepath.Abs(p.Path)
		if err != nil {
			absRoot = p.Path
		}
		normRoot := strings.ToLower(filepath.Clean(absRoot))
		if normFile == normRoot || strings.HasPrefix(normFile, normRoot+string(filepath.Separator)) {
			if len(normRoot) > bestMatchLen {
				bestMatch = p
				bestMatchLen = len(normRoot)
			}
		}
	}

	if bestMatchLen > 0 {
		return bestMatch, true
	}
	return ProjectProfile{}, false
}

// SwitchActiveProject marks a project as active and switches the active database context.
func (e *Engine) SwitchActiveProject(id string) (*ProjectProfile, error) {
	e.lockMu.Lock()
	target, ok := e.projects[id]
	if !ok {
		e.lockMu.Unlock()
		return nil, fmt.Errorf("project not found: %s", id)
	}

	for k, p := range e.projects {
		p.IsActive = (k == id)
		e.projects[k] = p
	}

	target.IsActive = true
	e.projects[id] = target
	e.cfg.RepoName = target.Name
	SaveProjectsIndex(e.projects)
	watcher := e.watcher
	e.lockMu.Unlock()

	// Hot-swap database to target project's dedicated SQLite store
	if target.DBPath != "" {
		if newStore, err := db.Open(target.DBPath); err == nil {
			_ = newStore.Migrate()
			e.lockMu.Lock()
			oldStore := e.cfg.Store
			e.cfg.Store = newStore
			e.lockMu.Unlock()
			if oldStore != nil && oldStore != newStore {
				// Close now, not on a timer: a lingering handle keeps the
				// old SQLite file locked on Windows (breaks backup/delete of
				// the project dir right after a switch). sql.DB.Close does
				// not cancel queries already running, so only a caller that
				// fetched Store() microseconds ago and has not started its
				// query yet can observe "database is closed" — accepted as
				// the cheaper side of the trade-off.
				_ = oldStore.Close()
			}
		}
	}

	// Reset in-memory AST snapshot cache so old repository symbols do not leak
	if e.cfg.AST != nil {
		e.cfg.AST.Reset()
	}

	// Clear active in-memory agent runs
	e.runMu.Lock()
	e.activeRuns = make(map[string]runMeta)
	e.runMu.Unlock()

	// Re-prime AST and watcher for target workspace directory
	if target.Path != "" {
		go e.PrimeDirectory(target.Path)
		if watcher != nil {
			_ = watcher.AddWatchDir(target.Path)
		}
	}

	e.BumpCacheGen()
	e.hub.Broadcast(WSEvent{Type: "project_switched", Payload: target})

	return &target, nil
}

// UserWrongTraceDir returns the root directory for WrongTrace data in userhome (~/.wrongtrace)
func UserWrongTraceDir() string {
	if dir := os.Getenv("WRONGTRACE_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".wrongtrace")
	}
	return filepath.Join(home, ".wrongtrace")
}

// UserProjectsDir returns the projects directory (~/.wrongtrace/projects)
func UserProjectsDir() string {
	return filepath.Join(UserWrongTraceDir(), "projects")
}

// GetProjectStorageDir returns the isolated storage folder for a project: ~/.wrongtrace/projects/<slug>
func GetProjectStorageDir(slug string) string {
	return filepath.Join(UserProjectsDir(), slug)
}

func sanitizeSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else if r == ' ' || r == '/' || r == '\\' || r == '.' {
			sb.WriteRune('-')
		}
	}
	s := strings.Trim(sb.String(), "-")
	if s == "" {
		return "project"
	}
	return s
}

// SaveProjectsIndex persists the registered project profiles to ~/.wrongtrace/projects.json
func SaveProjectsIndex(projects map[string]ProjectProfile) {
	dir := UserWrongTraceDir()
	_ = os.MkdirAll(dir, 0o755)
	indexPath := filepath.Join(dir, "projects.json")

	list := make([]ProjectProfile, 0, len(projects))
	for _, p := range projects {
		list = append(list, p)
	}

	data, err := json.MarshalIndent(map[string]interface{}{"projects": list}, "", "  ")
	if err == nil {
		_ = os.WriteFile(indexPath, data, 0o644)
	}
}

// LoadProjectsIndex reads previously saved projects from ~/.wrongtrace/projects.json
func LoadProjectsIndex() map[string]ProjectProfile {
	indexPath := filepath.Join(UserWrongTraceDir(), "projects.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return make(map[string]ProjectProfile)
	}

	var parsed struct {
		Projects []ProjectProfile `json:"projects"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return make(map[string]ProjectProfile)
	}

	res := make(map[string]ProjectProfile)
	for _, p := range parsed.Projects {
		if p.ID != "" {
			res[p.ID] = p
		}
	}
	return res
}

// AddProject registers a new project workspace, creates its isolated SQLite DB in ~/.wrongtrace/projects/<slug>/,
// scans for agent session folders, and begins watching its files.
func (e *Engine) AddProject(name, path string) (ProjectProfile, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return ProjectProfile{}, fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		return ProjectProfile{}, fmt.Errorf("directory does not exist: %s", absPath)
	}

	if name == "" {
		name = filepath.Base(absPath)
	}

	id := "proj-" + newID()[:8]
	slug := sanitizeSlug(name)

	// Isolated storage in userhome ~/.wrongtrace/projects/<slug>/
	storageDir := GetProjectStorageDir(slug)
	_ = os.MkdirAll(storageDir, 0o755)
	dbPath := filepath.Join(storageDir, "wrongtrace.db")

	// Ensure dedicated per-project SQLite database is initialized with migrations.
	// The handle is opened only to apply the schema; keeping it open would leak
	// a connection (and on Windows, lock the file) for every project ever added.
	if projStore, err := db.Open(dbPath); err == nil {
		_ = projStore.Migrate()
		_ = projStore.Close()
	}

	// Auto-discover agent session paths and language
	sessions := ScanAgentSessions(absPath)
	lang := DetectPrimaryLanguage(absPath)

	proj := ProjectProfile{
		ID:                 id,
		Name:               name,
		Description:        fmt.Sprintf("Observability workspace for %s", name),
		Path:               absPath,
		DBPath:             dbPath,
		PrimaryLanguage:    lang,
		ClaudeLogsPath:     filepath.Join(absPath, ".claude"),
		CursorLogsPath:     filepath.Join(absPath, ".cursor"),
		ClineLogsPath:      filepath.Join(absPath, ".clinerules"),
		AiderLogsPath:      filepath.Join(absPath, ".aider.chat.history.md"),
		WrongStackLogsPath: FindWrongStackLogsPath(absPath),
		DiscoveredSessions: sessions,
		CreatedAt:          time.Now().UTC(),
	}

	e.lockMu.Lock()
	if e.projects == nil {
		e.projects = make(map[string]ProjectProfile)
	}
	// isFirst must be decided under the lock: reading len(e.projects) before
	// it let two concurrent adds both mark themselves active.
	proj.IsActive = len(e.projects) == 0
	e.projects[id] = proj
	SaveProjectsIndex(e.projects)
	watcher := e.watcher
	e.lockMu.Unlock()

	// Prime atlas directory for the active project only; non-active projects are primed on demand when switched to
	if proj.IsActive {
		go e.PrimeDirectory(absPath)
	}

	// Add to live watcher
	if watcher != nil {
		_ = watcher.AddWatchDir(absPath)
	}

	return proj, nil
}

// FindWrongStackLogsPath locates the global or local WrongStack project session folder.
func FindWrongStackLogsPath(root string) string {
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		if entries, _, err := readWrongStackProjects(homeDir); err == nil {
			targetNorm := normalizeRoot(root)
			for _, proj := range entries {
				if normalizeRoot(proj.Root) == targetNorm && proj.Slug != "" {
					return filepath.Join(homeDir, ".wrongstack", "projects", proj.Slug, "sessions")
				}
			}
		}
	}
	return filepath.Join(root, ".wrongstack")
}

// WrongStackProject is a single entry of ~/.wrongstack/projects.json. Fields
// beyond root/name/slug (lastSeen, createdAt, projectId, ...) are tolerated
// but not needed by WrongTrace, so they are not modeled.
type WrongStackProject struct {
	Name string `json:"name"`
	Root string `json:"root"`
	Slug string `json:"slug"`
}

// wrongStackProjectsPath returns ~/.wrongstack/projects.json for the given
// home directory. WrongStack has no home override of its own, so the path is
// always <home>/.wrongstack/projects.json.
func wrongStackProjectsPath(homeDir string) string {
	return filepath.Join(homeDir, ".wrongstack", "projects.json")
}

// readWrongStackProjects reads and validates ~/.wrongstack/projects.json. It
// returns ErrWrongStackSourceMissing when the file is absent.
func readWrongStackProjects(homeDir string) ([]WrongStackProject, string, error) {
	path := wrongStackProjectsPath(homeDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, path, fmt.Errorf("%w: %s", ErrWrongStackSourceMissing, path)
		}
		return nil, path, fmt.Errorf("read %s: %w", path, err)
	}
	var parsed struct {
		Projects []WrongStackProject `json:"projects"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, path, fmt.Errorf("invalid %s: %w", path, err)
	}
	return parsed.Projects, path, nil
}

// normalizeRoot canonicalizes a workspace path for comparison. Case-folding
// matches the existing WrongStack matching convention in ScanAgentSessions —
// Windows roots arrive with mixed case from both sides.
func normalizeRoot(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return strings.ToLower(filepath.Clean(p))
}

// WrongStackPreviewEntry is one workspace in the preview of
// ~/.wrongstack/projects.json, annotated with what importing it would do.
type WrongStackPreviewEntry struct {
	Name              string `json:"name"`
	Root              string `json:"root"`
	Slug              string `json:"slug"`
	AlreadyRegistered bool   `json:"already_registered"`
	ExistsOnDisk      bool   `json:"exists_on_disk"`
}

// PreviewFromWrongStackResult is the preview of an import run: every
// WrongStack workspace with its eligibility, plus the source path.
type PreviewFromWrongStackResult struct {
	SourcePath string                   `json:"source_path"`
	Entries    []WrongStackPreviewEntry `json:"entries"`
}

// PreviewFromWrongStack lists every workspace in ~/.wrongstack/projects.json
// with the action an import would take for it. It only stats directories and
// consults the in-memory registry — no tree walks — so it stays fast even
// though ImportFromWrongStack itself scans each imported workspace.
func (e *Engine) PreviewFromWrongStack() (PreviewFromWrongStackResult, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return PreviewFromWrongStackResult{}, fmt.Errorf("resolve home directory: %w", err)
	}
	entries, sourcePath, err := readWrongStackProjects(homeDir)
	if err != nil {
		return PreviewFromWrongStackResult{}, err
	}

	e.lockMu.RLock()
	known := make(map[string]struct{}, len(e.projects))
	for _, p := range e.projects {
		known[normalizeRoot(p.Path)] = struct{}{}
	}
	e.lockMu.RUnlock()

	res := PreviewFromWrongStackResult{
		SourcePath: sourcePath,
		Entries:    make([]WrongStackPreviewEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Root) == "" {
			continue
		}
		absRoot := entry.Root
		if abs, absErr := filepath.Abs(absRoot); absErr == nil {
			absRoot = abs
		}
		info, statErr := os.Stat(absRoot)
		res.Entries = append(res.Entries, WrongStackPreviewEntry{
			Name:              entry.Name,
			Root:              absRoot,
			Slug:              entry.Slug,
			AlreadyRegistered: func() bool { _, ok := known[normalizeRoot(absRoot)]; return ok }(),
			ExistsOnDisk:      statErr == nil && info.IsDir(),
		})
	}
	return res, nil
}

// ImportFromWrongStackResult summarizes one import run from WrongStack.
type ImportFromWrongStackResult struct {
	SourcePath      string           `json:"source_path"`
	Found           int              `json:"found"`
	Imported        int              `json:"imported"`
	SkippedExisting int              `json:"skipped_existing"`
	SkippedMissing  int              `json:"skipped_missing"`
	MissingRoots    []string         `json:"missing_roots"`
	Errors          []string         `json:"errors,omitempty"`
	Projects        []ProjectProfile `json:"projects"`
}

// ImportFromWrongStack registers workspaces listed in
// ~/.wrongstack/projects.json that WrongTrace does not already monitor.
// roots selects which entries to import (matched case-insensitively against
// the registry entries' root paths); nil or empty means "all of them", the
// original one-click behavior. Roots already registered are skipped (same
// case-insensitive convention as ScanAgentSessions), so repeated runs are
// idempotent. Entries whose root directory no longer exists on disk are
// reported under missing_roots instead of failing the whole import; the
// first imported project becomes the active workspace (AddProject's rule
// for an empty registry).
//
// The duplicate guard snapshots the registry once: a concurrent AddProject
// registering the same root mid-import can still create a second profile.
// Accepted — AddProject itself is not duplicate-guarded, and making it
// idempotent by path would change the existing POST /api/projects contract.
func (e *Engine) ImportFromWrongStack(roots []string) (ImportFromWrongStackResult, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ImportFromWrongStackResult{}, fmt.Errorf("resolve home directory: %w", err)
	}
	entries, sourcePath, err := readWrongStackProjects(homeDir)
	if err != nil {
		return ImportFromWrongStackResult{}, err
	}

	// Empty selection means all entries (the no-preview, one-click path).
	selected := make(map[string]struct{}, len(roots))
	for _, r := range roots {
		if strings.TrimSpace(r) != "" {
			selected[normalizeRoot(r)] = struct{}{}
		}
	}
	if len(selected) == 0 {
		for _, entry := range entries {
			if strings.TrimSpace(entry.Root) != "" {
				selected[normalizeRoot(entry.Root)] = struct{}{}
			}
		}
	}

	res := ImportFromWrongStackResult{
		SourcePath:   sourcePath,
		Found:        len(entries),
		MissingRoots: []string{},
		Projects:     []ProjectProfile{},
	}

	e.lockMu.RLock()
	known := make(map[string]struct{}, len(e.projects))
	for _, p := range e.projects {
		known[normalizeRoot(p.Path)] = struct{}{}
	}
	e.lockMu.RUnlock()

	for _, entry := range entries {
		if strings.TrimSpace(entry.Root) == "" {
			continue
		}
		root := entry.Root
		if abs, absErr := filepath.Abs(root); absErr == nil {
			root = abs
		}
		if _, ok := selected[normalizeRoot(root)]; !ok {
			continue
		}
		if _, ok := known[normalizeRoot(root)]; ok {
			res.SkippedExisting++
			continue
		}
		if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
			res.SkippedMissing++
			res.MissingRoots = append(res.MissingRoots, root)
			continue
		}
		p, addErr := e.AddProject(entry.Name, root)
		if addErr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s (%s): %v", entry.Name, root, addErr))
			continue
		}
		known[normalizeRoot(p.Path)] = struct{}{}
		res.Imported++
		res.Projects = append(res.Projects, p)
	}

	return res, nil
}

// UpdateProject updates metadata or custom session log paths for a project.
func (e *Engine) UpdateProject(p ProjectProfile) (ProjectProfile, error) {
	e.lockMu.Lock()
	existing, ok := e.projects[p.ID]
	if !ok {
		e.lockMu.Unlock()
		return ProjectProfile{}, fmt.Errorf("%w: %s", ErrProjectNotFound, p.ID)
	}

	if p.Name != "" {
		existing.Name = p.Name
	}
	if p.Description != "" {
		existing.Description = p.Description
	}
	if p.ClaudeLogsPath != "" {
		existing.ClaudeLogsPath = p.ClaudeLogsPath
	}
	if p.CursorLogsPath != "" {
		existing.CursorLogsPath = p.CursorLogsPath
	}
	if p.ClineLogsPath != "" {
		existing.ClineLogsPath = p.ClineLogsPath
	}
	if p.AiderLogsPath != "" {
		existing.AiderLogsPath = p.AiderLogsPath
	}
	if p.CustomLogsPath != "" {
		existing.CustomLogsPath = p.CustomLogsPath
	}

	e.projects[p.ID] = existing
	SaveProjectsIndex(e.projects)
	e.lockMu.Unlock()

	// Re-scan sessions outside the lock: ScanAgentSessions walks many user
	// directories and easily takes seconds — holding lockMu that long stalls
	// the synchronous IPC guardrail checks agents run before every edit.
	sessions := ScanAgentSessions(existing.Path)

	e.lockMu.Lock()
	if cur, ok := e.projects[p.ID]; ok {
		// Keep any metadata applied concurrently while we scanned.
		cur.DiscoveredSessions = sessions
		existing = cur
		e.projects[p.ID] = cur
		SaveProjectsIndex(e.projects)
	}
	e.lockMu.Unlock()

	return existing, nil
}

// RemoveProject removes a project workspace and stops watching it.
func (e *Engine) RemoveProject(id string) error {
	e.lockMu.Lock()
	proj, ok := e.projects[id]
	if !ok {
		e.lockMu.Unlock()
		return fmt.Errorf("project not found: %s", id)
	}
	delete(e.projects, id)
	SaveProjectsIndex(e.projects)
	watcher := e.watcher
	e.lockMu.Unlock()

	if watcher != nil {
		_ = watcher.RemoveWatchDir(proj.Path)
	}
	return nil
}

// RescanProject re-runs agent session discovery for a specific project.
func (e *Engine) RescanProject(id string) (*ProjectProfile, error) {
	e.lockMu.RLock()
	proj, ok := e.projects[id]
	e.lockMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", id)
	}

	// Scan outside lockMu — see UpdateProject for why the walk must not
	// hold the lock.
	sessions := ScanAgentSessions(proj.Path)

	e.lockMu.Lock()
	proj, ok = e.projects[id]
	if !ok {
		e.lockMu.Unlock()
		return nil, fmt.Errorf("project not found: %s", id)
	}
	proj.DiscoveredSessions = sessions
	e.projects[id] = proj
	SaveProjectsIndex(e.projects)
	e.lockMu.Unlock()
	return &proj, nil
}

// RescanAllProjects re-runs session discovery on all registered workspaces.
func (e *Engine) RescanAllProjects() []ProjectProfile {
	// Snapshot under the lock, scan outside it (see UpdateProject).
	e.lockMu.RLock()
	ids := make([]string, 0, len(e.projects))
	paths := make(map[string]string, len(e.projects))
	for id, proj := range e.projects {
		ids = append(ids, id)
		paths[id] = proj.Path
	}
	e.lockMu.RUnlock()

	sessions := make(map[string]map[string]int, len(ids))
	for _, id := range ids {
		sessions[id] = ScanAgentSessions(paths[id])
	}

	e.lockMu.Lock()
	for _, id := range ids {
		if proj, ok := e.projects[id]; ok {
			proj.DiscoveredSessions = sessions[id]
			e.projects[id] = proj
		}
	}
	SaveProjectsIndex(e.projects)
	out := make([]ProjectProfile, 0, len(e.projects))
	for _, p := range e.projects {
		out = append(out, p)
	}
	e.lockMu.Unlock()
	return out
}

var (
	sessScanMu    sync.RWMutex
	sessScanCache = make(map[string]cachedSessScan)
)

const (
	sessScanCacheTTL        = 30 * time.Second
	maxSessScanCacheEntries = 128
)

type cachedSessScan struct {
	counts    map[string]int
	scannedAt time.Time
}

// ScanAgentSessions inspects workspace and global application directories for coding agent artifacts and logs specifically belonging to the target root workspace.
func ScanAgentSessions(root string) map[string]int {
	counts := make(map[string]int)
	if root == "" {
		return counts
	}
	normRoot := strings.ToLower(filepath.Clean(root))
	rootBase := strings.ToLower(filepath.Base(root))

	sessScanMu.RLock()
	if c, ok := sessScanCache[normRoot]; ok && time.Since(c.scannedAt) < sessScanCacheTTL {
		res := make(map[string]int, len(c.counts))
		for k, v := range c.counts {
			res[k] = v
		}
		sessScanMu.RUnlock()
		return res
	}
	sessScanMu.RUnlock()

	homeDir, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	if appData == "" && homeDir != "" {
		appData = filepath.Join(homeDir, "AppData", "Roaming")
	}

	// 1. WrongStack Deep Session Discovery
	if homeDir != "" {
		wsProjectsJSON := filepath.Join(homeDir, ".wrongstack", "projects.json")
		if data, err := os.ReadFile(wsProjectsJSON); err == nil {
			var pFile struct {
				Projects []struct {
					Root string `json:"root"`
					Slug string `json:"slug"`
				} `json:"projects"`
			}
			if json.Unmarshal(data, &pFile) == nil {
				for _, proj := range pFile.Projects {
					if strings.ToLower(filepath.Clean(proj.Root)) == normRoot && proj.Slug != "" {
						sessionsDir := filepath.Join(homeDir, ".wrongstack", "projects", proj.Slug, "sessions")
						var sessCnt int
						if dateEntries, err := os.ReadDir(sessionsDir); err == nil {
							for _, de := range dateEntries {
								if de.IsDir() && de.Name() != "_cas" {
									if strings.HasPrefix(de.Name(), "sess_") {
										sessCnt++
									} else {
										if sessFiles, err := os.ReadDir(filepath.Join(sessionsDir, de.Name())); err == nil {
											for _, sf := range sessFiles {
												if sf.IsDir() && strings.HasPrefix(sf.Name(), "sess_") {
													sessCnt++
												}
											}
										}
									}
								}
							}
						}
						if sessCnt > 0 {
							counts["wrongstack"] = sessCnt
						}
						break
					}
				}
			}
		}
		if counts["wrongstack"] == 0 && dirExists(filepath.Join(root, ".wrongstack")) {
			counts["wrongstack"] = 1
		}
	}

	// 2. Claude Code (Check specific workspace directory under ~/.claude/projects/ & local .claude)
	if homeDir != "" {
		claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
		if entries, err := os.ReadDir(claudeProjectsDir); err == nil {
			var claudeSessCount int
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				dirNameLower := strings.ToLower(e.Name())
				// Claude encodes path by replacing separators/colons with - or --
				if strings.Contains(dirNameLower, rootBase) {
					projDir := filepath.Join(claudeProjectsDir, e.Name())
					if pFiles, err := os.ReadDir(projDir); err == nil {
						for _, pf := range pFiles {
							if !pf.IsDir() && strings.HasSuffix(pf.Name(), ".jsonl") {
								claudeSessCount++
							}
						}
					}
				}
			}
			if claudeSessCount > 0 {
				counts["claude_code"] = claudeSessCount
			}
		}
	}
	if counts["claude_code"] == 0 && dirExists(filepath.Join(root, ".claude")) {
		counts["claude_code"] = 1
	}

	// 3. Google Antigravity & Gemini CLI
	if homeDir != "" {
		brainDir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain")
		if entries, err := os.ReadDir(brainDir); err == nil {
			var agyCount int
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				convDir := filepath.Join(brainDir, e.Name())
				logPath := filepath.Join(convDir, ".system_generated", "logs", "transcript.jsonl")
				if fileExists(logPath) {
					if f, err := os.Open(logPath); err == nil {
						buf := make([]byte, 4096)
						n, _ := f.Read(buf)
						_ = f.Close()
						contentLower := strings.ToLower(string(buf[:n]))
						if strings.Contains(contentLower, normRoot) || strings.Contains(contentLower, rootBase) {
							agyCount++
						}
					}
				}
			}
			if agyCount > 0 {
				counts["antigravity"] = agyCount
			}
		}
	}
	if counts["antigravity"] == 0 && dirExists(filepath.Join(root, ".gemini")) {
		counts["antigravity"] = 1
	}

	// 4. Cursor AI
	if appData != "" {
		cursorStorage := filepath.Join(appData, "Cursor", "User", "workspaceStorage")
		if entries, err := os.ReadDir(cursorStorage); err == nil {
			var cursorCount int
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				wsDir := filepath.Join(cursorStorage, e.Name())
				wsFile := filepath.Join(wsDir, "workspace.json")
				stateFile := filepath.Join(wsDir, "state.vscdb")
				matched := false
				if data, err := os.ReadFile(wsFile); err == nil {
					if strings.Contains(strings.ToLower(string(data)), normRoot) || strings.Contains(strings.ToLower(string(data)), rootBase) {
						matched = true
					}
				} else if fileExists(stateFile) {
					if f, err := os.Open(stateFile); err == nil {
						buf := make([]byte, 8192)
						n, _ := f.Read(buf)
						_ = f.Close()
						if strings.Contains(strings.ToLower(string(buf[:n])), normRoot) || strings.Contains(strings.ToLower(string(buf[:n])), rootBase) {
							matched = true
						}
					}
				}
				if matched {
					cursorCount++
				}
			}
			if cursorCount > 0 {
				counts["cursor"] = cursorCount
			}
		}
	}
	if counts["cursor"] == 0 && dirExists(filepath.Join(root, ".cursor")) {
		counts["cursor"] = 1
	}

	// 5. Windsurf
	if appData != "" {
		windsurfStorage := filepath.Join(appData, "Windsurf", "User", "workspaceStorage")
		if entries, err := os.ReadDir(windsurfStorage); err == nil {
			var wsCount int
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				wsDir := filepath.Join(windsurfStorage, e.Name())
				wsFile := filepath.Join(wsDir, "workspace.json")
				if data, err := os.ReadFile(wsFile); err == nil {
					if strings.Contains(strings.ToLower(string(data)), normRoot) || strings.Contains(strings.ToLower(string(data)), rootBase) {
						wsCount++
					}
				}
			}
			if wsCount > 0 {
				counts["windsurf"] = wsCount
			}
		}
	}
	if counts["windsurf"] == 0 && dirExists(filepath.Join(root, ".windsurf")) {
		counts["windsurf"] = 1
	}

	// 6. Trae (ByteDance)
	if appData != "" {
		traeStorage := filepath.Join(appData, "Trae", "User", "workspaceStorage")
		if entries, err := os.ReadDir(traeStorage); err == nil {
			var traeCount int
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				wsDir := filepath.Join(traeStorage, e.Name())
				wsFile := filepath.Join(wsDir, "workspace.json")
				if data, err := os.ReadFile(wsFile); err == nil {
					if strings.Contains(strings.ToLower(string(data)), normRoot) || strings.Contains(strings.ToLower(string(data)), rootBase) {
						traeCount++
					}
				}
			}
			if traeCount > 0 {
				counts["trae"] = traeCount
			}
		}
	}
	if counts["trae"] == 0 && dirExists(filepath.Join(root, ".trae")) {
		counts["trae"] = 1
	}

	// 7. Cline / Roo Code
	if appData != "" {
		for _, clineExt := range []string{"saoudrizwan.claude-dev", "rooveterinaryinc.roo-cline"} {
			clineTasks := filepath.Join(appData, "Code", "User", "globalStorage", clineExt, "tasks")
			if entries, err := os.ReadDir(clineTasks); err == nil {
				var taskCount int
				for _, e := range entries {
					if !e.IsDir() {
						continue
					}
					apiHistory := filepath.Join(clineTasks, e.Name(), "api_conversation_history.json")
					uiMessages := filepath.Join(clineTasks, e.Name(), "ui_messages.json")
					matched := false
					for _, checkFile := range []string{apiHistory, uiMessages} {
						if fileExists(checkFile) {
							if f, err := os.Open(checkFile); err == nil {
								buf := make([]byte, 4096)
								n, _ := f.Read(buf)
								_ = f.Close()
								if strings.Contains(strings.ToLower(string(buf[:n])), normRoot) || strings.Contains(strings.ToLower(string(buf[:n])), rootBase) {
									matched = true
									break
								}
							}
						}
					}
					if matched {
						taskCount++
					}
				}
				if taskCount > 0 {
					counts["cline"] += taskCount
				}
			}
		}
	}
	if counts["cline"] == 0 && fileExists(filepath.Join(root, ".clinerules")) {
		counts["cline"] = 1
	}

	// 8. Aider
	aiderHistory := filepath.Join(root, ".aider.chat.history.md")
	if fileExists(aiderHistory) {
		if data, err := os.ReadFile(aiderHistory); err == nil {
			chatMatches := strings.Count(string(data), "# aider chat started at")
			if chatMatches == 0 {
				chatMatches = strings.Count(string(data), "#### ")
			}
			if chatMatches == 0 && len(data) > 0 {
				chatMatches = 1
			}
			counts["aider"] = chatMatches
		}
	}

	// 9. GitHub Copilot & other tools in workspace
	if dirExists(filepath.Join(root, ".copilot")) {
		counts["copilot"] = 1
	}
	if dirExists(filepath.Join(root, ".minimax")) {
		counts["minimax"] = 1
	}
	if dirExists(filepath.Join(root, ".kimi")) || dirExists(filepath.Join(root, ".moonshot")) {
		counts["kimi"] = 1
	}
	if dirExists(filepath.Join(root, ".zcode")) {
		counts["zcode"] = 1
	}
	if dirExists(filepath.Join(root, ".continue")) {
		counts["continue"] = 1
	}
	if dirExists(filepath.Join(root, ".replit")) || fileExists(filepath.Join(root, ".replit")) {
		counts["replit"] = 1
	}
	if dirExists(filepath.Join(root, ".devin")) {
		counts["devin"] = 1
	}
	if dirExists(filepath.Join(root, ".goose")) {
		counts["goose"] = 1
	}
	if dirExists(filepath.Join(root, ".openhands")) {
		counts["openhands"] = 1
	}

	storeSessScan(normRoot, counts, time.Now())

	return counts
}

func storeSessScan(root string, counts map[string]int, scannedAt time.Time) {
	sessScanMu.Lock()
	defer sessScanMu.Unlock()

	cutoff := scannedAt.Add(-sessScanCacheTTL)
	for key, cached := range sessScanCache {
		if cached.scannedAt.Before(cutoff) {
			delete(sessScanCache, key)
		}
	}
	for len(sessScanCache) >= maxSessScanCacheEntries {
		var oldestKey string
		var oldestAt time.Time
		for key, cached := range sessScanCache {
			if oldestAt.IsZero() || cached.scannedAt.Before(oldestAt) {
				oldestKey, oldestAt = key, cached.scannedAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(sessScanCache, oldestKey)
	}
	sessScanCache[root] = cachedSessScan{counts: counts, scannedAt: scannedAt}
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// ignorePatterns returns the effective directory ignore patterns from the
// daemon settings. Falls back to the same defaults UpdateSettings seeds, so
// the language scan stays bounded even before any settings write.
func ignorePatterns() []string {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	if len(globalSettings.IgnorePatterns) > 0 {
		return globalSettings.IgnorePatterns
	}
	return []string{".git", "node_modules", "vendor", "dist", "build", ".cache", "target", ".next"}
}

// alwaysIgnoredDirs are pruned from every recursive walk regardless of
// settings: tooling caches, build outputs, and the data directories of
// WrongTrace/WrongStack themselves must never be scanned (scanning our own
// session store is both slow and meaningless).
var alwaysIgnoredDirs = []string{
	".git", ".temp_files", "temp_files", ".tmp", "tmp",
	"node_modules", "vendor", "dist", "build", "target",
	".next", ".nuxt", ".turbo", ".cache", ".wrongtrace",
	"coverage", "out", ".out", "bin",
	"__pycache__", ".venv", "venv", ".pytest_cache",
	".idea", ".vscode", ".svelte-kit", ".astro",
}

var alwaysIgnoredMap = func() map[string]struct{} {
	m := make(map[string]struct{}, len(alwaysIgnoredDirs))
	for _, d := range alwaysIgnoredDirs {
		m[strings.ToLower(d)] = struct{}{}
	}
	return m
}()

// isIgnoredDir reports whether a directory base name is excluded from all
// recursive walks: the ignore_patterns setting plus alwaysIgnoredDirs.
// Case-insensitive (EqualFold) because Windows roots arrive with mixed
// case from both WrongStack and users. This is the single ignore predicate
// shared by DetectPrimaryLanguage, PrimeDirectory, and any future walk —
// do not inline pattern lists in walkers.
func isIgnoredDir(base string) bool {
	baseLower := lowerASCII(base)
	if _, ok := alwaysIgnoredMap[baseLower]; ok {
		return true
	}
	_, ok := settingsIgnoreSet()[baseLower]
	return ok
}

// lowerASCII lowercases a directory base name without allocating when it is
// already lowercase -- which it is for nearly every directory on disk. The old
// unconditional strings.ToLower ran once per directory per walk, and
// PrimeDirectory plus DetectPrimaryLanguage walk whole workspaces.
func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}

// settingsIgnoreSet returns the configured ignore patterns as a lowercased
// set. isIgnoredDir used to re-lowercase every pattern for every directory it
// judged, allocating once per pattern per directory across whole-workspace
// walks; the set is now built once and reused.
//
// Validity is checked against the live pattern list rather than a change
// counter: settings are written through UpdateSettings, through the settings
// file loader, and directly by tests, and a cache that only tracked one of
// those paths would silently serve a stale ignore list.
var (
	ignoreSetMu    sync.RWMutex
	ignoreSetSrc   []string
	ignoreSetCache map[string]struct{}
)

func settingsIgnoreSet() map[string]struct{} {
	patterns := ignorePatterns()

	ignoreSetMu.RLock()
	if ignoreSetCache != nil && sameStrings(ignoreSetSrc, patterns) {
		set := ignoreSetCache
		ignoreSetMu.RUnlock()
		return set
	}
	ignoreSetMu.RUnlock()

	set := make(map[string]struct{}, len(patterns))
	for _, ig := range patterns {
		set[strings.ToLower(ig)] = struct{}{}
	}
	src := make([]string, len(patterns))
	copy(src, patterns)

	ignoreSetMu.Lock()
	ignoreSetCache = set
	ignoreSetSrc = src
	ignoreSetMu.Unlock()
	return set
}

// sameStrings reports element-wise equality. Comparing a handful of short
// pattern strings is far cheaper than the allocations it replaces.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// DetectPrimaryLanguage infers the predominant language by counting source file extensions.
// Directories whose base name matches an ignore_patterns setting entry are pruned with
// filepath.SkipDir (never descended) — without this, a fat node_modules tree dominated the
// count and made every web project classify as TypeScript while the full walk made
// batch-import pathologically slow.
func DetectPrimaryLanguage(root string) string {
	extCounts := make(map[string]int)
	scannedFiles := 0
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if p == root {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if isIgnoredDir(name) {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(root, p)
			if err == nil && rel != "." && ignoredPathSegment(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		scannedFiles++
		if scannedFiles > 300 {
			return filepath.SkipAll
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		switch ext {
		case ".go":
			extCounts["Go"]++
		case ".ts", ".tsx":
			extCounts["TypeScript"]++
		case ".js", ".jsx":
			extCounts["JavaScript"]++
		case ".py":
			extCounts["Python"]++
		case ".rs":
			extCounts["Rust"]++
		case ".java":
			extCounts["Java"]++
		case ".cpp", ".cc", ".cxx", ".h", ".hpp":
			extCounts["C++"]++
		case ".cs":
			extCounts["C#"]++
		}
		return nil
	})

	var bestLang = "Generic"
	var maxCount = 0
	// Deterministic precedence on tie
	precedence := []string{"Go", "Rust", "TypeScript", "JavaScript", "Python", "Java", "C++", "C#"}
	for _, lang := range precedence {
		if cnt, ok := extCounts[lang]; ok && cnt > maxCount {
			maxCount = cnt
			bestLang = lang
		}
	}
	for lang, cnt := range extCounts {
		if cnt > maxCount {
			maxCount = cnt
			bestLang = lang
		}
	}
	return bestLang
}

// VacuumDB executes SQLite VACUUM optimization on the database.
func (e *Engine) VacuumDB() error {
	st := e.Store()
	if st == nil {
		return nil
	}
	return st.Vacuum()
}

// ClearStale removes telemetry events older than N days across all tables.
func (e *Engine) ClearStale(days int) (int64, error) {
	st := e.Store()
	if st == nil {
		return 0, nil
	}
	return st.ClearStale(days)
}
