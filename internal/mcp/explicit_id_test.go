package mcp

import (
	"encoding/json"
	"testing"
)

func TestServeStdio_ExplicitNullIDGetsResponse(t *testing.T) {
	out := runStdioSession(t, &fakeSink{}, []string{
		`{"jsonrpc":"2.0","id":null,"method":"initialize"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	})
	if len(out) != 1 {
		t.Fatalf("want one response for explicit id:null and none for the notification, got %d: %q", len(out), out)
	}

	var response map[string]interface{}
	if err := json.Unmarshal([]byte(out[0]), &response); err != nil {
		t.Fatalf("response is not valid JSON: %v (%q)", err, out[0])
	}
	id, present := response["id"]
	if !present || id != nil {
		t.Fatalf("response id = %#v (present=%v), want explicit null", id, present)
	}
	if _, ok := response["result"]; !ok {
		t.Fatalf("explicit id:null request did not receive a result: %q", out[0])
	}
}
