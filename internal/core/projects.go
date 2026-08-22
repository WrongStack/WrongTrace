package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	for _, p := range e.projects {
		if p.IsActive {
			cp := p
			return &cp
		}
	}
	// Fallback to first project if none marked active
	for _, p := range e.projects {
		cp := p
		return &cp
	}
	return nil
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

	isFirst := len(e.projects) == 0
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
		IsActive:           isFirst,
	}

	e.lockMu.Lock()
	if e.projects == nil {
		e.projects = make(map[string]ProjectProfile)
	}
	e.projects[id] = proj
	SaveProjectsIndex(e.projects)
	watcher := e.watcher
	e.lockMu.Unlock()

	// Prime atlas directory
	go e.PrimeDirectory(absPath)

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
	defer e.lockMu.Unlock()

	existing, ok := e.projects[p.ID]
	if !ok {
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

	// Re-scan sessions
	existing.DiscoveredSessions = ScanAgentSessions(existing.Path)
	e.projects[p.ID] = existing
	SaveProjectsIndex(e.projects)

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

// ScanAgentSessions inspects workspace and global application directories for coding agent artifacts and logs.
// It deliberately issues only bounded os.ReadDir calls against well-known global
// directories (never a recursive filepath.Walk of the workspace), so it needs
// no ignore-pattern filtering and its cost is independent of tree size.
func ScanAgentSessions(root string) map[string]int {
	counts := make(map[string]int)
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
				targetNorm := strings.ToLower(filepath.Clean(root))
				for _, proj := range pFile.Projects {
					if strings.ToLower(filepath.Clean(proj.Root)) == targetNorm && proj.Slug != "" {
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
						counts["wrongstack"] = sessCnt
						break
					}
				}
			}
		}
		if counts["wrongstack"] == 0 && dirExists(filepath.Join(homeDir, ".wrongstack")) {
			counts["wrongstack"] = 1
		}
	}

	// 2. Antigravity & Gemini CLI
	if homeDir != "" {
		brainDir := filepath.Join(homeDir, ".gemini", "antigravity-cli", "brain")
		if entries, err := os.ReadDir(brainDir); err == nil {
			var brainCount int
			for _, e := range entries {
				if e.IsDir() {
					brainCount++
				}
			}
			if brainCount > 0 {
				counts["antigravity"] = brainCount
			}
		}
	}
	if counts["antigravity"] == 0 && dirExists(filepath.Join(root, ".gemini")) {
		counts["antigravity"] = 1
	}

	// 3. Claude Code
	if homeDir != "" {
		claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
		if entries, err := os.ReadDir(claudeProjectsDir); err == nil {
			var claudeCount int
			for _, e := range entries {
				if e.IsDir() {
					claudeCount++
				}
			}
			if claudeCount > 0 {
				counts["claude_code"] = claudeCount
			}
		}
	}
	if counts["claude_code"] == 0 && (dirExists(filepath.Join(root, ".claude")) || (homeDir != "" && dirExists(filepath.Join(homeDir, ".claude")))) {
		counts["claude_code"] = 1
	}

	// 4. Cursor (Workspace Storage & Global)
	if appData != "" {
		cursorStorage := filepath.Join(appData, "Cursor", "User", "workspaceStorage")
		if entries, err := os.ReadDir(cursorStorage); err == nil {
			counts["cursor"] = len(entries)
		}
	}
	if counts["cursor"] == 0 && (dirExists(filepath.Join(root, ".cursor")) || (homeDir != "" && dirExists(filepath.Join(homeDir, ".cursor")))) {
		counts["cursor"] = 1
	}

	// 5. Windsurf
	if appData != "" {
		windsurfStorage := filepath.Join(appData, "Windsurf", "User", "workspaceStorage")
		if entries, err := os.ReadDir(windsurfStorage); err == nil {
			counts["windsurf"] = len(entries)
		}
	}
	if counts["windsurf"] == 0 && (dirExists(filepath.Join(root, ".windsurf")) || (homeDir != "" && dirExists(filepath.Join(homeDir, ".windsurf")))) {
		counts["windsurf"] = 1
	}

	// 6. Trae (ByteDance)
	if appData != "" {
		traeStorage := filepath.Join(appData, "Trae", "User", "workspaceStorage")
		if entries, err := os.ReadDir(traeStorage); err == nil {
			counts["trae"] = len(entries)
		}
	}
	if counts["trae"] == 0 && (dirExists(filepath.Join(root, ".trae")) || (homeDir != "" && dirExists(filepath.Join(homeDir, ".trae")))) {
		counts["trae"] = 1
	}

	// 7. GitHub Copilot
	if appData != "" {
		copilotChat := filepath.Join(appData, "Code", "User", "globalStorage", "github.copilot-chat")
		if dirExists(copilotChat) {
			counts["copilot"] = 1
		}
	}
	if counts["copilot"] == 0 && (dirExists(filepath.Join(root, ".copilot")) || (homeDir != "" && dirExists(filepath.Join(homeDir, ".copilot")))) {
		counts["copilot"] = 1
	}

	// 8. Cline / Roo Code
	if appData != "" {
		clineTasks := filepath.Join(appData, "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks")
		if entries, err := os.ReadDir(clineTasks); err == nil {
			counts["cline"] = len(entries)
		}
	}
	if counts["cline"] == 0 && fileExists(filepath.Join(root, ".clinerules")) {
		counts["cline"] = 1
	}

	// 9. Aider
	aiderHistory := filepath.Join(root, ".aider.chat.history.md")
	if fileExists(aiderHistory) || (homeDir != "" && fileExists(filepath.Join(homeDir, ".aider.conf.yml"))) {
		counts["aider"] = 1
	}

	// 10. MiniMax Code & Kimi Code & ZCode
	if homeDir != "" {
		if dirExists(filepath.Join(homeDir, ".minimax")) {
			counts["minimax"] = 1
		}
		if dirExists(filepath.Join(homeDir, ".kimi")) || dirExists(filepath.Join(homeDir, ".moonshot")) {
			counts["kimi"] = 1
		}
		if dirExists(filepath.Join(homeDir, ".zcode")) {
			counts["zcode"] = 1
		}
	}

	// 11. Continue.dev & Zed & Replit & Devin & Goose & OpenHands
	if homeDir != "" {
		if dirExists(filepath.Join(homeDir, ".continue")) {
			counts["continue"] = 1
		}
		if dirExists(filepath.Join(homeDir, ".replit")) || fileExists(filepath.Join(root, ".replit")) {
			counts["replit"] = 1
		}
		if dirExists(filepath.Join(homeDir, ".devin")) {
			counts["devin"] = 1
		}
		if dirExists(filepath.Join(homeDir, ".goose")) {
			counts["goose"] = 1
		}
		if dirExists(filepath.Join(homeDir, ".openhands")) {
			counts["openhands"] = 1
		}
	}
	if appData != "" && dirExists(filepath.Join(appData, "Zed")) {
		counts["zed"] = 1
	}

	return counts
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
}

// isIgnoredDir reports whether a directory base name is excluded from all
// recursive walks: the ignore_patterns setting plus alwaysIgnoredDirs.
// Case-insensitive (EqualFold) because Windows roots arrive with mixed
// case from both WrongStack and users. This is the single ignore predicate
// shared by DetectPrimaryLanguage, PrimeDirectory, and any future walk —
// do not inline pattern lists in walkers.
func isIgnoredDir(base string) bool {
	for _, ig := range ignorePatterns() {
		if strings.EqualFold(base, ig) {
			return true
		}
	}
	for _, ig := range alwaysIgnoredDirs {
		if strings.EqualFold(base, ig) {
			return true
		}
	}
	return false
}

// DetectPrimaryLanguage infers the predominant language by counting source file extensions.
// Directories whose base name matches an ignore_patterns setting entry are pruned with
// filepath.SkipDir (never descended) — without this, a fat node_modules tree dominated the
// count and made every web project classify as TypeScript while the full walk made
// batch-import pathologically slow.
func DetectPrimaryLanguage(root string) string {
	extCounts := make(map[string]int)
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			if p == root {
				return nil
			}
			if isIgnoredDir(filepath.Base(p)) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
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
	if e.cfg.Store == nil || e.cfg.Store.DB() == nil {
		return nil
	}
	_, err := e.cfg.Store.DB().Exec("VACUUM")
	return err
}

// ClearStale removes telemetry events older than N days.
func (e *Engine) ClearStale(days int) (int64, error) {
	if e.cfg.Store == nil || e.cfg.Store.DB() == nil {
		return 0, nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.DateTime)
	res, err := e.cfg.Store.DB().Exec("DELETE FROM code_node_events WHERE event_time < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
