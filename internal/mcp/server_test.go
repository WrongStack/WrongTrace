package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
)

// fakeSink is an in-memory EngineSink for observing tool side effects and
// injecting failures.
type fakeSink struct {
	reportErr error
	healthErr error
	runID     string
	health    ipc.FileHealthReply

	gotModel, gotProvider, gotTaskID, gotIntent string
	gotPrompt, gotCompletion                    int64
	gotCost                                     float64
	gotPath                                     string
	reportCalls, healthCalls, readCalls         int
	readErr                                     error
	readStatsErr                                error
	readStats                                   db.FileReadStats
	lastReadRecord                              db.FileReadRecord
	isLocked                                    bool
	lockReason                                  string
}

func (f *fakeSink) IsFileLocked(path string) (bool, string) {
	return f.isLocked, f.lockReason
}

func (f *fakeSink) LockFile(path, reason string) {
	f.isLocked = true
	f.lockReason = reason
}

func (f *fakeSink) UnlockFile(path string) {
	f.isLocked = false
	f.lockReason = ""
}

func (f *fakeSink) ReportRunMCP(model, provider, taskID, intent string, promptTokens, completionTokens int64, cost float64) (string, error) {
	f.reportCalls++
	f.gotModel, f.gotProvider, f.gotTaskID, f.gotIntent = model, provider, taskID, intent
	f.gotPrompt, f.gotCompletion, f.gotCost = promptTokens, completionTokens, cost
	return f.runID, f.reportErr
}

func (f *fakeSink) FileHealth(path string) (ipc.FileHealthReply, error) {
	f.healthCalls++
	f.gotPath = path
	return f.health, f.healthErr
}

func (f *fakeSink) RecordReadEvent(rec db.FileReadRecord) error {
	f.readCalls++
	f.lastReadRecord = rec
	return f.readErr
}

func (f *fakeSink) GetFileReadStats(filePath string) (db.FileReadStats, error) {
	f.gotPath = filePath
	return f.readStats, f.readStatsErr
}

func (f *fakeSink) GetRecentEvents(limit int, repoFilter ...string) ([]db.EventRecord, error) {
	return []db.EventRecord{
		{
			EventID:      "ev-1",
			FilePath:     f.gotPath,
			Signature:    "function:auth.go::Login",
			NodeType:     "function",
			Action:       "MODIFIED",
			AddedLines:   3,
			DeletedLines: 1,
			DiffSnippet:  "+ login_v2\n- login_v1",
		},
	}, nil
}

func (f *fakeSink) GetRecentFileEvents(filePath string, limit int) ([]db.EventRecord, error) {
	f.gotPath = filePath
	return []db.EventRecord{
		{
			EventID:      "ev-1",
			FilePath:     filePath,
			Signature:    "function:auth.go::Login",
			NodeType:     "function",
			Action:       "MODIFIED",
			AddedLines:   3,
			DeletedLines: 1,
			DiffSnippet:  "+ login_v2\n- login_v1",
		},
	}, nil
}

func params(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad fixture %q: %v", raw, err)
	}
	return m
}

func toolCallReq(id int, name, argsJSON string) *jsonRPCRequest {
	req := &jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: "tools/call",
		Params: map[string]interface{}{"name": name}}
	if argsJSON != "" {
		req.Params["arguments"] = params(&testing.T{}, argsJSON)
	}
	return req
}

func approx(got, want float64) bool {
	d := got - want
	return d < 1e-9 && d > -1e-9
}

// ---------------------------------------------------------------
// dispatch
// ---------------------------------------------------------------

// wireResult marshals resp.Result to JSON and re-decodes it into generic
// maps/slices — i.e. exactly what an MCP client sees on the wire. This keeps
// the assertions independent of the concrete Go types dispatch builds.
func wireResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	m := map[string]interface{}{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal result: %v (%s)", err, b)
	}
	return m
}

func TestDispatch_Initialize(t *testing.T) {
	resp := dispatch(&fakeSink{}, &jsonRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize",
		Params: params(t, `{"protocolVersion":"2024-11-05"}`)})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	res := wireResult(t, resp)
	if res["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v", res["protocolVersion"])
	}
	info, _ := res["serverInfo"].(map[string]interface{})
	if info == nil || info["name"] != "wrongtrace" {
		t.Errorf("serverInfo = %#v", res["serverInfo"])
	}
	caps, _ := res["capabilities"].(map[string]interface{})
	if caps == nil {
		t.Fatalf("capabilities missing: %#v", res)
	}
	if _, ok := caps["tools"].(map[string]interface{}); !ok {
		t.Error("capabilities.tools missing")
	}
}

func TestDispatch_ToolsList_SchemaShape(t *testing.T) {
	resp := dispatch(&fakeSink{}, &jsonRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	res := wireResult(t, resp)
	tools, ok := res["tools"].([]interface{})
	if !ok || len(tools) != 8 {
		t.Fatalf("want 8 tools, got %#v", res["tools"])
	}

	byName := map[string]map[string]interface{}{}
	for _, tl := range tools {
		m, _ := tl.(map[string]interface{})
		if m == nil {
			continue
		}
		name, _ := m["name"].(string)
		byName[name] = m
	}
	if _, ok := byName["report_telemetry"]; !ok {
		t.Fatalf("report_telemetry missing: %v", byName)
	}
	if _, ok := byName["get_file_health_score"]; !ok {
		t.Fatalf("get_file_health_score missing: %v", byName)
	}
	if _, ok := byName["check_guardrail"]; !ok {
		t.Fatalf("check_guardrail missing: %v", byName)
	}
	if _, ok := byName["report_file_read"]; !ok {
		t.Fatalf("report_file_read missing: %v", byName)
	}
	if _, ok := byName["get_file_read_stats"]; !ok {
		t.Fatalf("get_file_read_stats missing: %v", byName)
	}
	if _, ok := byName["get_file_diff_history"]; !ok {
		t.Fatalf("get_file_diff_history missing: %v", byName)
	}

	for name, tl := range byName {
		if d, _ := tl["description"].(string); strings.TrimSpace(d) == "" {
			t.Errorf("tool %s has empty description", name)
		}
		schema, _ := tl["inputSchema"].(map[string]interface{})
		if schema == nil {
			t.Fatalf("tool %s: inputSchema missing: %#v", name, tl)
		}
		if schema["type"] != "object" {
			t.Errorf("tool %s: schema.type = %v, want object", name, schema["type"])
		}
		props, _ := schema["properties"].(map[string]interface{})
		reqd, _ := schema["required"].([]interface{})
		if len(props) == 0 || len(reqd) == 0 {
			t.Errorf("tool %s: properties/required empty", name)
		}
		for _, r := range reqd {
			s, _ := r.(string)
			if _, ok := props[s]; !ok {
				t.Errorf("tool %s: required field %q has no property schema", name, s)
			}
		}
	}

	rt := byName["report_telemetry"]["inputSchema"].(map[string]interface{})
	reqSet := map[string]bool{}
	for _, r := range rt["required"].([]interface{}) {
		reqSet[r.(string)] = true
	}
	for _, want := range []string{"model", "provider", "task_id", "intent"} {
		if !reqSet[want] {
			t.Errorf("report_telemetry required missing %q: %v", want, reqSet)
		}
	}
	props := rt["properties"].(map[string]interface{})
	for _, want := range []string{"model", "provider", "task_id", "intent", "tokens_used", "cost"} {
		if _, ok := props[want]; !ok {
			t.Errorf("report_telemetry properties missing %q", want)
		}
	}
	if len(reqSet) != 4 {
		t.Errorf("report_telemetry required should have exactly 4 entries, got %d", len(reqSet))
	}

	fh := byName["get_file_health_score"]["inputSchema"].(map[string]interface{})
	if req := fh["required"].([]interface{}); len(req) != 1 || req[0] != "file_path" {
		t.Errorf("get_file_health_score required = %#v, want [file_path]", req)
	}
}

func TestDispatch_NotificationsReturnEmptyResult(t *testing.T) {
	for _, m := range []string{"notifications/initialized", "notifications/cancelled"} {
		resp := dispatch(&fakeSink{}, &jsonRPCRequest{JSONRPC: "2.0", ID: 3, Method: m})
		if resp.Error != nil {
			t.Errorf("%s: unexpected error %+v", m, resp.Error)
		}
		if resp.Result == nil {
			t.Errorf("%s: expected non-nil empty result", m)
		}
	}
}

func TestDispatch_UnknownMethod(t *testing.T) {
	resp := dispatch(&fakeSink{}, &jsonRPCRequest{JSONRPC: "2.0", ID: 4, Method: "resources/list"})
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("want -32601, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "resources/list") {
		t.Errorf("message %q should echo method", resp.Error.Message)
	}
}

// ---------------------------------------------------------------
// tools/call: report_telemetry
// ---------------------------------------------------------------

func TestCallTool_ReportTelemetry_Success(t *testing.T) {
	sink := &fakeSink{runID: "run-abc-123"}
	resp := callTool(sink, toolCallReq(1, "report_telemetry",
		`{"model":"claude-3-7-sonnet","provider":"anthropic","task_id":"TASK-402","intent":"Refactor auth","tokens_used":42000,"cost":0.144}`))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	res := resp.Result.(map[string]interface{})
	content, _ := res["content"].([]map[string]interface{})
	if len(content) != 1 || content[0]["type"] != "text" {
		t.Fatalf("content malformed: %#v", res["content"])
	}
	text, _ := content[0]["text"].(string)
	if !strings.Contains(text, "Telemetry recorded successfully.") || !strings.Contains(text, "run-abc-123") {
		t.Errorf("text = %q, want success message with run_id echo", text)
	}

	if sink.reportCalls != 1 || sink.healthCalls != 0 {
		t.Fatalf("sink calls: report=%d health=%d, want 1/0", sink.reportCalls, sink.healthCalls)
	}
	if sink.gotModel != "claude-3-7-sonnet" || sink.gotProvider != "anthropic" ||
		sink.gotTaskID != "TASK-402" || sink.gotIntent != "Refactor auth" {
		t.Errorf("sink args wrong: %+v", sink)
	}
	if sink.gotPrompt != 42000 {
		t.Errorf("tokens: got %d, want 42000 (float64→int64 coercion)", sink.gotPrompt)
	}
	if !approx(sink.gotCost, 0.144) {
		t.Errorf("cost: got %v, want 0.144", sink.gotCost)
	}
}

func TestCallTool_ReportTelemetry_MissingRequired(t *testing.T) {
	cases := []struct {
		name, args string
		missing    string
	}{
		{"no model", `{"provider":"p","task_id":"t","intent":"i"}`, "model"},
		{"no provider", `{"model":"m","task_id":"t","intent":"i"}`, "provider"},
		{"no task_id", `{"model":"m","provider":"p","intent":"i"}`, "task_id"},
		{"empty args", `{}`, "model"},
	}
	for _, c := range cases {
		sink := &fakeSink{}
		resp := callTool(sink, toolCallReq(2, "report_telemetry", c.args))
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Errorf("%s: want -32602, got %+v", c.name, resp.Error)
			continue
		}
		if !strings.Contains(resp.Error.Message, c.missing) {
			t.Errorf("%s: message %q should mention %q", c.name, resp.Error.Message, c.missing)
		}
		if sink.reportCalls != 0 {
			t.Errorf("%s: sink called despite validation failure", c.name)
		}
	}
}

func TestCallTool_ReportTelemetry_NilArguments(t *testing.T) {
	sink := &fakeSink{}
	resp := callTool(sink, toolCallReq(3, "report_telemetry", ""))
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("want -32602 for absent arguments, got %+v", resp.Error)
	}
}

func TestCallTool_ReportTelemetry_SinkFailure(t *testing.T) {
	sink := &fakeSink{reportErr: errors.New("sqlite: disk full")}
	resp := callTool(sink, toolCallReq(4, "report_telemetry",
		`{"model":"m","provider":"p","task_id":"t","intent":"i"}`))
	if resp.Error == nil || resp.Error.Code != -32010 {
		t.Fatalf("want -32010, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "disk full") {
		t.Errorf("message %q should propagate sink cause", resp.Error.Message)
	}
}

// ---------------------------------------------------------------
// tools/call: get_file_health_score
// ---------------------------------------------------------------

func TestCallTool_GetFileHealth_Success(t *testing.T) {
	sink := &fakeSink{health: ipc.FileHealthReply{
		FilePath: "src/auth.go", HealthScore: 36, IsFragile: true,
		RecentThrashingCount: 8, Warning: "8 edits in the last 24h",
	}}
	resp := callTool(sink, toolCallReq(5, "get_file_health_score", `{"file_path":"src/auth.go"}`))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	res := resp.Result.(map[string]interface{})
	content := res["content"].([]map[string]interface{})
	text := content[0]["text"].(string)
	for _, want := range []string{"health_score=36", "fragile=true", "recent_thrashing_count=8"} {
		if !strings.Contains(text, want) {
			t.Errorf("text %q missing %q", text, want)
		}
	}
	data, _ := res["data"].(ipc.FileHealthReply)
	if data.HealthScore != 36 || !data.IsFragile || data.RecentThrashingCount != 8 {
		t.Errorf("data payload wrong: %+v", data)
	}
	if sink.gotPath != "src/auth.go" || sink.healthCalls != 1 {
		t.Errorf("sink path=%q calls=%d", sink.gotPath, sink.healthCalls)
	}
}

func TestCallTool_GetFileHealth_MissingPath(t *testing.T) {
	for _, args := range []string{`{}`, ""} {
		sink := &fakeSink{}
		resp := callTool(sink, toolCallReq(6, "get_file_health_score", args))
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("args %q: want -32602, got %+v", args, resp.Error)
		}
		if !strings.Contains(resp.Error.Message, "file_path") {
			t.Errorf("message %q should mention file_path", resp.Error.Message)
		}
		if sink.healthCalls != 0 {
			t.Error("sink called despite validation failure")
		}
	}
}

func TestCallTool_GetFileHealth_SinkFailure(t *testing.T) {
	sink := &fakeSink{healthErr: errors.New("connection refused")}
	resp := callTool(sink, toolCallReq(7, "get_file_health_score", `{"file_path":"x.go"}`))
	if resp.Error == nil || resp.Error.Code != -32011 {
		t.Fatalf("want -32011, got %+v", resp.Error)
	}
}

func TestCallTool_UnknownTool(t *testing.T) {
	resp := callTool(&fakeSink{}, toolCallReq(8, "delete_everything", `{}`))
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("want -32601, got %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "unknown tool") {
		t.Errorf("message %q should say unknown tool", resp.Error.Message)
	}
}

// ---------------------------------------------------------------
// ServeStdio: notification suppression + malformed input
// ---------------------------------------------------------------

// runStdioSession swaps os.Stdin/os.Stdout for pipes, feeds lines, waits for
// ServeStdio to finish, and returns its non-empty output lines.
func runStdioSession(t *testing.T, sink EngineSink, lines []string) []string {
	t.Helper()

	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = rIn, wOut
	t.Cleanup(func() { os.Stdin, os.Stdout = oldIn, oldOut })

	var wg sync.WaitGroup
	wg.Add(1)
	errCh := make(chan error, 1)
	go func() {
		defer wg.Done()
		errCh <- ServeStdio(sink)
	}()

	for _, l := range lines {
		if _, err := io.WriteString(wIn, l+"\n"); err != nil {
			t.Fatalf("write stdin: %v", err)
		}
	}
	_ = wIn.Close() // EOF terminates ServeStdio.

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ServeStdio: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeStdio did not terminate after stdin EOF")
	}
	wg.Wait()
	_ = wOut.Close()

	raw, err := io.ReadAll(rOut)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestServeStdio_NotificationSuppression(t *testing.T) {
	out := runStdioSession(t, &fakeSink{}, []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	})
	if len(out) != 2 {
		t.Fatalf("want exactly 2 responses (2 notifications suppressed), got %d: %q", len(out), out)
	}

	var first, second jsonRPCResponse
	if err := json.Unmarshal([]byte(out[0]), &first); err != nil {
		t.Fatalf("response 1 not valid JSON: %v (%q)", err, out[0])
	}
	if id, ok := first.ID.(float64); !ok || id != 1 {
		t.Errorf("response 1 id = %#v, want 1 (initialize)", first.ID)
	}
	if res, _ := first.Result.(map[string]interface{}); res == nil || res["protocolVersion"] != "2024-11-05" {
		t.Errorf("response 1 result wrong: %#v", first.Result)
	}

	if err := json.Unmarshal([]byte(out[1]), &second); err != nil {
		t.Fatalf("response 2 not valid JSON: %v (%q)", err, out[1])
	}
	if id, ok := second.ID.(float64); !ok || id != 2 {
		t.Errorf("response 2 id = %#v, want 2 (tools/list)", second.ID)
	}
}

func TestServeStdio_MalformedLineGetsParseError(t *testing.T) {
	out := runStdioSession(t, &fakeSink{}, []string{
		`{this is not json`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
	})
	if len(out) != 2 {
		t.Fatalf("want 2 responses (parse error + method-not-found), got %d: %q", len(out), out)
	}
	var pe jsonRPCResponse
	if err := json.Unmarshal([]byte(out[0]), &pe); err != nil {
		t.Fatalf("parse-error response not valid JSON: %v", err)
	}
	if pe.Error == nil || pe.Error.Code != -32700 {
		t.Errorf("want -32700, got %+v", pe.Error)
	}
	var nf jsonRPCResponse
	if err := json.Unmarshal([]byte(out[1]), &nf); err != nil {
		t.Fatalf("second response not valid JSON: %v", err)
	}
	if nf.Error == nil || nf.Error.Code != -32601 {
		t.Errorf("want -32601 for unknown method over stdio, got %+v", nf.Error)
	}
}

// ---------------------------------------------------------------
// number coercion helpers
// ---------------------------------------------------------------

func TestNumberCoercion(t *testing.T) {
	i := []struct {
		in   interface{}
		want int64
	}{
		{float64(42000), 42000},
		{int(7), 7},
		{int64(9), 9},
		{json.Number("42"), 42},
		{"nope", 0},
		{nil, 0},
	}
	for _, c := range i {
		if got := toInt64(c.in); got != c.want {
			t.Errorf("toInt64(%#v) = %d, want %d", c.in, got, c.want)
		}
	}
	f := []struct {
		in   interface{}
		want float64
	}{
		{float64(0.144), 0.144},
		{int(2), 2},
		{int64(3), 3},
		{json.Number("1.5"), 1.5},
		{true, 0},
	}
	for _, c := range f {
		if !approx(toFloat(c.in), c.want) {
			t.Errorf("toFloat(%#v) = %v, want %v", c.in, toFloat(c.in), c.want)
		}
	}
}

func TestCallTool_ReadTools(t *testing.T) {
	sink := &fakeSink{
		readStats: db.FileReadStats{
			FilePath:       "test.go",
			TotalReads:     5,
			TotalLinesRead: 250,
			TotalCostUSD:   0.015,
			UniqueModels:   2,
		},
	}

	// 1. Test report_file_read
	req := toolCallReq(1, "report_file_read", `{"file_path":"test.go","model":"claude-3-7-sonnet","start_line":10,"end_line":50,"prompt_tokens":1500,"cost":0.005}`)
	resp := dispatch(sink, req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if sink.readCalls != 1 {
		t.Errorf("expected 1 read call, got %d", sink.readCalls)
	}
	if sink.lastReadRecord.FilePath != "test.go" || sink.lastReadRecord.StartLine != 10 || sink.lastReadRecord.EndLine != 50 {
		t.Errorf("unexpected read record: %+v", sink.lastReadRecord)
	}

	// 2. Test get_file_read_stats
	req = toolCallReq(2, "get_file_read_stats", `{"file_path":"test.go"}`)
	resp = dispatch(sink, req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if sink.gotPath != "test.go" {
		t.Errorf("expected path test.go, got %s", sink.gotPath)
	}
}

func TestCallTool_CheckGuardrail_LockedFile(t *testing.T) {
	sink := &fakeSink{
		isLocked:   true,
		lockReason: "critical maintenance",
		health: ipc.FileHealthReply{
			FilePath:    "src/locked.go",
			HealthScore: 90,
			IsFragile:   false,
		},
	}

	req := toolCallReq(3, "check_guardrail", `{"file_path":"src/locked.go"}`)
	resp := dispatch(sink, req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	res := resp.Result.(map[string]interface{})
	data, ok := res["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data map in result, got %#v", res)
	}

	if allowed, _ := data["allowed"].(bool); allowed {
		t.Errorf("expected allowed=false for locked file, got true")
	}
	if isLocked, _ := data["is_locked"].(bool); !isLocked {
		t.Errorf("expected is_locked=true for locked file, got false")
	}
	if reason, _ := data["lock_reason"].(string); reason != "critical maintenance" {
		t.Errorf("expected lock_reason 'critical maintenance', got %q", reason)
	}
}

