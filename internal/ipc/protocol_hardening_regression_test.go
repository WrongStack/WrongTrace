package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// readResponse reads one framed response with a deadline so a missing reply
// fails the test instead of hanging it.
func (c *client) readResponse(t *testing.T, within time.Duration) Response {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(within))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
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

func (c *client) send(t *testing.T, payload string) {
	t.Helper()
	if _, err := c.w.WriteString(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := c.w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// JSON-RPC 2.0 §4.1: a request without an "id" member is a notification and
// must get no reply; blank lines are not requests at all. Before the fix both
// produced an {"id":null,...} frame that desynchronized request/response
// pairing for pipelined clients. An explicit "id": null is still a request.
func TestHandleConn_NotificationsAndBlankLinesGetNoReply(t *testing.T) {
	_, path := startLiveServer(t, nil)
	c := newClient(t, path)

	c.send(t, `{"jsonrpc":"2.0","method":"ping"}`+"\n")
	c.send(t, "\n   \n\t\n")
	c.send(t, `{"jsonrpc":"2.0","id":null,"method":"ping"}`+"\n")
	c.send(t, `{"jsonrpc":"2.0","id":5,"method":"ping"}`+"\n")

	first := c.readResponse(t, 3*time.Second)
	if first.ID != nil || first.Error != nil {
		t.Fatalf("first reply = %+v, want the explicit id:null ping's success (notification and blank lines must be silent)", first)
	}
	second := c.readResponse(t, 3*time.Second)
	if id, _ := second.ID.(float64); id != 5 || second.Error != nil {
		t.Fatalf("second reply = %+v, want id 5 success", second)
	}
}

// A line over the 16 MB cap used to tear the connection down silently. The
// oversized line is now drained and answered with -32600, and the same
// connection keeps serving.
func TestHandleConn_OversizedLineRejectedConnectionSurvives(t *testing.T) {
	_, path := startLiveServer(t, nil)
	c := newClient(t, path)

	done := make(chan error, 1)
	go func() {
		big := bytes.Repeat([]byte{'x'}, maxJSONLineBytes+1024)
		if _, err := c.w.Write(big); err != nil {
			done <- err
			return
		}
		if _, err := c.w.WriteString("\n" + `{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"); err != nil {
			done <- err
			return
		}
		done <- c.w.Flush()
	}()

	tooLong := c.readResponse(t, 30*time.Second)
	if tooLong.Error == nil || tooLong.Error.Code != -32600 || tooLong.ID != nil {
		t.Fatalf("oversized line reply = %+v, want -32600 with id null", tooLong)
	}
	next := c.readResponse(t, 30*time.Second)
	if id, _ := next.ID.(float64); id != 2 || next.Error != nil {
		t.Fatalf("follow-up reply = %+v, want id 2 success on the same connection", next)
	}
	if err := <-done; err != nil {
		t.Fatalf("client write: %v", err)
	}
}

func TestReadJSONLine_OversizedLineIsDrained(t *testing.T) {
	var in bytes.Buffer
	in.Write(bytes.Repeat([]byte{'y'}, maxJSONLineBytes+1))
	in.WriteString("\n{\"id\":1}\n")
	r := bufio.NewReaderSize(&in, 64*1024)

	if _, err := readJSONLine(r); !errors.Is(err, errLineTooLong) {
		t.Fatalf("first read err = %v, want errLineTooLong", err)
	}
	line, err := readJSONLine(r)
	if err != nil || string(line) != `{"id":1}` {
		t.Fatalf("second read = %q, %v; want the next request intact", line, err)
	}
	if _, err := readJSONLine(r); err != io.EOF {
		t.Fatalf("third read err = %v, want io.EOF", err)
	}
}

// Connections beyond MaxConns are closed immediately instead of each pinning
// a goroutine and 128 KiB of buffers forever.
func TestAcceptLoop_ConnectionCapRejectsExtra(t *testing.T) {
	path := testSocketPath(t)
	srv := NewServer(Config{SocketPath: path, Engine: &fakeSink{}, MaxConns: 1})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Stop)

	c1 := newClient(t, path)
	if resp := c1.roundTrip(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); resp.Error != nil {
		t.Fatalf("first client ping: %+v", resp.Error)
	}

	c2 := dial(t, path)
	_ = c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _ = c2.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"))
	buf := make([]byte, 64)
	if n, err := c2.Read(buf); err == nil {
		t.Fatalf("second connection over the cap was served (%q), want it closed", buf[:n])
	} else if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		t.Fatal("second connection over the cap was left open (read timed out), want it closed")
	}

	// Releasing the slot lets a new client in.
	_ = c1.conn.Close()
	waitFor(t, func() bool { return srv.ConnectedCount() == 0 }, "slot released")
	c3 := newClient(t, path)
	if resp := c3.roundTrip(t, `{"jsonrpc":"2.0","id":3,"method":"ping"}`); resp.Error != nil {
		t.Fatalf("client after release: %+v", resp.Error)
	}
}

// An idle connection is reaped after IdleTimeout, while a client that keeps
// sending is never cut off (the deadline is re-armed per read).
func TestHandleConn_IdleTimeout(t *testing.T) {
	path := testSocketPath(t)
	srv := NewServer(Config{SocketPath: path, Engine: &fakeSink{}, IdleTimeout: 300 * time.Millisecond})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Stop)

	active := newClient(t, path)
	for i := 0; i < 8; i++ { // ~800ms of steady traffic, well past the timeout
		if resp := active.roundTrip(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); resp.Error != nil {
			t.Fatalf("active client ping %d: %+v", i, resp.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}

	idle := dial(t, path)
	_ = idle.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 16)
	if _, err := idle.Read(buf); err == nil {
		t.Fatal("idle connection produced data, want it closed")
	} else if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
		t.Fatal("idle connection still open after 5s, want the server to close it after 300ms")
	}
}

// lock_file must surface an engine lock conflict instead of reporting the
// holder's lock as the caller's own success.
func TestDispatch_LockFileConflictIsAnError(t *testing.T) {
	existing := LockInfo{Path: "src/app.go", Owner: "alice", Reason: "refactor", ExpiresAt: time.Now().Add(time.Hour)}
	sink := &fakeSink{lockErr: &LockConflictError{Path: "src/app.go", Existing: existing}}
	s := newTestServer(sink)

	resp := s.dispatch(&Request{JSONRPC: "2.0", ID: 1, Method: "lock_file",
		Params: params(t, `{"file_path":"src/app.go","owner":"bob"}`)})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("conflict = %+v, want -32602 error", resp)
	}
	if resp.Result != nil {
		t.Errorf("conflict must not carry a success result, got %+v", resp.Result)
	}
	if !strings.Contains(resp.Error.Message, "alice") {
		t.Errorf("message %q should name the current owner", resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Error)
	if !bytes.Contains(b, []byte(`"status":"conflict"`)) || !bytes.Contains(b, []byte(`"owner":"alice"`)) {
		t.Errorf("error data %s should describe the existing lock", b)
	}
	if !errors.Is(sink.lockErr, ErrLockConflict) {
		t.Error("LockConflictError must match ErrLockConflict")
	}
}
