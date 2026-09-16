package ipc

import (
	"strings"
	"testing"
	"time"
)

// panickingSink panics on Ping while inheriting every other fake behavior.
type panickingSink struct {
	fakeSink
}

func (p *panickingSink) Ping() error {
	panic("ipc hardening test: sink is broken")
}

// Pins the per-conn panic recovery: a sink panic during dispatch must close
// the offending connection and leave the daemon serving, never crash the
// process. Pre-fix, the panic propagated out of the per-conn goroutine and
// killed the whole binary.
func TestHandleConn_RecoversFromSinkPanic(t *testing.T) {
	srv := NewServer(Config{
		SocketPath:  testSocketPath(t),
		Engine:      &panickingSink{},
		IdleTimeout: time.Second,
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Stop)

	c1 := newClient(t, srv.cfg.SocketPath)
	if _, err := c1.w.WriteString(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}` + "\n"); err != nil {
		t.Fatalf("write panicking request: %v", err)
	}
	if err := c1.w.Flush(); err != nil {
		t.Fatalf("flush panicking request: %v", err)
	}

	// The recovered handler closes this connection: a read must fail, not hang.
	_ = c1.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c1.r.ReadString('\n'); err == nil {
		t.Fatal("panicking request unexpectedly produced a response")
	}

	// The daemon must still serve fresh connections after the panic.
	c2 := newClient(t, srv.cfg.SocketPath)
	resp := c2.roundTrip(t, `{"jsonrpc":"2.0","id":2,"method":"list_locks","params":{}}`)
	if resp.Error != nil || resp.ID == nil {
		t.Fatalf("daemon did not survive the sink panic: %+v", resp)
	}
}

// Pins the response write deadline: a client that stops reading while a large
// reply is in flight must not pin the handler goroutine in the write flush
// forever — the write deadline reaps it and the slot is released.
func TestHandleConn_WriteDeadlineReapsWedgedClient(t *testing.T) {
	sink := &fakeSink{listLocks: []LockInfo{{
		Path:   "big.go",
		Reason: strings.Repeat("x", 1<<25), // ~32 MB reply: exceeds every pipe buffer so a non-reading client genuinely wedges the write
	}}}
	srv := NewServer(Config{
		SocketPath:  testSocketPath(t),
		Engine:      sink,
		IdleTimeout: 300 * time.Millisecond,
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Stop)

	c := newClient(t, srv.cfg.SocketPath)
	if _, err := c.w.WriteString(`{"jsonrpc":"2.0","id":1,"method":"list_locks","params":{}}` + "\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := c.w.Flush(); err != nil {
		t.Fatalf("flush request: %v", err)
	}
	// Wedged: deliberately never read the ~1 MB reply.

	waitFor(t, func() bool { return srv.ConnectedCount() == 0 },
		"wedged client to be reaped by the write deadline")

	// The reaped connection must be closed server-side, not left open.
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.r.ReadString('\n'); err == nil {
		t.Fatal("wedged connection still open after the write deadline")
	}
}
