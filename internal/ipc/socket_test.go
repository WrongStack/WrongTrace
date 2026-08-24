package ipc

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// fakeSink is an in-memory EngineSink used to observe dispatch side effects
// and to inject sink failures for error-code assertions.
type fakeSink struct {
	reportErr   error
	healthErr   error
	pingErr     error
	healthOut   FileHealthReply
	gotReport   TelemetryReport
	gotPath     string
	reportCalls int
	healthCalls int
	pingCalls   int
}

func (f *fakeSink) ReportRun(r TelemetryReport) error {
	f.reportCalls++
	f.gotReport = r
	return f.reportErr
}

func (f *fakeSink) RecordReadEvent(rec db.FileReadRecord) error {
	return nil
}

func (f *fakeSink) FileHealth(p string) (FileHealthReply, error) {
	f.healthCalls++
	f.gotPath = p
	return f.healthOut, f.healthErr
}

func (f *fakeSink) CheckGuardrail(p string) (GuardrailResult, error) {
	return GuardrailResult{Allowed: true, HealthScore: 100}, nil
}

func (f *fakeSink) LockFileWithOptions(path, reason, owner, ownerRunID string, ttl time.Duration) LockInfo {
	return LockInfo{Path: path, Reason: reason, Owner: owner, OwnerRunID: ownerRunID, LockedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(ttl)}
}

func (f *fakeSink) UnlockFile(path string) {}

func (f *fakeSink) ListLocks() []LockInfo {
	return []LockInfo{}
}

func (f *fakeSink) GetFileReadStats(filePath string) (db.FileReadStats, error) {
	return db.FileReadStats{FilePath: filePath}, nil
}

func (f *fakeSink) GetRecentFileEvents(filePath string, limit int) ([]db.EventRecord, error) {
	return []db.EventRecord{}, nil
}

func (f *fakeSink) Ping() error {
	f.pingCalls++
	return f.pingErr
}

func (f *fakeSink) RecordIPCTraffic(rec IPCTrafficRecord) {}

func newTestServer(sink *fakeSink) *Server {
	return NewServer(Config{Engine: sink})
}

// params builds a map exactly the way production code receives one: by
// json.Unmarshal into map[string]interface{}, so numbers arrive as float64
// and mapToStruct's re-marshal path is exercised faithfully.
func params(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad fixture %q: %v", raw, err)
	}
	return m
}

func approx(got, want float64) bool {
	d := got - want
	return d < 1e-9 && d > -1e-9
}

func TestDispatch_ReportRun_Success(t *testing.T) {
	sink := &fakeSink{}
	s := newTestServer(sink)

	req := &Request{
		JSONRPC: "2.0",
		ID:      7,
		Method:  "telemetry/report_run",
		Params: params(t, `{
			"run_id":"c62a8b9f","task_id":"TASK-402","agent_name":"Claude-Code",
			"model_name":"claude-3-7-sonnet","provider":"anthropic",
			"prompt_tokens":42000,"completion_tokens":1200,
			"cost_usd":0.144,"intent":"Refactor auth"
		}`),
	}
	resp := s.dispatch(req)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.JSONRPC != "2.0" || resp.ID != 7 {
		t.Errorf("envelope wrong: jsonrpc=%q id=%v", resp.JSONRPC, resp.ID)
	}
	res, ok := resp.Result.(map[string]string)
	if !ok || res["status"] != "ok" {
		t.Errorf("result = %#v, want {status:ok}", resp.Result)
	}

	// The typed struct the engine received must match every param.
	g := sink.gotReport
	switch {
	case g.RunID != "c62a8b9f", g.TaskID != "TASK-402", g.AgentName != "Claude-Code",
		g.ModelName != "claude-3-7-sonnet", g.Provider != "anthropic",
		g.PromptTokens != 42000, g.CompletionTokens != 1200,
		g.Intent != "Refactor auth":
		t.Errorf("sink received wrong report: %+v", g)
	}
	if !approx(g.CostUSD, 0.144) {
		t.Errorf("cost_usd: got %v, want 0.144", g.CostUSD)
	}
	if sink.reportCalls != 1 {
		t.Errorf("ReportRun calls = %d, want 1", sink.reportCalls)
	}
}

func TestDispatch_ReportRun_MissingRunID(t *testing.T) {
	sink := &fakeSink{}
	s := newTestServer(sink)

	req := &Request{JSONRPC: "2.0", ID: 1, Method: "telemetry/report_run",
		Params: params(t, `{"task_id":"T","model_name":"m","provider":"p"}`)}
	resp := s.dispatch(req)

	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("want -32602, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "run_id is required") {
		t.Errorf("message %q should mention run_id", resp.Error.Message)
	}
	if sink.reportCalls != 0 {
		t.Errorf("sink called despite validation failure (%d calls)", sink.reportCalls)
	}
}

func TestDispatch_ReportRun_NilParams(t *testing.T) {
	sink := &fakeSink{}
	s := newTestServer(sink)

	resp := s.dispatch(&Request{JSONRPC: "2.0", ID: 2, Method: "telemetry/report_run"})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("nil params: want -32602, got %+v", resp.Error)
	}
}

func TestDispatch_ReportRun_SinkFailure(t *testing.T) {
	sink := &fakeSink{reportErr: errors.New("db is locked")}
	s := newTestServer(sink)

	req := &Request{JSONRPC: "2.0", ID: 3, Method: "telemetry/report_run",
		Params: params(t, `{"run_id":"r1","task_id":"t","model_name":"m","provider":"p"}`)}
	resp := s.dispatch(req)

	if resp.Error == nil || resp.Error.Code != -32010 {
		t.Fatalf("want -32010, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "db is locked") {
		t.Errorf("error message should propagate sink failure, got %q", resp.Error.Message)
	}
}

func TestDispatch_FileHealth_Success(t *testing.T) {
	sink := &fakeSink{healthOut: FileHealthReply{
		FilePath: "src/auth.go", HealthScore: 36, IsFragile: true,
		RecentThrashingCount: 8, Warning: "8 edits in the last 24h across 3 signatures",
	}}
	s := newTestServer(sink)

	req := &Request{JSONRPC: "2.0", ID: 4, Method: "telemetry/file_health",
		Params: params(t, `{"file_path":"src/auth.go"}`)}
	resp := s.dispatch(req)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	h, ok := resp.Result.(FileHealthReply)
	if !ok {
		t.Fatalf("result type = %T, want FileHealthReply", resp.Result)
	}
	if h.HealthScore != 36 || !h.IsFragile || h.RecentThrashingCount != 8 || h.Warning == "" {
		t.Errorf("health payload wrong: %+v", h)
	}
	if sink.gotPath != "src/auth.go" || sink.healthCalls != 1 {
		t.Errorf("sink path=%q calls=%d", sink.gotPath, sink.healthCalls)
	}
}

func TestDispatch_FileHealth_MissingPath(t *testing.T) {
	sink := &fakeSink{}
	s := newTestServer(sink)

	resp := s.dispatch(&Request{JSONRPC: "2.0", ID: 5, Method: "telemetry/file_health",
		Params: params(t, `{}`)})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("want -32602, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "file_path is required") {
		t.Errorf("message %q should mention file_path", resp.Error.Message)
	}
	if sink.healthCalls != 0 {
		t.Errorf("sink called despite validation failure (%d calls)", sink.healthCalls)
	}
}

func TestDispatch_FileHealth_SinkFailure(t *testing.T) {
	sink := &fakeSink{healthErr: errors.New("query blew up")}
	s := newTestServer(sink)

	resp := s.dispatch(&Request{JSONRPC: "2.0", ID: 6, Method: "telemetry/file_health",
		Params: params(t, `{"file_path":"x.go"}`)})
	if resp.Error == nil || resp.Error.Code != -32011 {
		t.Fatalf("want -32011, got %+v", resp.Error)
	}
}

func TestDispatch_Ping(t *testing.T) {
	sink := &fakeSink{}
	s := newTestServer(sink)

	resp := s.dispatch(&Request{JSONRPC: "2.0", ID: 8, Method: "ping"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if res, ok := resp.Result.(map[string]string); !ok || res["pong"] != "1" {
		t.Errorf("result = %#v, want {pong:1}", resp.Result)
	}
	if sink.pingCalls != 1 {
		t.Errorf("Ping calls = %d, want 1", sink.pingCalls)
	}

	// Ping failure surfaces as -32000.
	sink.pingErr = errors.New("unreachable")
	resp = s.dispatch(&Request{JSONRPC: "2.0", ID: 9, Method: "ping"})
	if resp.Error == nil || resp.Error.Code != -32000 {
		t.Fatalf("ping failure: want -32000, got %+v", resp.Error)
	}
}

func TestDispatch_UnknownMethod(t *testing.T) {
	sink := &fakeSink{}
	s := newTestServer(sink)

	resp := s.dispatch(&Request{JSONRPC: "2.0", ID: 10, Method: "workspace/edit"})
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("want -32601, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "workspace/edit") {
		t.Errorf("message %q should echo the method", resp.Error.Message)
	}
	if sink.reportCalls+sink.healthCalls+sink.pingCalls != 0 {
		t.Error("sink touched by unknown method")
	}
}

func TestDispatch_ParamTypeMismatch(t *testing.T) {
	sink := &fakeSink{}
	s := newTestServer(sink)

	// run_id as a number where the struct wants a string: decode must fail
	// and surface as an invalid-params error, never a silent zero value.
	resp := s.dispatch(&Request{JSONRPC: "2.0", ID: 11, Method: "telemetry/report_run",
		Params: params(t, `{"run_id":12345}`)})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("type mismatch: want -32602, got %+v", resp.Error)
	}
	if sink.reportCalls != 0 {
		t.Error("sink called despite decode failure")
	}
}

func TestMapToStruct(t *testing.T) {
	t.Run("nil params is a no-op", func(t *testing.T) {
		var out TelemetryReport
		out.RunID = "preset"
		if err := mapToStruct(nil, &out); err != nil {
			t.Fatalf("nil params: %v", err)
		}
		if out.RunID != "preset" {
			t.Errorf("nil params mutated out: %+v", out)
		}
	})

	t.Run("decodes numbers from json-generic maps", func(t *testing.T) {
		var out TelemetryReport
		err := mapToStruct(params(t, `{"run_id":"r","prompt_tokens":42,"cost_usd":0.5}`), &out)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.RunID != "r" || out.PromptTokens != 42 || !approx(out.CostUSD, 0.5) {
			t.Errorf("decoded wrong: %+v", out)
		}
	})

	t.Run("unknown fields ignored, missing fields zero", func(t *testing.T) {
		var out TelemetryReport
		if err := mapToStruct(params(t, `{"run_id":"r","bogus":true}`), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.RunID != "r" || out.TaskID != "" || out.CostUSD != 0 {
			t.Errorf("unknown/missing field handling wrong: %+v", out)
		}
	})

	t.Run("type mismatch errors", func(t *testing.T) {
		var out TelemetryReport
		if err := mapToStruct(params(t, `{"prompt_tokens":"lots"}`), &out); err == nil {
			t.Error("string into int64 field should fail")
		}
	})

	t.Run("non-pointer target errors", func(t *testing.T) {
		if err := mapToStruct(params(t, `{"run_id":"r"}`), TelemetryReport{}); err == nil {
			t.Error("non-pointer out should fail json.Unmarshal")
		}
	})
}

// TestResponseWireShape pins the JSON-RPC 2.0 wire contract: an error
// response carries "error" and omits "result"; a success response does the
// reverse; the id always echoes.
func TestResponseWireShape(t *testing.T) {
	errResp := Response{JSONRPC: "2.0", ID: 42, Error: &RPCError{Code: -32602, Message: "file_path is required"}}
	b, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `"error":{"code":-32602,"message":"file_path is required"}`) {
		t.Errorf("error object malformed: %s", s)
	}
	if strings.Contains(s, `"result"`) {
		t.Errorf("error response must omit result: %s", s)
	}
	if !strings.Contains(s, `"id":42`) || !strings.Contains(s, `"jsonrpc":"2.0"`) {
		t.Errorf("envelope fields missing: %s", s)
	}

	okResp := Response{JSONRPC: "2.0", ID: 43, Result: map[string]string{"status": "ok"}}
	b, _ = json.Marshal(okResp)
	if s := string(b); !strings.Contains(s, `"result":{"status":"ok"}`) || strings.Contains(s, `"error"`) {
		t.Errorf("success response malformed: %s", s)
	}
}

func TestExtendedIPCMethods(t *testing.T) {
	sink := &fakeSink{}
	srv := newTestServer(sink)

	// 1. telemetry/report_file_read
	resp := srv.dispatch(&Request{
		Method: "telemetry/report_file_read",
		Params: params(t, `{"file_path":"package.json","line_count":1,"model_name":"probe-model","tokens_consumed":1}`),
		ID:     1,
	})
	if resp.Error != nil {
		t.Fatalf("report_file_read failed: %v", resp.Error)
	}

	// 2. check_guardrail
	resp = srv.dispatch(&Request{
		Method: "check_guardrail",
		Params: params(t, `{"path":"package.json"}`),
		ID:     2,
	})
	if resp.Error != nil {
		t.Fatalf("check_guardrail failed: %v", resp.Error)
	}

	// 3. lock_file & unlock_file
	resp = srv.dispatch(&Request{
		Method: "lock_file",
		Params: params(t, `{"path":".temp_files/test","reason":"probe","ttl_seconds":60}`),
		ID:     3,
	})
	if resp.Error != nil {
		t.Fatalf("lock_file failed: %v", resp.Error)
	}

	resp = srv.dispatch(&Request{
		Method: "unlock_file",
		Params: params(t, `{"path":".temp_files/test"}`),
		ID:     4,
	})
	if resp.Error != nil {
		t.Fatalf("unlock_file failed: %v", resp.Error)
	}

	// 4. list_locks
	resp = srv.dispatch(&Request{
		Method: "list_locks",
		ID:     5,
	})
	if resp.Error != nil {
		t.Fatalf("list_locks failed: %v", resp.Error)
	}

	// 5. rpc.discover & system.listMethods
	resp = srv.dispatch(&Request{
		Method: "rpc.discover",
		ID:     6,
	})
	if resp.Error != nil {
		t.Fatalf("rpc.discover failed: %v", resp.Error)
	}

	resp = srv.dispatch(&Request{
		Method: "system.listMethods",
		ID:     7,
	})
	if resp.Error != nil {
		t.Fatalf("system.listMethods failed: %v", resp.Error)
	}

	// 6. get_file_read_stats & get_file_diff_history
	resp = srv.dispatch(&Request{
		Method: "get_file_read_stats",
		Params: params(t, `{"file_path":"main.go"}`),
		ID:     8,
	})
	if resp.Error != nil {
		t.Fatalf("get_file_read_stats failed: %v", resp.Error)
	}

	resp = srv.dispatch(&Request{
		Method: "get_file_diff_history",
		Params: params(t, `{"file_path":"main.go","limit":10}`),
		ID:     9,
	})
	if resp.Error != nil {
		t.Fatalf("get_file_diff_history failed: %v", resp.Error)
	}

	// 7. ConnectedCount
	if count := srv.ConnectedCount(); count != 0 {
		t.Errorf("expected 0 connected count, got %d", count)
	}

	// 8. isClientDisconnect
	if !isClientDisconnect(errors.New("broken pipe")) {
		t.Errorf("expected broken pipe to be client disconnect")
	}
	if isClientDisconnect(nil) {
		t.Errorf("expected nil error to NOT be client disconnect")
	}
}
