package ipc

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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

// ---------------------------------------------------------------------------
// Response write-deadline reaping
// ---------------------------------------------------------------------------

// ipcDeadline is what a real socket reports when an armed deadline expires
// before the operation completes. It satisfies net.Error so handleConn's
// `errors.As(err, &ne) && ne.Timeout()` classification sees a timeout rather
// than a client disconnect.
type ipcDeadline struct{}

func (ipcDeadline) Error() string   { return "i/o timeout" }
func (ipcDeadline) Timeout() bool   { return true }
func (ipcDeadline) Temporary() bool { return true }

// wedgedConn is an injected net.Conn standing in for a client that stops
// reading while a reply is in flight.
//
// WHY IT EXISTS. The contract under test is that the response write is bounded:
// handleConn arms SetWriteDeadline (socket.go) before flushing, so a client
// that never drains cannot pin the handler goroutine forever. The first version
// of this test produced that wedge for real — a ~32 MB list_locks reply written
// down a live Windows named pipe while the client deliberately never read. That
// made the outcome depend on how much of the payload the OS pipe buffer happened
// to absorb before the deadline expired, so the assertion flipped between runs
// (measured: 5 of 6 isolated runs failing, on a pristine checkout as often as on
// a modified one). Pipe absorption is a property of Windows, not of WrongTrace,
// so it does not belong in the test.
//
// Injecting the peer removes that variable: Write blocks until the deadline
// handleConn itself armed has expired, then reports the timeout a real socket
// would. The wedge becomes guaranteed instead of size- and luck-dependent, and
// the reply can be ordinary-sized.
//
// NOT A MOCK OF THE BEHAVIOR UNDER TEST. Only the transport is injected;
// acceptLoop's semaphore/track bookkeeping, the read loop, dispatch, and the
// write+deadline sequence are the real production code, reached through a fake
// listener. And the guard stays honest rather than tautological: Write waits on
// the deadline the PRODUCTION code supplied via SetWriteDeadline. If that call
// is ever dropped, writeDeadline stays zero, Write blocks until Close, Close
// never arrives because handleConn is stuck in Write, and the bounded wait below
// fails — so the test still catches the regression it pins.
type wedgedConn struct {
	req     []byte // one framed JSON-RPC request line for the server to read
	readPos int

	writeDeadline atomic.Value // time.Time, armed by handleConn
	readMu        sync.Mutex
	readDeadline  time.Time

	writeCalled   atomic.Bool
	writeTimedOut atomic.Bool

	closeOnce sync.Once
	closeCh   chan struct{}
}

func newWedgedConn(req string) *wedgedConn {
	c := &wedgedConn{req: []byte(req + "\n"), closeCh: make(chan struct{})}
	c.writeDeadline.Store(time.Time{})
	return c
}

func (c *wedgedConn) Read(p []byte) (int, error) {
	if c.readPos < len(c.req) {
		n := copy(p, c.req[c.readPos:])
		c.readPos += n
		return n, nil
	}
	// No further input. Honor an armed read deadline the way a real conn would,
	// and stop waiting once the connection is closed, so this fixture can never
	// itself leave the handler blocked in Read.
	c.readMu.Lock()
	dl := c.readDeadline
	c.readMu.Unlock()
	wait := time.Until(dl)
	if !dl.IsZero() {
		if wait < 0 {
			wait = 0
		}
	} else {
		wait = 50 * time.Millisecond
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return 0, ipcDeadline{}
	case <-c.closeCh:
		return 0, io.EOF
	}
}

// Write blocks until the deadline production armed has expired, reproducing a
// peer that never reads.
func (c *wedgedConn) Write(p []byte) (int, error) {
	c.writeCalled.Store(true)
	dl, _ := c.writeDeadline.Load().(time.Time)
	if dl.IsZero() {
		// No write deadline armed: behave like an unbounded write. This is the
		// regression path, and it is what makes the bounded wait below fail.
		<-c.closeCh
		return 0, net.ErrClosed
	}
	wait := time.Until(dl)
	if wait < 0 {
		wait = 0
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		c.writeTimedOut.Store(true)
		return 0, ipcDeadline{}
	case <-c.closeCh:
		return 0, net.ErrClosed
	}
}

func (c *wedgedConn) Close() error {
	c.closeOnce.Do(func() { close(c.closeCh) })
	return nil
}

func (c *wedgedConn) SetWriteDeadline(t time.Time) error {
	c.writeDeadline.Store(t)
	return nil
}

func (c *wedgedConn) SetDeadline(t time.Time) error {
	c.writeDeadline.Store(t)
	c.readMu.Lock()
	c.readDeadline = t
	c.readMu.Unlock()
	return nil
}

func (c *wedgedConn) SetReadDeadline(t time.Time) error {
	c.readMu.Lock()
	c.readDeadline = t
	c.readMu.Unlock()
	return nil
}

func (c *wedgedConn) LocalAddr() net.Addr  { return nil }
func (c *wedgedConn) RemoteAddr() net.Addr { return nil }

// wedgedListener feeds the injected conn to the real acceptLoop exactly once,
// then reports a closed listener so the loop returns.
type wedgedListener struct {
	done chan net.Conn
	once sync.Once
}

func (l *wedgedListener) Accept() (net.Conn, error) {
	select {
	case c, ok := <-l.done:
		if !ok || c == nil {
			return nil, net.ErrClosed
		}
		return c, nil
	case <-time.After(10 * time.Second):
		return nil, net.ErrClosed
	}
}

func (l *wedgedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *wedgedListener) Addr() net.Addr { return nil }

// TestHandleConn_WriteDeadlineReapsWedgedClient pins the response write
// deadline: a client that stops reading while a reply is in flight must not pin
// the handler goroutine in the write flush forever — the write deadline reaps it
// and the slot is released.
func TestHandleConn_WriteDeadlineReapsWedgedClient(t *testing.T) {
	const idle = 150 * time.Millisecond

	sink := &fakeSink{listLocks: []LockInfo{{Path: "big.go", Reason: strings.Repeat("x", 1<<16)}}}
	srv := NewServer(Config{
		SocketPath:  testSocketPath(t),
		Engine:      sink,
		MaxConns:    4,
		IdleTimeout: idle,
	})
	if srv.cfg.MaxConns <= 0 {
		t.Fatalf("MaxConns must be honored, got %d", srv.cfg.MaxConns)
	}

	// Drive the REAL acceptLoop, so semaphore accounting, track() and its
	// closing of the conn are production code, not test scaffolding.
	conn := newWedgedConn(`{"jsonrpc":"2.0","id":1,"method":"list_locks","params":{}}`)
	ln := &wedgedListener{done: make(chan net.Conn, 1)}
	srv.ln = ln
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.wg.Add(1)
	go srv.acceptLoop(ctx)
	ln.done <- conn

	// The reap must happen on its own: bounded wait, generous enough to stay
	// quiet on a loaded runner, far short of "never" if the write is unbounded.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if conn.writeCalled.Load() && srv.ConnectedCount() == 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if !conn.writeCalled.Load() {
		t.Fatal("handleConn never attempted the response write, so the write path was not exercised")
	}
	if !conn.writeTimedOut.Load() {
		t.Fatalf("the wedged write was never reaped by its deadline: production must arm "+
			"SetWriteDeadline before flushing the reply (ConnectedCount=%d)", srv.ConnectedCount())
	}
	if n := srv.ConnectedCount(); n != 0 {
		t.Fatalf("ConnectedCount = %d after the reap, want 0 (slot must be released)", n)
	}
	select {
	case <-conn.closeCh:
	default:
		t.Fatal("reaped connection was not closed by the handler cleanup")
	}

	// Reaping a wedged client must leave the server teardown-able.
	srv.Stop()
}
