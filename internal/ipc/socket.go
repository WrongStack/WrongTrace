package ipc

import (
	"bufio"
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
}

// EngineSink is the subset of the core Engine used by the IPC server. Keeping
// it as an interface lets us test the IPC layer without spinning up the full
// engine, and avoids an import cycle (ipc -> core -> ipc).
type EngineSink interface {
	ReportRun(r TelemetryReport) error
	RecordReadEvent(rec db.FileReadRecord) error
	FileHealth(p string) (FileHealthReply, error)
	CheckGuardrail(p string) (GuardrailResult, error)
	LockFileWithOptions(path, reason, owner, ownerRunID string, ttl time.Duration) LockInfo
	UnlockFile(path string)
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
}

// Server is the agent-facing IPC endpoint.
type Server struct {
	cfg       Config
	ln        net.Listener
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	connsMu   sync.Mutex
	conns     map[net.Conn]struct{}
	connected atomic.Int64
	startedAt time.Time
}

// NewServer returns a Server ready to be started with Start.
func NewServer(cfg Config) *Server {
	return &Server{
		cfg:       cfg,
		conns:     make(map[net.Conn]struct{}),
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

	ln, err := bindSocket(s.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("bind socket: %w", err)
	}
	s.ln = ln

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
	if runtime.GOOS != "windows" && s.cfg.SocketPath != "" {
		_ = os.Remove(s.cfg.SocketPath)
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
		s.track(conn, true)
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer s.track(c, false)
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

// handleConn reads newline-delimited JSON-RPC requests and writes one response
// per request until EOF, error, or cancellation.
func (s *Server) handleConn(ctx context.Context, c net.Conn) {
	reader := bufio.NewReaderSize(c, 64*1024)
	writer := bufio.NewWriterSize(c, 64*1024)
	defer func() { _ = writer.Flush() }()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line, err := readJSONLine(reader)
		if err != nil {
			if !isClientDisconnect(err) {
				log.Printf("ipc: read error: %v", err)
			}
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = writeJSONLine(writer, Response{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &RPCError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}
		start := time.Now()
		resp := s.dispatch(&req)
		durMs := float64(time.Since(start).Microseconds()) / 1000.0

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
			})
		}

		if err := writeJSONLine(writer, resp); err != nil {
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
		info := s.cfg.Engine.LockFileWithOptions(filePath, p.Reason, p.Owner, p.OwnerRunID, ttl)
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
		s.cfg.Engine.UnlockFile(filePath)
		resp.Result = map[string]interface{}{"status": "unlocked", "file_path": filePath}

	case "list_locks", "guardrail/locks", "telemetry/list_locks":
		locks := s.cfg.Engine.ListLocks()
		resp.Result = map[string]interface{}{"locks": locks, "count": len(locks)}

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
		events, err := s.cfg.Engine.GetRecentFileEvents(filePath, limit)
		if err != nil {
			resp.Error = &RPCError{Code: -32016, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]interface{}{"events": events, "count": len(events)}

	case "rpc.discover", "system.listMethods", "rpc.listMethods", "tools/list":
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
			"version": "0.3.6",
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

// bindSocket opens a Unix Domain Socket or Named Pipe, depending on platform.
func bindSocket(path string) (net.Listener, error) {
	if runtime.GOOS == "windows" {
		if dir := filepath.Dir(path); dir != "" && dir != "." && !strings.HasPrefix(path, `\\.\pipe`) {
			_ = os.MkdirAll(dir, 0o755)
		}
		return bindWindowsPipe(path)
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
			return nil, err
		}
	}

	// Clean stale socket from a previous run; ignore "not exist" errors.
	if _, err := os.Stat(targetPath); err == nil {
		_ = os.Remove(targetPath)
	}
	ln, err := net.Listen("unix", targetPath)
	if err != nil {
		return nil, err
	}

	// On POSIX, if primary socket is ~/.wrongtrace/wrongtrace.sock, also create /tmp/wrongtrace.sock
	// symlink for zero-config discovery by third-party agents looking in /tmp.
	if targetPath != "/tmp/wrongtrace.sock" {
		_ = os.Remove("/tmp/wrongtrace.sock")
		_ = os.Symlink(targetPath, "/tmp/wrongtrace.sock")
	}

	return ln, nil
}

const maxJSONLineBytes = 16 * 1024 * 1024 // 16 MB max line length to protect against unbounded RAM allocation

func readJSONLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(line)+len(chunk) > maxJSONLineBytes {
			return nil, errors.New("ipc: line too long, exceeded maximum buffer limit")
		}
		line = append(line, chunk...)
		if !isPrefix {
			break
		}
	}
	return line, nil
}

func writeJSONLine(w *bufio.Writer, v Response) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
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
