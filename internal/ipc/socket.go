package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// IPCTrafficRecord captures an individual IPC JSON-RPC request and response pair.
type IPCTrafficRecord struct {
	ID         string                 `json:"id"`
	Method     string                 `json:"method"`
	Params     map[string]interface{} `json:"params"`
	Result     interface{}            `json:"result,omitempty"`
	Error      *RPCError              `json:"error,omitempty"`
	DurationMs float64                `json:"duration_ms"`
	Timestamp  time.Time              `json:"timestamp"`
	ClientAddr string                 `json:"client_addr,omitempty"`
	// WireBytes/RespBytes are the on-wire sizes of the request line and the
	// marshaled response. They let the retention layer prove params/result
	// fit the stored cap without re-encoding them. Not serialized.
	WireBytes int `json:"-"`
	RespBytes int `json:"-"`
}

// EngineSink is the subset of the core Engine used by the IPC server. Keeping
// it as an interface lets us test the IPC layer without spinning up the full
// engine, and avoids an import cycle (ipc -> core -> ipc).
type EngineSink interface {
	ReportRun(r TelemetryReport) error
	RecordReadEvent(rec db.FileReadRecord) error
	FileHealth(p string) (FileHealthReply, error)
	CheckGuardrail(p string) (GuardrailResult, error)
	// TryLockFile must refuse (returning a *LockConflictError) when a
	// different owner already holds an unexpired lock; force=false always
	// on this agent-facing surface.
	TryLockFile(path, reason, owner, ownerRunID string, ttl time.Duration, force bool) (LockInfo, error)
	UnlockFile(path, ownerRunID string) error
	ListLocks() []LockInfo
	GetFileReadStats(filePath string) (db.FileReadStats, error)
	GetRecentFileEvents(filePath string, limit int) ([]db.EventRecord, error)
	Ping() error
	RecordIPCTraffic(rec IPCTrafficRecord)
}

// FileHealthReply is the JSON-serializable view of db.FileHealth. The ipc
// package re-declares it so the protocol is decoupled from the storage type.
type FileHealthReply struct {
	FilePath             string     `json:"file_path"`
	HealthScore          int        `json:"health_score"`
	IsFragile            bool       `json:"is_fragile"`
	RecentThrashingCount int        `json:"recent_thrashing_count"`
	Warning              string     `json:"warning"`
	IsLocked             bool       `json:"is_locked"`
	LockReason           string     `json:"lock_reason,omitempty"`
	LockOwner            string     `json:"lock_owner,omitempty"`
	LockOwnerRunID       string     `json:"lock_owner_run_id,omitempty"`
	LockExpiresAt        *time.Time `json:"lock_expires_at,omitempty"`
}

// Config configures the IPC server.
type Config struct {
	SocketPath string
	Engine     EngineSink
	Version    string
	// MaxConns caps concurrently served connections; extra connections are
	// closed immediately. Zero selects DefaultMaxConns.
	MaxConns int
	// IdleTimeout closes a connection that sends nothing for this long. It is
	// re-armed before every read, so a persistent client that keeps talking
	// is never cut off. The same duration bounds each response write, so a
	// client that stops reading cannot pin the handler goroutine. Zero
	// selects DefaultIdleTimeout.
	IdleTimeout time.Duration
}

const (
	// DefaultMaxConns bounds goroutines and 128 KiB of per-connection buffers
	// a local process can pin by opening sockets and never closing them.
	DefaultMaxConns = 256
	// DefaultIdleTimeout reaps connections left open with no traffic.
	DefaultIdleTimeout = 10 * time.Minute
)

// Server is the agent-facing IPC endpoint.
type Server struct {
	cfg       Config
	ln        net.Listener
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	connsMu   sync.Mutex
	conns     map[net.Conn]struct{}
	connected atomic.Int64
	rejected  atomic.Int64
	sem       chan struct{}
	startedAt time.Time
	// boundPath is the socket file actually bound (differs from SocketPath
	// after the POSIX long-path fallback); linkPath is the discovery symlink
	// this server created, if any. Both are removed by Stop.
	boundPath string
	linkPath  string
}

// NewServer returns a Server ready to be started with Start.
func NewServer(cfg Config) *Server {
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = DefaultMaxConns
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	return &Server{
		cfg:       cfg,
		conns:     make(map[net.Conn]struct{}),
		sem:       make(chan struct{}, cfg.MaxConns),
		startedAt: time.Now(),
	}
}

// Start binds the socket and begins accepting connections.
func (s *Server) Start() error {
	if s.cfg.Engine == nil {
		return errors.New("ipc: engine sink is required")
	}
	if s.cfg.SocketPath == "" {
		return errors.New("ipc: socket path is required")
	}

	ln, bound, link, err := bindSocketPaths(s.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("bind socket: %w", err)
	}
	s.ln = ln
	s.boundPath, s.linkPath = bound, link

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	s.wg.Add(1)
	go s.acceptLoop(ctx)

	log.Printf("ipc: listening on %s (%s)", s.cfg.SocketPath, runtime.GOOS)
	return nil
}

// Stop tears down the listener and waits for in-flight handlers.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.connsMu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.connsMu.Unlock()
	s.wg.Wait()
	if runtime.GOOS == "windows" {
		return
	}
	// Drop the discovery symlink only while it still points at our socket: a
	// newer daemon (or another user) may have replaced it since.
	if s.linkPath != "" {
		if dest, err := os.Readlink(s.linkPath); err == nil && dest == s.boundPath {
			if err := os.Remove(s.linkPath); err != nil && !os.IsNotExist(err) {
				log.Printf("ipc: remove discovery symlink %s: %v", s.linkPath, err)
			}
		}
		s.linkPath = ""
	}
	// Only the path actually bound: after the long-path fallback the
	// configured SocketPath was never created by us.
	if s.boundPath != "" {
		if err := os.Remove(s.boundPath); err != nil && !os.IsNotExist(err) {
			log.Printf("ipc: remove socket %s: %v", s.boundPath, err)
		}
	}
}

// ConnectedCount reports the current number of live agent connections.
func (s *Server) ConnectedCount() int { return int(s.connected.Load()) }

// acceptLoop pumps accepted connections through the per-conn handler.
func (s *Server) acceptLoop(ctx context.Context) {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("ipc: accept error: %v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		select {
		case s.sem <- struct{}{}:
		default:
			// At capacity: refuse instead of spawning an unbounded handler.
			// Log the first rejection and then every 100th so a flood cannot
			// also flood the daemon log.
			if n := s.rejected.Add(1); n == 1 || n%100 == 0 {
				log.Printf("ipc: connection limit (%d) reached, rejecting connection (%d rejected so far)", s.cfg.MaxConns, n)
			}
			_ = conn.Close()
			continue
		}
		s.track(conn, true)
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			defer s.track(c, false)
			// A sink panic must take down the offending connection, not the
			// daemon: recover last so the cleanup defers still run.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("ipc: recovered from panic in connection handler: %v", r)
				}
			}()
			s.handleConn(ctx, c)
		}(conn)
	}
}

func (s *Server) track(c net.Conn, add bool) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if add {
		s.conns[c] = struct{}{}
		s.connected.Add(1)
		return
	}
	if _, ok := s.conns[c]; ok {
		delete(s.conns, c)
		s.connected.Add(-1)
	}
	_ = c.Close()
}

func isClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "pipe is being closed") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "closed pipe") ||
		strings.Contains(msg, "wsasend") ||
		strings.Contains(msg, "wsarecv")
}

// wireRequest decodes a request line while keeping the raw id member, so a
// JSON-RPC notification (no "id" member at all) can be told apart from an
// explicit "id": null, which still gets a response.
type wireRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
	ID      json.RawMessage        `json:"id,omitempty"`
}

// handleConn reads newline-delimited JSON-RPC requests and writes one response
// per request (none for notifications) until EOF, error, idle timeout, or
// cancellation.
func (s *Server) handleConn(ctx context.Context, c net.Conn) {
	reader := bufio.NewReaderSize(c, 64*1024)
	writer := bufio.NewWriterSize(c, 64*1024)
	defer func() { _ = writer.Flush() }()

	writeErrorLine := func(code int, msg string) bool {
		payload, mErr := json.Marshal(Response{JSONRPC: "2.0", ID: nil, Error: &RPCError{Code: code, Message: msg}})
		if mErr != nil {
			return true
		}
		_ = c.SetWriteDeadline(time.Now().Add(s.cfg.IdleTimeout))
		return writeJSONLine(writer, payload) == nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if s.cfg.IdleTimeout > 0 {
			_ = c.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
		}
		line, err := readJSONLine(reader)
		if errors.Is(err, errLineTooLong) {
			// The oversized line was drained up to its newline, so framing is
			// intact: report it and keep serving the connection.
			if !writeErrorLine(-32600, fmt.Sprintf("invalid request: line exceeds %d bytes", maxJSONLineBytes)) {
				return
			}
			continue
		}
		if err != nil {
			var ne net.Error
			switch {
			case errors.As(err, &ne) && ne.Timeout():
				log.Printf("ipc: closing connection idle for %s", s.cfg.IdleTimeout)
			case !isClientDisconnect(err):
				log.Printf("ipc: read error: %v", err)
			}
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue // blank separator lines are not requests
		}
		var wire wireRequest
		if err := json.Unmarshal(line, &wire); err != nil {
			if !writeErrorLine(-32700, "parse error: "+err.Error()) {
				return
			}
			continue
		}
		isNotification := wire.ID == nil
		req := Request{JSONRPC: wire.JSONRPC, Method: wire.Method, Params: wire.Params}
		if !isNotification {
			if err := json.Unmarshal(wire.ID, &req.ID); err != nil {
				req.ID = nil
			}
		}
		start := time.Now()
		resp := s.dispatch(&req)
		durMs := float64(time.Since(start).Microseconds()) / 1000.0

		// Marshal the response up front: the wire write needs the bytes, and
		// the traffic record needs the encoded size to skip retention-side
		// size probing for normal-sized traffic.
		respPayload, err := json.Marshal(resp)
		if err != nil {
			log.Printf("ipc: marshal response: %v", err)
			return
		}

		if s.cfg.Engine != nil {
			clientAddr := "named_pipe"
			if c.RemoteAddr() != nil {
				clientAddr = c.RemoteAddr().String()
			}
			s.cfg.Engine.RecordIPCTraffic(IPCTrafficRecord{
				ID:         fmt.Sprintf("ipc-%d", time.Now().UnixNano()),
				Method:     req.Method,
				Params:     req.Params,
				Result:     resp.Result,
				Error:      resp.Error,
				DurationMs: durMs,
				Timestamp:  time.Now().UTC(),
				ClientAddr: clientAddr,
				WireBytes:  len(line),
				RespBytes:  len(respPayload),
			})
		}

		if isNotification {
			// JSON-RPC 2.0 §4.1: the server MUST NOT reply to a notification.
			// It is still dispatched (fire-and-forget telemetry) and recorded.
			continue
		}
		// Bound the response write: a client that stops reading while a large
		// reply is in flight must not pin this goroutine in the flush forever.
		_ = c.SetWriteDeadline(time.Now().Add(s.cfg.IdleTimeout))
		if err := writeJSONLine(writer, respPayload); err != nil {
			if !isClientDisconnect(err) {
				log.Printf("ipc: write error: %v", err)
			}
			return
		}
	}
}

// dispatch routes a request to the matching method.
func (s *Server) dispatch(req *Request) Response {
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "telemetry/report_run", "report_telemetry", "telemetry/report", "telemetry_report":
		if req.Params == nil {
			resp.Error = &RPCError{Code: -32602, Message: "params are required"}
			return resp
		}
		var p TelemetryReport
		if err := mapToStruct(req.Params, &p); err != nil {
			resp.Error = &RPCError{Code: -32602, Message: err.Error()}
			return resp
		}
		// Fallbacks for flexible argument aliases
		if p.ModelName == "" {
			if m, ok := req.Params["model"].(string); ok {
				p.ModelName = m
			}
		}
		if p.AgentName == "" {
			if a, ok := req.Params["agent"].(string); ok {
				p.AgentName = a
			}
		}
		if p.PromptTokens == 0 {
			if tok, ok := req.Params["tokens_used"].(float64); ok {
				p.PromptTokens = int64(tok)
			}
		}
		if p.CostUSD == 0 {
			if c, ok := req.Params["cost"].(float64); ok {
				p.CostUSD = c
			}
		}
		if p.RunID == "" {
			if req.Method == "telemetry/report_run" {
				resp.Error = &RPCError{Code: -32602, Message: "run_id is required"}
				return resp
			}
			p.RunID = fmt.Sprintf("ipc-%d", time.Now().UnixNano())
		}
		if err := s.cfg.Engine.ReportRun(p); err != nil {
			resp.Error = &RPCError{Code: -32010, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]string{"status": "ok"}

	case "telemetry/report_file_read", "report_file_read", "report_read", "telemetry/read_event":
		var p FileReadReport
		if err := mapToStruct(req.Params, &p); err != nil {
			resp.Error = &RPCError{Code: -32602, Message: err.Error()}
			return resp
		}
		filePath := p.FilePath
		if filePath == "" {
			filePath = p.Path
		}
		if filePath == "" {
			resp.Error = &RPCError{Code: -32602, Message: "file_path is required"}
			return resp
		}
		// repo_name is intentionally NOT validated here. An empty value cannot
		// violate the schema: NOT NULL rejects NULL, while the store binds plain
		// Go strings, so "" is stored as '' (provider and tool_name are already
		// '' on every call from this handler). Engine.RecordReadEvent resolves an
		// empty RepoName from the active project, or from the project that owns
		// FilePath (engine.go), which is more accurate than rejecting the event --
		// and the MCP report_file_read tool sends this same payload shape.
		modelName := p.ModelName
		if modelName == "" {
			modelName = p.Model
		}
		promptTokens := p.PromptTokens
		if promptTokens == 0 {
			promptTokens = p.TokensConsumed
		}
		rec := db.FileReadRecord{
			ReadID:         fmt.Sprintf("read-%d", time.Now().UnixNano()),
			RunID:          p.RunID,
			RepoName:       p.RepoName,
			FilePath:       filePath,
			ModelName:      modelName,
			AgentName:      p.AgentName,
			LinesReadCount: p.LineCount,
			PromptTokens:   promptTokens,
			CostUSD:        p.CostUSD,
			ReadTime:       time.Now().UTC(),
		}
		if err := s.cfg.Engine.RecordReadEvent(rec); err != nil {
			resp.Error = &RPCError{Code: -32012, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]interface{}{"status": "ok", "file_path": filePath, "read_id": rec.ReadID}

	case "check_guardrail", "guardrail/check", "telemetry/check_guardrail", "guardrail_check":
		var p GuardrailCheckRequest
		if err := mapToStruct(req.Params, &p); err != nil {
			resp.Error = &RPCError{Code: -32602, Message: err.Error()}
			return resp
		}
		filePath := p.FilePath
		if filePath == "" {
			filePath = p.Path
		}
		if filePath == "" {
			resp.Error = &RPCError{Code: -32602, Message: "path or file_path is required"}
			return resp
		}
		gr, err := s.cfg.Engine.CheckGuardrail(filePath)
		if err != nil {
			resp.Error = &RPCError{Code: -32013, Message: err.Error()}
			return resp
		}
		// lock_owner_run_id is the credential the unlock_file ownership check
		// verifies, and unlike the other guardrail replies this one
		// serializes the whole struct, so the field must be cleared on the
		// local copy before it goes on the wire. Handing it out lets any agent
		// enumerate a locked file's holder and present the credential to
		// unlock_file, which is exactly the lock steal the ownership check
		// exists to refuse. The lock owner NAME and reason stay for
		// diagnostics.
		gr.LockOwnerRunID = ""
		resp.Result = gr

	case "telemetry/file_health", "get_file_health_score", "file_health", "telemetry/get_file_health_score":
		var p FileHealthQuery
		if err := mapToStruct(req.Params, &p); err != nil {
			resp.Error = &RPCError{Code: -32602, Message: err.Error()}
			return resp
		}
		filePath := p.FilePath
		if filePath == "" {
			filePath = p.Path
		}
		if filePath == "" {
			resp.Error = &RPCError{Code: -32602, Message: "file_path is required"}
			return resp
		}
		h, err := s.cfg.Engine.FileHealth(filePath)
		if err != nil {
			resp.Error = &RPCError{Code: -32011, Message: err.Error()}
			return resp
		}
		// lock_owner_run_id is the credential the unlock_file ownership check
		// verifies; broadcasting it would let any agent steal locks. The
		// lock owner name stays for diagnostics.
		h.LockOwnerRunID = ""
		resp.Result = h

	case "lock_file", "guardrail/lock", "telemetry/lock_file":
		var p LockFileRequest
		if err := mapToStruct(req.Params, &p); err != nil {
			resp.Error = &RPCError{Code: -32602, Message: err.Error()}
			return resp
		}
		filePath := p.FilePath
		if filePath == "" {
			filePath = p.Path
		}
		if filePath == "" {
			resp.Error = &RPCError{Code: -32602, Message: "file_path or path is required"}
			return resp
		}
		// Round-25 contract (re-landed round 40): cap the TTL BEFORE scaling to
		// time.Duration — a raw client integer multiplied into int64
		// nanoseconds overflows (ttl_seconds=18446744074 wraps to ~0.29s),
		// silently issuing a near-instant guardrail lock.
		const maxLockTTL = 24 * time.Hour
		switch {
		case int64(p.TTLSeconds) > int64(maxLockTTL/time.Second):
			resp.Error = &RPCError{Code: -32602, Message: "ttl_seconds exceeds the 24h maximum lock TTL"}
			return resp
		case int64(p.TTLMinutes) > int64(maxLockTTL/time.Minute):
			resp.Error = &RPCError{Code: -32602, Message: "ttl_minutes exceeds the 24h maximum lock TTL"}
			return resp
		case int64(p.TTL) > int64(maxLockTTL/time.Second):
			resp.Error = &RPCError{Code: -32602, Message: "ttl exceeds the 24h maximum lock TTL"}
			return resp
		}
		var ttl time.Duration
		if p.TTLSeconds > 0 {
			ttl = time.Duration(p.TTLSeconds) * time.Second
		} else if p.TTLMinutes > 0 {
			ttl = time.Duration(p.TTLMinutes) * time.Minute
		} else if p.TTL > 0 {
			ttl = time.Duration(p.TTL) * time.Second
		} else {
			ttl = 15 * time.Minute
		}
		info, err := s.cfg.Engine.TryLockFile(filePath, p.Reason, p.Owner, p.OwnerRunID, ttl, false)
		if err != nil {
			var conflict *LockConflictError
			if errors.As(err, &conflict) {
				resp.Error = &RPCError{Code: -32602, Message: conflict.Error(), Data: map[string]interface{}{
					"status":   "conflict",
					"existing": conflict.Existing,
				}}
				return resp
			}
			resp.Error = &RPCError{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = info

	case "unlock_file", "guardrail/unlock", "telemetry/unlock_file":
		var p LockFileRequest
		if err := mapToStruct(req.Params, &p); err != nil {
			resp.Error = &RPCError{Code: -32602, Message: err.Error()}
			return resp
		}
		filePath := p.FilePath
		if filePath == "" {
			filePath = p.Path
		}
		if filePath == "" {
			resp.Error = &RPCError{Code: -32602, Message: "file_path or path is required"}
			return resp
		}
		if err := s.cfg.Engine.UnlockFile(filePath, p.OwnerRunID); err != nil {
			// Surface the refusal (e.g. ErrNotLockOwner for a foreign owner)
			// instead of answering a lock-steal attempt with success; matches
			// the lock_file handler's engine-error convention above.
			resp.Error = &RPCError{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]interface{}{"status": "unlocked", "file_path": filePath}

	case "list_locks", "guardrail/locks", "telemetry/list_locks":
		locks := s.cfg.Engine.ListLocks()
		// owner_run_id is the credential the unlock_file ownership check
		// verifies; broadcasting it would let any agent pass that check and
		// steal locks. Callers identify themselves with their own run_id.
		redacted := make([]LockInfo, len(locks))
		for i, info := range locks {
			info.OwnerRunID = ""
			redacted[i] = info
		}
		resp.Result = map[string]interface{}{"locks": redacted, "count": len(redacted)}

	case "atlas", "get_atlas", "telemetry/atlas":
		var p AtlasRequest
		_ = mapToStruct(req.Params, &p)
		filter := p.Repo
		if filter == "" {
			filter = p.Filter
		}
		snap, err := callAtlas(s.cfg.Engine, filter)
		if err != nil {
			resp.Error = &RPCError{Code: -32014, Message: err.Error()}
			return resp
		}
		resp.Result = snap

	case "get_file_read_stats", "file_read_stats", "telemetry/file_read_stats":
		var p FileHealthQuery
		_ = mapToStruct(req.Params, &p)
		filePath := p.FilePath
		if filePath == "" {
			filePath = p.Path
		}
		stats, err := s.cfg.Engine.GetFileReadStats(filePath)
		if err != nil {
			resp.Error = &RPCError{Code: -32015, Message: err.Error()}
			return resp
		}
		resp.Result = stats

	case "get_file_diff_history", "diff_history", "recent_file_events":
		var p DiffHistoryRequest
		_ = mapToStruct(req.Params, &p)
		filePath := p.FilePath
		if filePath == "" {
			filePath = p.Path
		}
		limit := p.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > maxIPCHistoryLimit {
			limit = maxIPCHistoryLimit
		}
		events, err := s.cfg.Engine.GetRecentFileEvents(filePath, limit)
		if err != nil {
			resp.Error = &RPCError{Code: -32016, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]interface{}{"events": events, "count": len(events)}

	case "rpc.discover", "system.listMethods", "rpc.listMethods", "tools/list":
		serverVersion := s.cfg.Version
		if serverVersion == "" {
			serverVersion = "dev"
		}
		resp.Result = map[string]interface{}{
			"methods": []string{
				"telemetry/report_run",
				"report_telemetry",
				"telemetry/report_file_read",
				"report_file_read",
				"check_guardrail",
				"telemetry/check_guardrail",
				"telemetry/file_health",
				"get_file_health_score",
				"lock_file",
				"unlock_file",
				"list_locks",
				"atlas",
				"get_atlas",
				"get_file_read_stats",
				"get_file_diff_history",
				"ping",
				"rpc.discover",
				"system.listMethods",
			},
			"server":  "wrongtrace",
			"version": serverVersion,
		}

	case "ping":
		if err := s.cfg.Engine.Ping(); err != nil {
			resp.Error = &RPCError{Code: -32000, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]string{"pong": "1"}

	default:
		resp.Error = &RPCError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

// callAtlas dynamically invokes Atlas on EngineSink if present.
func callAtlas(engine any, filter string) (any, error) {
	if engine == nil {
		return nil, errors.New("engine is nil")
	}
	val := reflect.ValueOf(engine)
	m := val.MethodByName("Atlas")
	if !m.IsValid() {
		return nil, errors.New("atlas method not available")
	}
	var args []reflect.Value
	if filter != "" {
		args = append(args, reflect.ValueOf(filter))
	}
	res := m.Call(args)
	if len(res) == 2 {
		if !res[1].IsNil() {
			return nil, res[1].Interface().(error)
		}
		return res[0].Interface(), nil
	}
	return nil, errors.New("unexpected atlas return signature")
}

// discoveryLinkPath is the well-known POSIX symlink third-party agents probe.
// A variable only so tests can point it at a temp dir instead of real /tmp.
var discoveryLinkPath = "/tmp/wrongtrace.sock"

// bindSocket opens a Unix Domain Socket or Named Pipe, depending on platform.
func bindSocket(path string) (net.Listener, error) {
	ln, _, _, err := bindSocketPaths(path)
	return ln, err
}

// bindSocketPaths binds like bindSocket and also reports the socket file
// actually bound and the discovery symlink it created ("" when none), so Stop
// can clean both up.
func bindSocketPaths(path string) (net.Listener, string, string, error) {
	if runtime.GOOS == "windows" {
		if dir := filepath.Dir(path); dir != "" && dir != "." && !strings.HasPrefix(path, `\\.\pipe`) {
			_ = os.MkdirAll(dir, 0o755)
		}
		ln, err := bindWindowsPipe(path)
		return ln, path, "", err
	}

	// POSIX Unix Domain Socket handling
	// macOS (Darwin) limit is 104 bytes, Linux limit is 108 bytes for sockaddr_un.sun_path.
	maxLen := 104
	if runtime.GOOS == "linux" {
		maxLen = 108
	}

	targetPath := path
	if len(targetPath) >= maxLen {
		// Fallback to /tmp if configured path is too long
		targetPath = filepath.Join(os.TempDir(), "wrongtrace.sock")
		log.Printf("ipc: socket path too long (%d >= %d), falling back to %s", len(path), maxLen, targetPath)
	}

	if dir := filepath.Dir(targetPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, "", "", err
		}
	}

	// Clean a stale socket from a previous run. The fallback lives in a
	// shared, world-writable directory, so never delete an entry another user
	// planted there: refuse to bind rather than serve (or clobber) theirs.
	if fi, err := os.Lstat(targetPath); err == nil {
		if !ownedByCurrentUser(fi) {
			return nil, "", "", fmt.Errorf("refusing to replace %s: it is owned by another user", targetPath)
		}
		if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
			log.Printf("ipc: remove stale socket %s: %v", targetPath, err)
		}
	}
	ln, err := net.Listen("unix", targetPath)
	if err != nil {
		return nil, "", "", err
	}

	// On POSIX, if the primary socket is ~/.wrongtrace/wrongtrace.sock, also
	// publish a /tmp/wrongtrace.sock symlink for zero-config discovery.
	link := ""
	if targetPath != discoveryLinkPath && publishDiscoveryLink(targetPath, discoveryLinkPath) {
		link = discoveryLinkPath
	}
	return ln, targetPath, link, nil
}

// publishDiscoveryLink points linkPath at target. An existing entry is
// replaced only when it is a symlink owned by the current user (a previous
// run of ours); anything else — another user's socket, file, or symlink
// squatting the well-known name — is logged and left untouched so agents are
// never silently redirected. Reports whether the symlink was created.
func publishDiscoveryLink(target, linkPath string) bool {
	if fi, err := os.Lstat(linkPath); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 || !ownedByCurrentUser(fi) {
			log.Printf("ipc: leaving %s untouched: not a symlink owned by this user (discovery link not published)", linkPath)
			return false
		}
		if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
			log.Printf("ipc: replace discovery symlink %s: %v", linkPath, err)
			return false
		}
	} else if !os.IsNotExist(err) {
		log.Printf("ipc: inspect discovery symlink %s: %v", linkPath, err)
		return false
	}
	if err := os.Symlink(target, linkPath); err != nil {
		log.Printf("ipc: create discovery symlink %s -> %s: %v", linkPath, target, err)
		return false
	}
	return true
}

// maxIPCHistoryLimit caps positive client-supplied history limits, matching
// the HTTP surface's maxRecentEventsLimit, so a single oversized request
// cannot make the engine scan unbounded event history.
const maxIPCHistoryLimit = 1000

const maxJSONLineBytes = 16 * 1024 * 1024 // 16 MB max line length to protect against unbounded RAM allocation

// errLineTooLong reports a request line over maxJSONLineBytes. The rest of the
// line has already been discarded, so the stream is positioned at the next
// request and the caller may keep serving.
var errLineTooLong = errors.New("ipc: line too long, exceeded maximum buffer limit")

func readJSONLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		if !tooLong {
			if len(line)+len(chunk) > maxJSONLineBytes {
				tooLong = true
				line = nil // release what was buffered; drain the rest
			} else {
				line = append(line, chunk...)
			}
		}
		if !isPrefix {
			break
		}
	}
	if tooLong {
		return nil, errLineTooLong
	}
	return line, nil
}

// writeJSONLine writes a pre-marshaled response frame followed by a newline
// and flushes, so the traffic layer and the wire share one encode.
func writeJSONLine(w *bufio.Writer, payload []byte) error {
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

// mapToStruct decodes a generic JSON-RPC params object into the concrete
// payload struct via re-marshal, so number precision behaves predictably.
func mapToStruct(params map[string]interface{}, out interface{}) error {
	if params == nil {
		return nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
