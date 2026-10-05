package ipc

import (
	"encoding/json"
	"testing"
)

func TestResponse_SuccessResultIsAlwaysPresent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, "null"}, {"false", false, "false"}, {"zero", 0, "0"}, {"empty string", "", `""`}, {"empty map", map[string]any{}, "{}"}, {"empty slice", []any{}, "[]"}, {"typed nil", map[string]any(nil), "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(Response{JSONRPC: "2.0", ID: 0, Result: tc.value})
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			if err = json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			result, present := m["result"]
			if !present || string(result) != tc.want || string(m["id"]) != "0" {
				t.Fatalf("wrong success envelope: %s", b)
			}
			if _, present := m["error"]; present {
				t.Fatalf("success contains error: %s", b)
			}
		})
	}
}

func TestResponse_ErrorOmitsStaleResult(t *testing.T) {
	b, err := json.Marshal(Response{JSONRPC: "2.0", ID: nil, Result: "stale", Error: &RPCError{Code: -32603, Message: "failure"}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, present := m["result"]; present {
		t.Fatalf("error contains result: %s", b)
	}
	if _, present := m["error"]; !present || string(m["id"]) != "null" {
		t.Fatalf("wrong error envelope: %s", b)
	}
	if _, err := json.Marshal(Response{JSONRPC: "2.0", Result: make(chan int)}); err == nil {
		t.Fatal("unsupported result did not error")
	}
	if _, err := json.Marshal(Response{JSONRPC: "2.0", Error: &RPCError{Code: -32603, Data: make(chan int)}}); err == nil {
		t.Fatal("unsupported error data did not error")
	}
}
