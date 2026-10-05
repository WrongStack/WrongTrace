package ipc

import (
	"testing"
	"time"
)

func TestHandleConn_SemanticEnvelopeErrorsAreInvalidRequests(t *testing.T) {
	_, path := startLiveServer(t, nil)
	c := newClient(t, path)
	for _, tc := range []struct {
		body string
		code int
	}{
		{"{", -32700}, {`{"method":`, -32700},
		{`{"jsonrpc":"2.0","method":1,"id":43}`, -32600}, {`{"jsonrpc":2,"method":"ping","id":43}`, -32600},
		{"42", -32600}, {"[]", -32600}, {"true", -32600}, {`"string"`, -32600},
	} {
		c.send(t, tc.body+"\n")
		r := c.readResponse(t, 3*time.Second)
		if r.Error == nil || r.Error.Code != tc.code || r.ID != nil {
			t.Fatalf("%s → %+v; wantcode%d/idnull", tc.body, r, tc.code)
		}
	}
	c.send(t, `{"jsonrpc":"2.0","method":"ping","id":43}`+"\n")
	r := c.readResponse(t, 3*time.Second)
	if r.Error != nil || r.ID != float64(43) {
		t.Fatalf("connection did not recover: %+v", r)
	}
}

func TestHandleConn_NullEnvelopeIsNotNotification(t *testing.T) {
	_, path := startLiveServer(t, nil)
	c := newClient(t, path)
	for pass := 0; pass < 2; pass++ {
		c.send(t, "null\n"+`{"jsonrpc":"2.0","method":"ping","id":43}`+"\n")
		r := c.readResponse(t, 3*time.Second)
		if r.Error == nil || r.Error.Code != -32600 || r.ID != nil {
			t.Fatalf("null envelope silently treated as notification: %+v", r)
		}
		r = c.readResponse(t, 3*time.Second)
		if r.Error != nil || r.ID != float64(43) {
			t.Fatalf("following valid request lost: %+v", r)
		}
	}
}
