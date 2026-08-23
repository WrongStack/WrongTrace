package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ingest"
	"github.com/wrongstack/wrongtrace/internal/ipc"
	"github.com/wrongstack/wrongtrace/internal/lock"
	"github.com/wrongstack/wrongtrace/internal/mcp"
	"github.com/wrongstack/wrongtrace/internal/report"
	"github.com/wrongstack/wrongtrace/internal/server"
	"github.com/wrongstack/wrongtrace/internal/watcher"
)

// version is overridden via -ldflags at release build time.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "wrongtrace",
	Short: "AI-native code churn & agent observability daemon",
	Long: `WrongTrace is a single-binary telemetry daemon that tracks the real-time
lifecycle of code nodes (functions, classes, methods) and correlates them with
agent telemetry to expose churn, thrashing, model survival, and token ROI.`,
	Version: version,
	RunE:    runStart,
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the WrongTrace observer daemon, IPC socket, and embedded dashboard",
	RunE:  runStart,
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run WrongTrace as a Model Context Protocol server over stdio",
	RunE:  runMCP,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print a short status summary (DB, recent events)",
	RunE:  runStatus,
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run comprehensive diagnostics on database, IPC, agent logs, and AST parsers",
	RunE:  runDoctor,
}

var traceCmd = &cobra.Command{
	Use:   "trace -- <command...>",
	Short: "Execute any command/test, measure runtime latency, and record profiler telemetry",
	RunE:  runTrace,
}

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export telemetry and code churn records to JSON",
	RunE:  runExport,
}

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate an executive Markdown, HTML, or JSON observability report",
	RunE:  runReport,
}

var hookCmd = &cobra.Command{
	Use:   "hook [install|uninstall]",
	Short: "Install or remove git hooks for telemetry and code churn tracking",
	Args:  cobra.ExactArgs(1),
	RunE:  runHook,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize WrongTrace observability configs, agent rules (CLAUDE.md, AGENTS.md), and git hooks",
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(startCmd, mcpCmd, statusCmd, doctorCmd, traceCmd, exportCmd, reportCmd, hookCmd, initCmd)

	rootCmd.PersistentFlags().StringP("watch", "w", ".", "directory to observe")
	rootCmd.PersistentFlags().IntP("port", "p", 4318, "HTTP port for the embedded dashboard")
	rootCmd.PersistentFlags().String("db", filepath.Join(defaultDataDir(), "wrongtrace.db"), "SQLite database file")
	rootCmd.PersistentFlags().String("socket", defaultSocketPath(), "Unix Domain Socket / Named Pipe path")
	rootCmd.PersistentFlags().String("repo", filepath.Base(mustCwd()), "repository name to record events under")

	traceCmd.Flags().String("service", "cli-command", "service name for the trace")
	traceCmd.Flags().String("node", "", "optional AST node or function signature to correlate")
	exportCmd.Flags().String("out", "", "output file path (default stdout)")

	reportCmd.Flags().String("format", "markdown", "report format: markdown, html, or json")
	reportCmd.Flags().String("out", "", "output file path (default stdout)")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runStart(cmd *cobra.Command, _ []string) error {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("FATAL PANIC in runStart: %v\n%s", r, debug.Stack())
		}
	}()

	// Ensure userhome .wrongtrace directory exists and write daemon logs there
	dataDir := defaultDataDir()
	_ = os.MkdirAll(dataDir, 0o755)
	logFilePath := filepath.Join(dataDir, "daemon.log")
	if logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
		defer logFile.Close()
	}

	// Soft memory limit to keep Go GC and virtual memory footprint lean (< 256MB)
	debug.SetMemoryLimit(256 * 1024 * 1024)

	watchDir, _ := cmd.Flags().GetString("watch")
	port, _ := cmd.Flags().GetInt("port")
	dbPath, _ := cmd.Flags().GetString("db")
	socketPath, _ := cmd.Flags().GetString("socket")
	repoName, _ := cmd.Flags().GetString("repo")

	// Enforce single instance: prevent 2nd copy from running concurrently
	instanceLock, err := lock.Acquire(dataDir, port)
	if err != nil {
		if errors.Is(err, lock.ErrAlreadyRunning) {
			log.Printf("daemon: %v — refusing duplicate execution", err)
			fmt.Fprintf(os.Stderr, "⚠ %v\n", err)
			return nil
		}
		log.Printf("daemon: lock warning: %v", err)
	} else {
		defer instanceLock.Release()
	}

	abs, err := filepath.Abs(watchDir)
	if err != nil {
		return fmt.Errorf("resolve watch dir: %w", err)
	}

	log.Printf("wrongtrace %s starting (resilient daemon mode)", version)
	log.Printf("  watch  : %s", abs)
	log.Printf("  port   : %d", port)
	log.Printf("  db     : %s", dbPath)
	log.Printf("  socket : %s", socketPath)
	log.Printf("  repo   : %s", repoName)

	// Storage layer.
	store, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	if err := store.Migrate(); err != nil {
		return fmt.Errorf("migrate db: %w", err)
	}

	// AST engine (Tree-sitter multi-language parser + diff).
	astEngine, err := ast.NewEngine()
	if err != nil {
		return fmt.Errorf("init ast engine: %w", err)
	}
	defer astEngine.Close()

	// Correlation + metrics engine: coordinates filesystem events,
	// agent telemetry, and DB persistence.
	engine := core.NewEngine(core.Config{
		RepoName: repoName,
		Store:    store,
		AST:      astEngine,
	})
	engine.PrimeDirectory(abs)

	// Filesystem watcher with debouncing + ignore rules.
	w, err := watcher.New(watcher.Config{
		Dir:    abs,
		Engine: engine,
	})
	if err != nil {
		return fmt.Errorf("init watcher: %w", err)
	}
	defer w.Close()
	engine.SetWatcher(w)

	// IPC listener: Unix Domain Socket on POSIX, Named Pipe on Windows.
	ipcServer := ipc.NewServer(ipc.Config{
		SocketPath: socketPath,
		Engine:     engine,
	})
	if err := ipcServer.Start(); err != nil {
		log.Printf("ipc: disabled (%v) — HTTP API and watching continue", err)
		ipcServer = nil
	} else {
		defer ipcServer.Stop()
	}

	// Embedded HTTP server + WebSocket hub.
	httpServer := server.New(server.Config{
		Port:       port,
		Engine:     engine,
		SocketPath: socketPath,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Automatic Session Log & Tool Call Ingestor.
	sessionWatcher := ingest.NewSessionWatcher(func(ev ingest.ToolCallEvent) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("sessionWatcher ingest recover: %v", r)
			}
		}()
		projectID := ""
		projectSlug := ""
		if ev.TargetFile != "" {
			if proj, ok := engine.FindProjectForFile(ev.TargetFile); ok {
				projectID = proj.ID
				projectSlug = proj.Name
			}
		}
		_ = engine.ReportRun(ipc.TelemetryReport{
			RunID:            ev.SessionID,
			TaskID:           ev.ToolName,
			ProjectID:        projectID,
			ProjectSlug:      projectSlug,
			AgentName:        ev.AgentName,
			ModelName:        ev.ModelName,
			Provider:         ev.Provider,
			PromptTokens:     ev.PromptTokens,
			CompletionTokens: ev.CompletionTokens,
			CostUSD:          ev.CostUSD,
			Intent:           ev.Intent,
		})
	})
	sessionWatcher.SetOnReadEvent(func(rev ingest.FileReadEvent) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("sessionWatcher read recover: %v", r)
			}
		}()
		_ = engine.RecordReadEvent(db.FileReadRecord{
			ReadID:         rev.ReadID,
			SessionID:      rev.SessionID,
			RunID:          rev.RunID,
			RepoName:       rev.RepoName,
			FilePath:       rev.FilePath,
			AgentName:      rev.AgentName,
			ModelName:      rev.ModelName,
			Provider:       rev.Provider,
			ToolName:       rev.ToolName,
			StartLine:      rev.StartLine,
			EndLine:        rev.EndLine,
			LinesReadCount: rev.LinesReadCount,
			PromptTokens:   rev.PromptTokens,
			CachedTokens:   rev.CachedTokens,
			CostUSD:        rev.CostUSD,
			Intent:         rev.Intent,
			ReadTime:       rev.OccurredAt,
		})
	})
	sessionWatcher.DiscoverAgentDirs(abs)
	sessionWatcher.DiscoverGlobalAgentDirs()
	sessionWatcher.StartPolling(ctx, 3*time.Second)

	// Resilient HTTP Server Loop: never drops daemon on temporary listener issues
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("PANIC in httpServer: %v\n%s (recovering in 2s)", r, debug.Stack())
						time.Sleep(2 * time.Second)
					}
				}()
				if err := httpServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Printf("http server warning: %v (restarting in 2s)", err)
					time.Sleep(2 * time.Second)
				}
			}()
		}
	}()

	// Resilient Watcher Loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("PANIC in watcher.Run: %v\n%s (recovering in 2s)", r, debug.Stack())
						time.Sleep(2 * time.Second)
					}
				}()
				w.Run(ctx)
			}()
		}
	}()

	// Resilient Core Engine Loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("PANIC in engine.Run: %v\n%s (recovering in 2s)", r, debug.Stack())
						time.Sleep(2 * time.Second)
					}
				}()
				engine.Run(ctx)
			}()
		}
	}()

	// Model catalog background sync
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in SyncModelsDev: %v\n%s", r, debug.Stack())
			}
		}()
		if n, err := engine.SyncModelsDev(); err != nil {
			log.Printf("models.dev sync: %v — using catalog already in memory", err)
		} else {
			log.Printf("models.dev sync: %d models loaded", n)
		}
	}()

	// Periodic memory recycler: returns unused pages back to the OS kernel
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				debug.FreeOSMemory()
			}
		}
	}()

	// Graceful shutdown ONLY on explicit user interruption (SIGINT, SIGTERM, Ctrl+C)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received signal %s (%T), shutting down daemon", sig, sig)

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}
	log.Printf("wrongtrace stopped gracefully")
	return nil
}

func runMCP(cmd *cobra.Command, _ []string) error {
	dbPath, _ := cmd.Flags().GetString("db")
	if dbPath == "" {
		dbPath = filepath.Join(defaultDataDir(), "wrongtrace.db")
	}
	store, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("migrate db: %w", err)
	}
	engine := core.NewEngine(core.Config{
		RepoName: "mcp",
		Store:    store,
		AST:      nil, // MCP mode never parses files; only reports/queries telemetry.
	})
	return mcp.ServeStdio(engine)
}

func runStatus(cmd *cobra.Command, _ []string) error {
	dbPath, _ := cmd.Flags().GetString("db")
	store, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("migrate db: %w", err)
	}
	stats, err := store.Overview()
	if err != nil {
		return fmt.Errorf("read overview: %w", err)
	}
	fmt.Printf("WrongTrace %s\n", version)
	fmt.Printf("  database       : %s\n", dbPath)
	fmt.Printf("  total runs     : %d\n", stats.TotalRuns)
	fmt.Printf("  total events   : %d\n", stats.TotalEvents)
	fmt.Printf("  total spend USD: %.4f\n", stats.TotalCost)
	fmt.Printf("  unique models  : %d\n", stats.UniqueModels)
	return nil
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	dbPath, _ := cmd.Flags().GetString("db")
	socketPath, _ := cmd.Flags().GetString("socket")
	watchDir, _ := cmd.Flags().GetString("watch")
	absWatch, _ := filepath.Abs(watchDir)

	fmt.Printf("=== WrongTrace Diagnostics (%s) ===\n", version)
	fmt.Printf("OS / Arch      : %s / %s (CPUs: %d)\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	fmt.Printf("Workspace      : %s\n", absWatch)

	// 1. Storage Check
	fmt.Print("\n[1] SQLite Database: ")
	store, err := db.Open(dbPath)
	if err != nil {
		fmt.Printf("FAIL (%v)\n", err)
	} else {
		defer store.Close()
		if err := store.Migrate(); err != nil {
			fmt.Printf("FAIL MIGRATION (%v)\n", err)
		} else {
			overview, _ := store.Overview()
			profOverview, _ := store.ProfilerOverview()
			fmt.Printf("OK (Runs: %d, Events: %d, Traces: %d)\n",
				overview.TotalRuns, overview.TotalEvents, profOverview.TotalTraces)
		}
	}

	// 2. Multi-language AST Engine Check
	fmt.Print("[2] AST Engine (Tree-sitter & Multi-lang): ")
	astEng, err := ast.NewEngine()
	if err != nil {
		fmt.Printf("FAIL (%v)\n", err)
	} else {
		defer astEng.Close()
		testSnap, err := astEng.Parse("doctor_test.go", []byte("package main\nfunc Test() {}"))
		if err != nil || len(testSnap.Nodes) == 0 {
			fmt.Printf("WARN (parsed 0 nodes)\n")
		} else {
			fmt.Printf("OK (Languages supported: Go, TS/JS, Python, Rust, C/C++, Java, C#, PHP, Ruby)\n")
		}
	}

	// 3. IPC / Named Pipe Path
	fmt.Printf("[3] IPC Path: %s\n", socketPath)

	// 4. Discovered Coding Agent Logs
	fmt.Println("\n[4] Coding Agent Logs & Transcripts Discovery:")
	sw := ingest.NewSessionWatcher(nil)
	sw.DiscoverAgentDirs(absWatch)
	sw.DiscoverGlobalAgentDirs()

	home, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = filepath.Join(home, "AppData", "Local")
	}

	checkAgents := []struct {
		Name  string
		Paths []string
	}{
		{"WrongStack (Native)", []string{filepath.Join(home, ".wrongstack")}},
		{"Antigravity / Gemini", []string{filepath.Join(home, ".gemini", "antigravity-cli", "brain"), filepath.Join(home, ".gemini")}},
		{"Claude Code", []string{filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".claude", "logs"), filepath.Join(home, ".claude")}},
		{"Cursor", []string{filepath.Join(appData, "Cursor", "User", "workspaceStorage"), filepath.Join(appData, "Cursor"), filepath.Join(home, ".cursor")}},
		{"Windsurf / Codeium", []string{filepath.Join(appData, "Windsurf", "User", "workspaceStorage"), filepath.Join(appData, "Windsurf"), filepath.Join(home, ".codeium", "windsurf")}},
		{"Trae (ByteDance)", []string{filepath.Join(appData, "Trae", "User", "workspaceStorage"), filepath.Join(appData, "Trae"), filepath.Join(home, ".trae")}},
		{"GitHub Copilot", []string{filepath.Join(appData, "Code", "User", "globalStorage", "github.copilot-chat"), filepath.Join(localAppData, "github-copilot"), filepath.Join(home, ".copilot")}},
		{"Cline / Roo Code", []string{
			filepath.Join(appData, "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
			filepath.Join(appData, "Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "tasks"),
			filepath.Join(appData, "Cursor", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
			filepath.Join(home, ".config", "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks"),
		}},
		{"MiniMax Code", []string{filepath.Join(home, ".minimax", "sessions"), filepath.Join(home, ".minimax")}},
		{"Kimi Code (Moonshot)", []string{filepath.Join(home, ".kimi", "sessions"), filepath.Join(home, ".kimi"), filepath.Join(home, ".moonshot")}},
		{"Continue.dev", []string{filepath.Join(home, ".continue", "sessions"), filepath.Join(home, ".continue")}},
		{"Zed AI", []string{filepath.Join(appData, "Zed", "conversations"), filepath.Join(home, ".config", "zed", "conversations")}},
		{"Replit Agent", []string{filepath.Join(home, ".replit", "agent"), filepath.Join(home, ".replit")}},
		{"ZCode (Z.ai)", []string{filepath.Join(home, ".zcode", "tasks"), filepath.Join(home, ".zcode")}},
		{"Devin (Cognition)", []string{filepath.Join(home, ".devin", "sessions"), filepath.Join(home, ".devin")}},
		{"Goose AI Agent", []string{filepath.Join(home, ".goose", "sessions"), filepath.Join(home, ".goose"), filepath.Join(localAppData, "goose")}},
		{"OpenHands (OpenDevin)", []string{filepath.Join(home, ".openhands", "conversations"), filepath.Join(home, ".openhands")}},
		{"Aider (Workspace)", []string{filepath.Join(absWatch, ".aider.chat.history.md"), filepath.Join(home, ".aider.conf.yml")}},
	}

	for _, a := range checkAgents {
		detectedPath := ""
		for _, p := range a.Paths {
			if _, err := os.Stat(p); err == nil {
				detectedPath = p
				break
			}
		}
		if detectedPath != "" {
			fmt.Printf("  ✓ %-24s: Detected (%s)\n", a.Name, detectedPath)
		} else {
			fmt.Printf("  - %-24s: Not found (or standard path inactive)\n", a.Name)
		}
	}

	fmt.Println("\n✓ Diagnostics complete. WrongTrace is operational.")
	return nil
}

func runTrace(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("trace requires a command to execute (e.g. wrongtrace trace -- go test ./...)")
	}

	service, _ := cmd.Flags().GetString("service")
	nodeSig, _ := cmd.Flags().GetString("node")
	dbPath, _ := cmd.Flags().GetString("db")

	startTime := time.Now()
	execCmd := exec.Command(args[0], args[1:]...)
	execCmd.Stdin = os.Stdin
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr

	fmt.Printf("⏱  [WrongTrace] Profiling command: %s\n", strings.Join(args, " "))
	runErr := execCmd.Run()
	duration := time.Since(startTime)
	durationMs := float64(duration.Microseconds()) / 1000.0

	statusCode := 200
	var errMsg string
	if runErr != nil {
		statusCode = 500
		errMsg = runErr.Error()
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			statusCode = exitErr.ExitCode()
		}
	}

	if nodeSig == "" {
		nodeSig = fmt.Sprintf("exec:%s", args[0])
	}

	// 1. Try sending trace to active daemon via HTTP API
	port, _ := cmd.Flags().GetInt("port")
	if port <= 0 {
		port = 4318
	}
	daemonURL := fmt.Sprintf("http://localhost:%d/api/profiler/ingest", port)
	payload := map[string]interface{}{
		"service_name":   service,
		"node_signature": nodeSig,
		"duration_ms":    durationMs,
		"status_code":    statusCode,
		"error_msg":      errMsg,
		"profiler_type":  "test_runner",
		"metadata":       map[string]interface{}{"command": strings.Join(args, " ")},
	}
	payloadBytes, _ := json.Marshal(payload)

	sentToDaemon := false
	httpClient := &http.Client{Timeout: 800 * time.Millisecond}
	if resp, err := httpClient.Post(daemonURL, "application/json", bytes.NewReader(payloadBytes)); err == nil {
		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
			sentToDaemon = true
		}
		_ = resp.Body.Close()
	}

	// 2. If daemon is offline, persist directly to local SQLite database
	if !sentToDaemon {
		store, err := db.Open(dbPath)
		if err == nil {
			defer store.Close()
			_ = store.Migrate()
			_ = store.InsertTrace(db.RuntimeTraceRecord{
				TraceID:       fmt.Sprintf("tr-exec-%d", time.Now().UnixNano()),
				ServiceName:   service,
				NodeSignature: nodeSig,
				DurationMs:    durationMs,
				StatusCode:    statusCode,
				ErrorMsg:      errMsg,
				ProfilerType:  "test_runner",
				MetadataJSON:  fmt.Sprintf(`{"command":%q}`, strings.Join(args, " ")),
				Timestamp:     startTime.UTC(),
			})
		}
	}

	fmt.Printf("\n📊 [WrongTrace] Captured Execution Trace: duration=%.2fms status=%d node=%s\n",
		durationMs, statusCode, nodeSig)

	return runErr
}

func runExport(cmd *cobra.Command, _ []string) error {
	dbPath, _ := cmd.Flags().GetString("db")
	outFile, _ := cmd.Flags().GetString("out")

	store, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	overview, err := store.Overview()
	if err != nil {
		return fmt.Errorf("overview: %w", err)
	}

	events, _ := store.RecentEvents(1000)
	traces, _ := store.RecentTraces(1000)
	models, _ := store.ModelComparison()

	exportData := map[string]interface{}{
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"overview":       overview,
		"models":         models,
		"recent_events":  events,
		"runtime_traces": traces,
	}

	b, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal export: %w", err)
	}

	if outFile != "" {
		if err := os.WriteFile(outFile, b, 0644); err != nil {
			return fmt.Errorf("write export file: %w", err)
		}
		fmt.Printf("Exported telemetry to %s (%d bytes)\n", outFile, len(b))
	} else {
		fmt.Println(string(b))
	}

	return nil
}

func runReport(cmd *cobra.Command, _ []string) error {
	dbPath, _ := cmd.Flags().GetString("db")
	outFile, _ := cmd.Flags().GetString("out")
	format, _ := cmd.Flags().GetString("format")
	repoName, _ := cmd.Flags().GetString("repo")

	store, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	overview, err := store.Overview()
	if err != nil {
		return fmt.Errorf("overview: %w", err)
	}

	events, _ := store.RecentEvents(20)
	thrashing, _ := store.Thrashing(3, 7)
	models, _ := store.ModelComparison()
	profOverview, _ := store.ProfilerOverview()
	hotspots, _ := store.ProfilerHotspots(10)

	data := report.ReportData{
		Snapshot: core.MetricsSnapshot{
			Repo:         repoName,
			GeneratedAt:  time.Now().UTC(),
			Overview:     overview,
			Thrashing:    thrashing,
			Models:       models,
			RecentEvents: events,
		},
		ProfilerOverview: profOverview,
		Hotspots:         hotspots,
	}

	var outputContent string
	switch strings.ToLower(format) {
	case "html":
		outputContent = report.GenerateHTMLReport(data)
	case "json":
		outputContent, err = report.GenerateJSONReport(data)
		if err != nil {
			return fmt.Errorf("generate json report: %w", err)
		}
	default:
		outputContent = report.GenerateMarkdownReport(data)
	}

	if outFile != "" {
		if err := os.WriteFile(outFile, []byte(outputContent), 0644); err != nil {
			return fmt.Errorf("write report file: %w", err)
		}
		fmt.Printf("Generated %s report at %s (%d bytes)\n", format, outFile, len(outputContent))
	} else {
		fmt.Println(outputContent)
	}

	return nil
}

func runInit(cmd *cobra.Command, _ []string) error {
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}

	fmt.Printf("🚀 Initializing WrongTrace AI Observability in %s\n\n", dir)

	// 1. Generate .mcp.json
	mcpJSON := `{
  "mcpServers": {
    "wrongtrace": {
      "command": "wrongtrace",
      "args": ["mcp"]
    }
  }
}
`
	mcpPath := filepath.Join(dir, ".mcp.json")
	if !fileExists(mcpPath) {
		_ = os.WriteFile(mcpPath, []byte(mcpJSON), 0644)
		fmt.Printf("  ✓ Created %s (MCP server registration for Claude Code, Cursor, Windsurf)\n", filepath.Base(mcpPath))
	} else {
		fmt.Printf("  - %s already exists\n", filepath.Base(mcpPath))
	}

	// 2. Generate CLAUDE.md
	claudeMD := `# WrongTrace AI Observability Instructions for Claude Code

When working in this repository:
1. **Report Telemetry**: If available, report run intent and tokens using the ` + "`wrongtrace`" + ` MCP tools or IPC.
2. **File Health Guardrail**: Before modifying fragile or unfamiliar files, call ` + "`check_guardrail(file_path)`" + ` or ` + "`get_file_health_score(file_path)`" + `.
3. **Minimize Churn**: Avoid repeated whole-file rewrites. Use targeted semantic edits (` + "`replace_file_content`" + `) to prevent thrashing.
`
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if !fileExists(claudePath) {
		_ = os.WriteFile(claudePath, []byte(claudeMD), 0644)
		fmt.Printf("  ✓ Created %s (Claude Code agent instructions)\n", filepath.Base(claudePath))
	} else {
		fmt.Printf("  - %s already exists\n", filepath.Base(claudePath))
	}

	// 3. Generate AGENTS.md
	agentsMD := `# Universal AI Coding Agent Guidelines (WrongTrace)

This repository is monitored by **WrongTrace AI Observability**.

## Standard Protocols for All AI Agents (WrongStack, Cursor, Devin, Windsurf, Antigravity, Cline, Aider, Replit Agent, Zed AI, Kimi, MiniMax, ZCode, Trae, Goose, OpenHands, Copilot):
- **Pre-flight Check**: Check file fragility before heavy refactoring. If a file health score is below 40%, review recent thrash events.
- **Precision Diffs**: Prefer minimal localized AST modifications over wholesale overwrites.
- **Safety First**: Do not modify files locked by guardrails.
- **Telemetry Verification**: WrongTrace tracks token expenditure and code survival rates per model. Write clean, durable code.
`
	agentsPath := filepath.Join(dir, "AGENTS.md")
	if !fileExists(agentsPath) {
		_ = os.WriteFile(agentsPath, []byte(agentsMD), 0644)
		fmt.Printf("  ✓ Created %s (Universal instructions for all coding agents)\n", filepath.Base(agentsPath))
	} else {
		fmt.Printf("  - %s already exists\n", filepath.Base(agentsPath))
	}

	// 4. Generate .cursorrules
	cursorRules := `# WrongTrace Rules for Cursor
- Always check if the target file has high churn before making large refactors.
- Prefer targeted semantic diffs to preserve code longevity and survival score.
- Run ` + "`wrongtrace doctor`" + ` to check local telemetry health.
`
	cursorRulesPath := filepath.Join(dir, ".cursorrules")
	if !fileExists(cursorRulesPath) {
		_ = os.WriteFile(cursorRulesPath, []byte(cursorRules), 0644)
		fmt.Printf("  ✓ Created %s (Cursor IDE rules)\n", filepath.Base(cursorRulesPath))
	} else {
		fmt.Printf("  - %s already exists\n", filepath.Base(cursorRulesPath))
	}

	// 5. Install Git Hooks if repository exists
	if err := runHook(cmd, []string{"install"}); err == nil {
		fmt.Println("  ✓ Configured Git post-commit telemetry hook")
	}

	fmt.Println("\n✨ Setup complete! WrongTrace is now ready to observe and guide all coding agents.")
	return nil
}

func runHook(cmd *cobra.Command, args []string) error {
	action := strings.ToLower(args[0])

	// Find .git directory starting from cwd and walking up
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	var gitRoot string
	for {
		candidate := filepath.Join(dir, ".git")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			gitRoot = candidate
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if gitRoot == "" {
		return fmt.Errorf("current directory is not a git repository (.git folder not found)")
	}
	gitHooksDir := filepath.Join(gitRoot, "hooks")
	_ = os.MkdirAll(gitHooksDir, 0755)
	hookFile := filepath.Join(gitHooksDir, "post-commit")

	switch action {
	case "install":
		hookScript := `#!/bin/sh
# WrongTrace automatic post-commit telemetry ping
if command -v wrongtrace >/dev/null 2>&1; then
  wrongtrace status >/dev/null 2>&1 &
fi
`
		if err := os.WriteFile(hookFile, []byte(hookScript), 0755); err != nil {
			return fmt.Errorf("write git hook: %w", err)
		}
		fmt.Printf("✓ Installed WrongTrace post-commit hook at %s\n", hookFile)
	case "uninstall":
		if err := os.Remove(hookFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove git hook: %w", err)
		}
		fmt.Printf("✓ Removed WrongTrace post-commit hook\n")
	default:
		return fmt.Errorf("unknown hook action: %s (supported: install, uninstall)", action)
	}
	return nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func mustCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

func defaultDataDir() string {
	if dir := os.Getenv("WRONGTRACE_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".wrongtrace")
}

func defaultSocketPath() string {
	if p := os.Getenv("WRONGTRACE_SOCKET"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		// go-winio requires the \\.\pipe\ prefix; plain file paths are invalid.
		return `\\.\pipe\wrongtrace`
	}
	return filepath.Join(defaultDataDir(), "wrongtrace.sock")
}
