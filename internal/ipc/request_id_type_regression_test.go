package ipc

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type idAdmissionSink struct {
	fakeSink
	calls atomic.Int64
}

func (s *idAdmissionSink) Ping() error { s.calls.Add(1); return nil }

func TestHandleConn_InvalidIDTypesNeverDispatch(t *testing.T) {
	path := testSocketPath(t)
	sink := &idAdmissionSink{}
	srv := NewServer(Config{SocketPath: path, Engine: sink})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	c := newClient(t, path)
	for _, id := range []string{"true", "false", "[]", "[1]", "{}", `{"x":1}`} {
		before := sink.calls.Load()
		c.send(t, fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"method\":\"ping\",\"id\":%s}\n", id))
		r := c.readResponse(t, 3*time.Second)
		if r.Error == nil || r.Error.Code != -32600 || r.ID != nil || sink.calls.Load() != before {
			t.Fatalf("invalidID%s reachedmethodorwrongreply: %+v calls=%d", id, r, sink.calls.Load()-before)
		}
	}
	c.send(t, "{\"jsonrpc\":\"2.0\",\"method\":\"ping\"}\n{\"jsonrpc\":\"2.0\",\"method\":\"ping\",\"id\":43}\n")
	r := c.readResponse(t, 3*time.Second)
	if r.Error != nil || r.ID != float64(43) || sink.calls.Load() != 2 {
		t.Fatalf("notification/control behavior changed: %+v calls=%d", r, sink.calls.Load())
	}
}
