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
			e.cfg.Store = newStore
			e.lockMu.Unlock()
		}
	}

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
						return filepath.Join(homeDir, ".wrongstack", "projects", proj.Slug, "sessions")
					}
				}
			}
		}
	}
	return filepath.Join(root, ".wrongstack")
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

// ScanAgentSessions inspects a workspace directory for coding agent artifacts and logs.
func ScanAgentSessions(root string) map[string]int {
	counts := make(map[string]int)

	// 1. Claude Code
	claudeDir := filepath.Join(root, ".claude")
	if info, err := os.Stat(claudeDir); err == nil && info.IsDir() {
		var cnt int
		_ = filepath.Walk(claudeDir, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
				cnt++
			}
			return nil
		})
		counts["claude_code"] = cnt
	}

	// 2. Aider
	aiderHistory := filepath.Join(root, ".aider.chat.history.md")
	if _, err := os.Stat(aiderHistory); err == nil {
		counts["aider"] = 1
	}

	// 3. Cursor
	cursorDir := filepath.Join(root, ".cursor")
	if info, err := os.Stat(cursorDir); err == nil && info.IsDir() {
		counts["cursor"] = 1
	}

	// 4. Cline / Roo Code
	clineRules := filepath.Join(root, ".clinerules")
	if _, err := os.Stat(clineRules); err == nil {
		counts["cline"] = 1
	}

	// 5. Antigravity / Gemini CLI
	geminiDir := filepath.Join(root, ".gemini")
	if info, err := os.Stat(geminiDir); err == nil && info.IsDir() {
		counts["antigravity"] = 1
	}

	// 6. WrongStack Deep Session Discovery
	homeDir, _ := os.UserHomeDir()
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
										// Date folder (e.g. 2026-08-20)
										subEntries, _ := os.ReadDir(filepath.Join(sessionsDir, de.Name()))
										for _, se := range subEntries {
											if se.IsDir() && strings.HasPrefix(se.Name(), "sess_") {
												sessCnt++
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
	}
	if _, ok := counts["wrongstack"]; !ok {
		wsLocal := filepath.Join(root, ".wrongstack")
		if info, err := os.Stat(wsLocal); err == nil && info.IsDir() {
			counts["wrongstack"] = 1
		}
	}

	return counts
}

// DetectPrimaryLanguage infers the predominant language by counting source file extensions.
func DetectPrimaryLanguage(root string) string {
	extCounts := make(map[string]int)
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
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

