package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
	"github.com/wrongstack/wrongtrace/internal/mcp"
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

func init() {
	rootCmd.AddCommand(startCmd, mcpCmd, statusCmd)

	startCmd.Flags().StringP("watch", "w", ".", "directory to observe")
	startCmd.Flags().IntP("port", "p", 4318, "HTTP port for the embedded dashboard")
	startCmd.Flags().String("db", filepath.Join(defaultDataDir(), "wrongtrace.db"), "SQLite database file")
	startCmd.Flags().String("socket", defaultSocketPath(), "Unix Domain Socket / Named Pipe path")
	startCmd.Flags().String("repo", filepath.Base(mustCwd()), "repository name to record events under")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runStart(cmd *cobra.Command, _ []string) error {
	watchDir, _ := cmd.Flags().GetString("watch")
	port, _ := cmd.Flags().GetInt("port")
	dbPath, _ := cmd.Flags().GetString("db")
	socketPath, _ := cmd.Flags().GetString("socket")
	repoName, _ := cmd.Flags().GetString("repo")

	abs, err := filepath.Abs(watchDir)
	if err != nil {
		return fmt.Errorf("resolve watch dir: %w", err)
	}

	log.Printf("wrongtrace %s starting", version)
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

	// Filesystem watcher with debouncing + ignore rules.
	w, err := watcher.New(watcher.Config{
		Dir:    abs,
		Engine: engine,
	})
	if err != nil {
		return fmt.Errorf("init watcher: %w", err)
	}
	defer w.Close()

	// IPC listener: Unix Domain Socket on POSIX, Named Pipe on Windows.
	// A bind failure (pipe held by another instance, ACL denial) is logged
	// but not fatal: HTTP + watching still work, and agents fall back to MCP.
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
		Port:   port,
		Engine: engine,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := httpServer.Start(); err != nil {
			log.Printf("http server: %v", err)
			cancel()
		}
	}()
	go w.Run(ctx)
	go engine.Run(ctx)

	// Graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	case <-ctx.Done():
	}

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = shutdownCtx
	log.Printf("wrongtrace stopped")
	return nil
}

func runMCP(cmd *cobra.Command, _ []string) error {
	dbPath, _ := startCmd.Flags().GetString("db")
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
	dbPath, _ := startCmd.Flags().GetString("db")
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
