package ipc

import (
	"testing"
	"time"
)

func TestHandleConn_MethodMustBePresentString(t *testing.T) {
	path := testSocketPath(t)
	sink := &idAdmissionSink{}
	srv := NewServer(Config{SocketPath: path, Engine: sink})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	c := newClient(t, path)
	for _, body := range []string{`{"jsonrpc":"2.0","id":43}`, `{"jsonrpc":"2.0","method":null,"id":43}`, `{"jsonrpc":"2.0","method":1,"id":43}`, `{}`, `{"jsonrpc":"2.0","method":null}`} {
		before := sink.calls.Load()
		c.send(t, body+"\n")
		r := c.readResponse(t, 3*time.Second)
		if r.Error == nil || r.Error.Code != -32600 || r.ID != nil || sink.calls.Load() != before {
			t.Fatalf("invalidmethodaccepted/suppressed: %s → %+v", body, r)
		}
	}
	for _, method := range []string{`""`, `"unknown"`} {
		c.send(t, `{"jsonrpc":"2.0","method":`+method+`,"id":43}`+"\n")
		r := c.readResponse(t, 3*time.Second)
		if r.Error == nil || r.Error.Code != -32601 || r.ID != float64(43) {
			t.Fatalf("presentunknownstringmethodchanged: %+v", r)
		}
	}
	c.send(t, `{"jsonrpc":"2.0","method":"ping"}`+"\n"+`{"jsonrpc":"2.0","method":"ping","id":43}`+"\n")
	r := c.readResponse(t, 3*time.Second)
	if r.Error != nil || r.ID != float64(43) || sink.calls.Load() != 2 {
		t.Fatalf("validnotification/recoverychanged: %+v calls%d", r, sink.calls.Load())
	}
}
