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
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// EngineSink is the subset of the core Engine used by the IPC server. Keeping
// it as an interface lets us test the IPC layer without spinning up the full
// engine, and avoids an import cycle (ipc -> core -> ipc).
type EngineSink interface {
	ReportRun(r TelemetryReport) error
	FileHealth(p string) (FileHealthReply, error)
	Ping() error
}

// FileHealthReply is the JSON-serializable view of db.FileHealth. The ipc
// package re-declares it so the protocol is decoupled from the storage type.
type FileHealthReply struct {
	FilePath             string `json:"file_path"`
	HealthScore          int    `json:"health_score"`
	IsFragile            bool   `json:"is_fragile"`
	RecentThrashingCount int    `json:"recent_thrashing_count"`
	Warning              string `json:"warning"`
}

// Config configures the IPC server.
type Config struct {
	SocketPath string
	Engine     EngineSink
}

// Server is the agent-facing IPC endpoint.
type Server struct {
	cfg      Config
	ln       net.Listener
	wg       sync.WaitGroup
	cancel   context.CancelFunc
	connsMu  sync.Mutex
	conns    map[net.Conn]struct{}
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
			if err != io.EOF {
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
		resp := s.dispatch(&req)
		if err := writeJSONLine(writer, resp); err != nil {
			log.Printf("ipc: write error: %v", err)
			return
		}
	}
}

// dispatch routes a request to the matching method.
func (s *Server) dispatch(req *Request) Response {
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "telemetry/report_run":
		var p TelemetryReport
		if err := mapToStruct(req.Params, &p); err != nil {
			resp.Error = &RPCError{Code: -32602, Message: err.Error()}
			return resp
		}
		if p.RunID == "" {
			resp.Error = &RPCError{Code: -32602, Message: "run_id is required"}
			return resp
		}
		if err := s.cfg.Engine.ReportRun(p); err != nil {
			resp.Error = &RPCError{Code: -32010, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]string{"status": "ok"}
	case "telemetry/file_health":
		var p FileHealthQuery
		if err := mapToStruct(req.Params, &p); err != nil {
			resp.Error = &RPCError{Code: -32602, Message: err.Error()}
			return resp
		}
		if p.FilePath == "" {
			resp.Error = &RPCError{Code: -32602, Message: "file_path is required"}
			return resp
		}
		h, err := s.cfg.Engine.FileHealth(p.FilePath)
		if err != nil {
			resp.Error = &RPCError{Code: -32011, Message: err.Error()}
			return resp
		}
		resp.Result = h
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

// bindSocket opens a Unix Domain Socket or Named Pipe, depending on platform.
func bindSocket(path string) (net.Listener, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if runtime.GOOS == "windows" {
		return bindWindowsPipe(path)
	}
	// Clean stale socket from a previous run; ignore "not exist" errors.
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}
	return net.Listen("unix", path)
}

func readJSONLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
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
