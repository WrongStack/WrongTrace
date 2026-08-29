package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// testSocketPath returns a per-test socket path. On Windows this is a unique
// pipe name; on POSIX a temp file path whose directory bindSocket creates.
// On macOS Darwin, sockaddr_un.sun_path is capped at 104 bytes (Linux 108 bytes),
// so we generate bounded short paths in os.TempDir().
func testSocketPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return `\\.\pipe\wrongtrace-test-` + t.Name() + fmt.Sprintf("-%d", time.Now().UnixNano())
	}
	shortName := fmt.Sprintf("wt-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%1000000)
	return filepath.Join(os.TempDir(), shortName)
}

// startLiveServer binds a real socket, starts serving, and registers cleanup.
func startLiveServer(t *testing.T, sink *fakeSink) (*Server, string) {
	t.Helper()
	if sink == nil {
		sink = &fakeSink{}
	}
	path := testSocketPath(t)
	srv := NewServer(Config{SocketPath: path, Engine: sink})
	if err := srv.Start(); err != nil {
		t.Fatalf("ipc start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv, path
}

// dial opens a client connection, retrying briefly because a named pipe may
// need a moment to accept after Start returns.
func dial(t *testing.T, path string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := dialSocket(path, 2*time.Second)
		if err == nil {
			t.Cleanup(func() { _ = conn.Close() })
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type client struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

func newClient(t *testing.T, path string) *client {
	t.Helper()
	conn := dial(t, path)
	return &client{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}
}

func (c *client) roundTrip(t *testing.T, payload string) Response {
	t.Helper()
	if _, err := c.w.WriteString(payload + "\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := c.w.Flush(); err != nil {
		t.Fatalf("flush request: %v", err)
	}
	line, err := c.r.ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", line, err)
	}
	return resp
}

func TestStart_Validation(t *testing.T) {
	if err := NewServer(Config{SocketPath: `\\.\pipe\wt-v-1`}).Start(); err == nil {
		t.Error("Start with nil engine must fail")
	}
	if err := NewServer(Config{Engine: &fakeSink{}}).Start(); err == nil {
		t.Error("Start with empty socket path must fail")
	}
}

func TestServer_LifecycleAndStopIdempotent(t *testing.T) {
	srv, path := startLiveServer(t, nil)
	if srv.ConnectedCount() != 0 {
		t.Errorf("fresh server ConnectedCount = %d, want 0", srv.ConnectedCount())
	}

	c := newClient(t, path)
	resp := c.roundTrip(t, `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`)
	if resp.Error != nil || resp.ID == nil {
		t.Fatalf("ping over live socket = %+v, want success", resp)
	}

	srv.Stop()
	srv.Stop() // must be safe twice

	// A new server must rebind the same path immediately after Stop.
	srv2 := NewServer(Config{SocketPath: path, Engine: &fakeSink{}})
	if err := srv2.Start(); err != nil {
		t.Fatalf("rebind same path after Stop: %v", err)
	}
	srv2.Stop()
}

func TestConnectedCount_TracksClientLifecycle(t *testing.T) {
	srv, path := startLiveServer(t, nil)

	c1 := newClient(t, path)
	waitFor(t, func() bool { return srv.ConnectedCount() == 1 }, "first client tracked")

	c2 := newClient(t, path)
	waitFor(t, func() bool { return srv.ConnectedCount() == 2 }, "second client tracked")

	_ = c1.conn.Close()
	waitFor(t, func() bool { return srv.ConnectedCount() == 1 }, "count drop after EOF")

	_ = c2.conn.Close()
	waitFor(t, func() bool { return srv.ConnectedCount() == 0 }, "count reaches zero")
}

func TestHandleConn_FullProtocolOverSocket(t *testing.T) {
	sink := &fakeSink{healthOut: FileHealthReply{
		FilePath: "hot.go", HealthScore: 91, IsFragile: true, RecentThrashingCount: 3, Warning: "hot",
	}}
	_, path := startLiveServer(t, sink)
	c := newClient(t, path)

	resp := c.roundTrip(t, `{"jsonrpc":"2.0","id":7,"method":"telemetry/report_run","params":{"run_id":"r-77","model_name":"m1","agent_name":"ipc-test","task_intent":"test intent","cost_usd":0.5}}`)
	if resp.Error != nil {
		t.Fatalf("report_run failed: %+v", resp.Error)
	}
	if sink.gotReport.RunID != "r-77" || sink.gotReport.ModelName != "m1" || sink.gotReport.AgentName != "ipc-test" {
		t.Errorf("sink received %+v", sink.gotReport)
	}
	if !approx(sink.gotReport.CostUSD, 0.5) {
		t.Errorf("cost = %v, want 0.5", sink.gotReport.CostUSD)
	}

	resp = c.roundTrip(t, `{"jsonrpc":"2.0","id":8,"method":"telemetry/file_health","params":{"file_path":"hot.go"}}`)
	if resp.Error != nil {
		t.Fatalf("file_health failed: %+v", resp.Error)
	}
	if sink.gotPath != "hot.go" {
		t.Errorf("gotPath = %q, want hot.go", sink.gotPath)
	}
	hb, _ := json.Marshal(resp.Result)
	for _, want := range []string{`"health_score":91`, `"is_fragile":true`, `"file_path":"hot.go"`} {
		if !bytes.Contains(hb, []byte(want)) {
			t.Errorf("file_health payload %s missing %s", hb, want)
		}
	}

	// Malformed line -> parse error, connection stays usable.
	resp = c.roundTrip(t, `{this is not json`)
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Errorf("malformed JSON = %+v, want -32700", resp.Error)
	}
	resp = c.roundTrip(t, `{"jsonrpc":"2.0","id":9,"method":"ping"}`)
	if resp.Error != nil {
		t.Errorf("connection must survive a parse error, got %+v", resp.Error)
	}

	resp = c.roundTrip(t, `{"jsonrpc":"2.0","id":10,"method":"no/such"}`)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("unknown method = %+v, want -32601", resp.Error)
	}

	resp = c.roundTrip(t, `{"jsonrpc":"2.0","id":11,"method":"telemetry/report_run","params":{"model_name":"x"}}`)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("missing run_id = %+v, want -32602", resp.Error)
	}
}

func TestHandleConn_SinkErrorMapsToCode(t *testing.T) {
	sink := &fakeSink{reportErr: fmt.Errorf("db exploded")}
	_, path := startLiveServer(t, sink)
	c := newClient(t, path)

	resp := c.roundTrip(t, `{"jsonrpc":"2.0","id":1,"method":"telemetry/report_run","params":{"run_id":"boom"}}`)
	if resp.Error == nil || resp.Error.Code != -32010 {
		t.Errorf("sink error = %+v, want -32010 carrying cause", resp.Error)
	}
}

// TestWriteJSONLine_NewlineFraming pins the protocol contract: every response
// is exactly one JSON document terminated by \n.
func TestWriteJSONLine_NewlineFraming(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	payload, err := json.Marshal(Response{JSONRPC: "2.0", ID: 42})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if err := writeJSONLine(w, payload); err != nil {
		t.Fatalf("writeJSONLine: %v", err)
	}
	out := buf.Bytes()
	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Fatalf("response not newline-terminated: %q", out)
	}
	if n := bytes.Count(out, []byte{'\n'}); n != 1 {
		t.Errorf("response spans %d lines, want exactly 1", n)
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSuffix(out, []byte{'\n'}), &resp); err != nil {
		t.Errorf("framed payload is not valid JSON: %v", err)
	}
}

// TestReadJSONLine_LongLineAcrossBuffers forces the ReadLine continuation
// loop with a line longer than the reader's 4096-byte default buffer.
func TestReadJSONLine_LongLineAcrossBuffers(t *testing.T) {
	big := bytes.Repeat([]byte{'x'}, 20_000)
	line := append([]byte(`{"jsonrpc":"2.0","id":1,"padding":"`), big...)
	line = append(line, '"', '}', '\n')

	pr, pw := net.Pipe()
	defer pr.Close()
	go func() {
		_, _ = pw.Write(line)
		_ = pw.Close()
	}()
	got, err := readJSONLine(bufio.NewReader(pr))
	if err != nil {
		t.Fatalf("readJSONLine: %v", err)
	}
	if len(got) != len(line)-1 {
		t.Errorf("read %d bytes, want %d (full reassembled line)", len(got), len(line)-1)
	}
	var req Request
	if err := json.Unmarshal(got, &req); err != nil {
		t.Errorf("reassembled line is not valid JSON: %v", err)
	}
}

func TestBindSocket_CreatesParentDirs(t *testing.T) {
	// Parent-directory creation only applies to POSIX unix socket paths; on
	// Windows bindSocket always routes to a named pipe (\\.\pipe\name),
	// which has no filesystem directory component to create.
	if runtime.GOOS == "windows" {
		t.Skip("unix-socket path semantics do not apply on Windows named pipes")
	}
	path := filepath.Join(t.TempDir(), "deep", "nested", "ipc.sock")
	ln, err := bindSocket(path)
	if err != nil {
		t.Fatalf("bindSocket with nested dirs: %v", err)
	}
	defer ln.Close()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("dial freshly bound unix socket: %v", err)
	}
	_ = conn.Close()
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
