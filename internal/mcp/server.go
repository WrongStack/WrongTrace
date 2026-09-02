// Package mcp implements the Model Context Protocol server for WrongTrace.
// When invoked as `wrongtrace mcp`, the binary reads JSON-RPC requests from
// stdin and writes responses to stdout, following the MCP conventions used
// by Claude Code, Cursor, Devin, Windsurf, and Cline.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
)

var serverVersion = "dev"

// SetVersion supplies the release version injected into the CLI via ldflags.
// Keeping the default preserves source/test builds while release binaries
// report one consistent version across CLI, IPC, and MCP discovery surfaces.
func SetVersion(v string) {
	if v = strings.TrimSpace(v); v != "" {
		serverVersion = v
	}
}

// EngineSink is the subset of the core Engine used by the MCP server.
type EngineSink interface {
	ReportRunMCP(model, provider, taskID, intent string, promptTokens, completionTokens int64, cost float64) (string, error)
	FileHealth(path string) (ipc.FileHealthReply, error)
	RecordReadEvent(rec db.FileReadRecord) error
	GetFileReadStats(filePath string) (db.FileReadStats, error)
	GetRecentEvents(limit int, repoFilter ...string) ([]db.EventRecord, error)
	GetRecentFileEvents(filePath string, limit int) ([]db.EventRecord, error)
}

// jsonRPCRequest is the MCP wire format. Notifications (no id) are valid.
type jsonRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
	ID      interface{}            `json:"id,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeStdio runs the MCP server on os.Stdin/os.Stdout until stdin closes.
func ServeStdio(sink EngineSink) error {
	if sink == nil {
		return fmt.Errorf("mcp: engine sink is required")
	}
	in := bufio.NewReaderSize(os.Stdin, 64*1024)
	out := bufio.NewWriterSize(os.Stdout, 64*1024)
	defer func() { _ = out.Flush() }()

	for {
		line, err := readMessage(in)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		req := jsonRPCRequest{}
		if err := json.Unmarshal(line, &req); err != nil {
			_ = writeMessage(out, jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}
		// Notifications carry no id: handle state changes, write no response.
		if req.ID == nil {
			continue
		}
		resp := dispatch(sink, &req)
		if err := writeMessage(out, resp); err != nil {
			return err
		}
	}
}

const maxMCPLineBytes = 16 * 1024 * 1024 // 16 MB max line length to protect against unbounded RAM allocation

func readMessage(r *bufio.Reader) ([]byte, error) {
	for {
		var line []byte
		for {
			chunk, isPrefix, err := r.ReadLine()
			if err != nil {
				return nil, err
			}
			if len(line)+len(chunk) > maxMCPLineBytes {
				return nil, fmt.Errorf("mcp: message line exceeded maximum limit of %d bytes", maxMCPLineBytes)
			}
			line = append(line, chunk...)
			if !isPrefix {
				break
			}
		}
		if len(line) > 0 {
			return line, nil
		}
		// A blank line is not end-of-stream: real EOF surfaces as an error
		// from ReadLine above. Treating a blank separator line as EOF exited
		// the whole MCP session mid-flight; skip it and keep serving.
	}
}

func writeMessage(w *bufio.Writer, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

// dispatch routes an MCP method to its handler.
func dispatch(sink EngineSink, req *jsonRPCRequest) jsonRPCResponse {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]string{
				"name":    "wrongtrace",
				"version": serverVersion,
			},
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
		}
	case "tools/list":
		resp.Result = mcpToolsList()
	case "tools/call":
		return callTool(sink, req)
	case "notifications/initialized", "notifications/cancelled":
		resp.Result = map[string]interface{}{}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

func callTool(sink EngineSink, req *jsonRPCRequest) jsonRPCResponse {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}
	name, _ := req.Params["name"].(string)
	args, _ := req.Params["arguments"].(map[string]interface{})
	if args == nil {
		args = map[string]interface{}{}
	}

	switch name {
	case "report_telemetry":
		model, _ := args["model"].(string)
		if model == "" {
			model, _ = args["model_name"].(string)
		}
		provider, _ := args["provider"].(string)
		taskID, _ := args["task_id"].(string)
		intent, _ := args["intent"].(string)
		var promptTokens, completionTokens int64
		if v, ok := args["prompt_tokens"]; ok && v != nil {
			promptTokens = toInt64(v)
		}
		if v, ok := args["completion_tokens"]; ok && v != nil {
			completionTokens = toInt64(v)
		}
		if promptTokens == 0 {
			if v, ok := args["tokens_used"]; ok && v != nil {
				promptTokens = toInt64(v)
			} else if v, ok := args["tokens"]; ok && v != nil {
				promptTokens = toInt64(v)
			}
		}
		var cost float64
		if v, ok := args["cost"]; ok && v != nil {
			cost = toFloat(v)
		} else if v, ok := args["cost_usd"]; ok && v != nil {
			cost = toFloat(v)
		}
		if model == "" || provider == "" || taskID == "" {
			resp.Error = &rpcError{Code: -32602, Message: "model, provider, and task_id are required"}
			return resp
		}
		runID, err := sink.ReportRunMCP(model, provider, taskID, intent, promptTokens, completionTokens, cost)
		if err != nil {
			resp.Error = &rpcError{Code: -32010, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "Telemetry recorded successfully. run_id=" + runID},
			},
		}
	case "get_file_health_score":
		path, _ := args["file_path"].(string)
		if path == "" {
			resp.Error = &rpcError{Code: -32602, Message: "file_path is required"}
			return resp
		}
		h, err := sink.FileHealth(path)
		if err != nil {
			resp.Error = &rpcError{Code: -32011, Message: err.Error()}
			return resp
		}
		text := fmt.Sprintf("health_score=%d fragile=%v recent_thrashing_count=%d is_locked=%v warning=%q",
			h.HealthScore, h.IsFragile, h.RecentThrashingCount, h.IsLocked, h.Warning)
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": text},
			},
			"data": h,
		}
	case "check_guardrail":
		path, _ := args["file_path"].(string)
		if path == "" {
			resp.Error = &rpcError{Code: -32602, Message: "file_path is required"}
			return resp
		}
		if locker, ok := sink.(interface {
			IsFileLocked(path string) (bool, string)
		}); ok {
			if locked, reason := locker.IsFileLocked(path); locked {
				rec := fmt.Sprintf("GUARDRAIL BLOCKED: File %s is locked (%s).", path, reason)
				text := fmt.Sprintf("allowed=false health_score=0 fragile=true recent_thrashing_count=0 is_locked=true lock_reason=%q recommendation=%q", reason, rec)
				resp.Result = map[string]interface{}{
					"content": []map[string]interface{}{
						{"type": "text", "text": text},
					},
					"data": map[string]interface{}{
						"allowed":                false,
						"health_score":           0,
						"is_fragile":             true,
						"is_locked":              true,
						"lock_reason":            reason,
						"recent_thrashing_count": 0,
						"recommendation":         rec,
					},
				}
				return resp
			}
		}
		h, err := sink.FileHealth(path)
		if err != nil {
			resp.Error = &rpcError{Code: -32011, Message: err.Error()}
			return resp
		}
		if h.IsLocked {
			rec := fmt.Sprintf("GUARDRAIL BLOCKED: File %s is locked (%s).", path, h.LockReason)
			text := fmt.Sprintf("allowed=false health_score=%d fragile=%v is_locked=true lock_reason=%q recommendation=%q",
				h.HealthScore, h.IsFragile, h.LockReason, rec)
			resp.Result = map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": text},
				},
				"data": map[string]interface{}{
					"allowed":                false,
					"health_score":           h.HealthScore,
					"is_fragile":             h.IsFragile,
					"is_locked":              true,
					"lock_reason":            h.LockReason,
					"lock_owner":             h.LockOwner,
					"lock_expires_at":        h.LockExpiresAt,
					"recent_thrashing_count": h.RecentThrashingCount,
					"recommendation":         rec,
				},
			}
			return resp
		}
		allowed := !h.IsFragile && h.HealthScore >= 40
		rec := "Safe to modify."
		if h.IsFragile || h.HealthScore < 40 {
			rec = fmt.Sprintf("GUARDRAIL WARNING: File %s has high churn (%d thrashing events, health score %d/100).", path, h.RecentThrashingCount, h.HealthScore)
		}
		text := fmt.Sprintf("allowed=%v health_score=%d fragile=%v recent_thrashing_count=%d recommendation=%q",
			allowed, h.HealthScore, h.IsFragile, h.RecentThrashingCount, rec)
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": text},
			},
			"data": map[string]interface{}{
				"allowed":                allowed,
				"health_score":           h.HealthScore,
				"is_fragile":             h.IsFragile,
				"is_locked":              false,
				"recent_thrashing_count": h.RecentThrashingCount,
				"recommendation":         rec,
			},
		}
	case "lock_file":
		path, _ := args["file_path"].(string)
		reason, _ := args["reason"].(string)
		owner, _ := args["owner"].(string)
		ownerRunID, _ := args["owner_run_id"].(string)
		// TTL sanity: time.Duration is int64 nanoseconds, so
		// time.Duration(mins) * time.Minute silently wraps negative once
		// mins exceeds ~153.7M (secs beyond ~9.2e9); the engine would then
		// degrade the lock to its 15-minute default or an arbitrary expiry
		// while this tool reports success. Client-supplied TTLs are capped
		// at 24h; anything beyond is an invalid parameter.
		const (
			maxLockTTLMinutes = 24 * 60
			maxLockTTLSeconds = 24 * 60 * 60
		)
		mins := toInt64(args["ttl_minutes"])
		secs := toInt64(args["ttl_seconds"])
		if mins > maxLockTTLMinutes || secs > maxLockTTLSeconds {
			resp.Error = &rpcError{Code: -32602, Message: "ttl_minutes and ttl_seconds must be <= 1440 and 86400 respectively (24h maximum lock)"}
			return resp
		}
		var ttl time.Duration = 15 * time.Minute
		if mins > 0 {
			ttl = time.Duration(mins) * time.Minute
		} else if secs > 0 {
			ttl = time.Duration(secs) * time.Second
		}
		if path == "" {
			resp.Error = &rpcError{Code: -32602, Message: "file_path is required"}
			return resp
		}
		// Capability assertions must match the sink's real signatures:
		// core.Engine's Lock methods RETURN core.LockInfo, and Go interface
		// satisfaction requires exact result types. The previous result-less
		// assertions matched no production sink, so lock_file silently took
		// no lock while reporting success.
		if locker, ok := sink.(interface {
			LockFileWithOptions(path, reason, owner, ownerRunID string, ttl time.Duration) core.LockInfo
		}); ok {
			locker.LockFileWithOptions(path, reason, owner, ownerRunID, ttl)
		} else if locker, ok := sink.(interface {
			LockFile(path, reason string) core.LockInfo
		}); ok {
			locker.LockFile(path, reason)
		}
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("File %s locked successfully. reason=%s owner=%s", path, reason, owner)},
			},
		}
	case "unlock_file":
		path, _ := args["file_path"].(string)
		if path == "" {
			resp.Error = &rpcError{Code: -32602, Message: "file_path is required"}
			return resp
		}
		if locker, ok := sink.(interface{ UnlockFile(path string) }); ok {
			locker.UnlockFile(path)
		}
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("File %s unlocked successfully.", path)},
			},
		}
	case "list_locks":
		var locks interface{} = []interface{}{}
		if locker, ok := sink.(interface{ ListLocks() []core.LockInfo }); ok {
			locks = locker.ListLocks()
		}
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "Active guardrail locks retrieved."},
			},
			"data": locks,
		}
	case "report_file_read":
		path, _ := args["file_path"].(string)
		model, _ := args["model"].(string)
		provider, _ := args["provider"].(string)
		toolName, _ := args["tool_name"].(string)
		intent, _ := args["intent"].(string)
		if toolName == "" {
			toolName = "mcp_read"
		}
		startLine := int(toInt64(args["start_line"]))
		endLine := int(toInt64(args["end_line"]))
		promptTokens := toInt64(args["prompt_tokens"])
		cost := toFloat(args["cost"])

		if path == "" || model == "" {
			resp.Error = &rpcError{Code: -32602, Message: "file_path and model are required"}
			return resp
		}

		err := sink.RecordReadEvent(db.FileReadRecord{
			FilePath:     path,
			AgentName:    "MCP",
			ModelName:    model,
			Provider:     provider,
			ToolName:     toolName,
			StartLine:    startLine,
			EndLine:      endLine,
			PromptTokens: promptTokens,
			CostUSD:      cost,
			Intent:       intent,
			ReadTime:     time.Now().UTC(),
		})
		if err != nil {
			resp.Error = &rpcError{Code: -32012, Message: err.Error()}
			return resp
		}
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("Read event recorded for %s (model: %s).", path, model)},
			},
		}
	case "get_file_read_stats":
		path, _ := args["file_path"].(string)
		if path == "" {
			resp.Error = &rpcError{Code: -32602, Message: "file_path is required"}
			return resp
		}
		stats, err := sink.GetFileReadStats(path)
		if err != nil {
			resp.Error = &rpcError{Code: -32013, Message: err.Error()}
			return resp
		}
		summary := fmt.Sprintf("file=%s total_reads=%d total_lines_read=%d total_cost=$%.4f unique_models=%d",
			stats.FilePath, stats.TotalReads, stats.TotalLinesRead, stats.TotalCostUSD, stats.UniqueModels)
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": summary},
			},
			"data": stats,
		}
	case "get_file_diff_history":
		path, _ := args["file_path"].(string)
		limit := 20
		if l := int(toInt64(args["limit"])); l > 0 {
			limit = l
		}
		var events []db.EventRecord
		var err error
		if path != "" {
			events, err = sink.GetRecentFileEvents(path, limit)
		} else {
			events, err = sink.GetRecentEvents(limit)
		}
		if err != nil {
			resp.Error = &rpcError{Code: -32014, Message: err.Error()}
			return resp
		}
		summary := fmt.Sprintf("Found %d diff events for file filter %q.", len(events), path)
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": summary},
			},
			"data": events,
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "unknown tool: " + name}
	}
	return resp
}

func toInt64(v interface{}) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case json.Number:
		i, _ := t.Int64()
		return i
	}
	return 0
}

func toFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	}
	return 0
}

// mcpToolsList builds the static capability advertisement exactly once.
// Clients call tools/list on every session start and reconnect; the nested
// map literal is constant, so rebuilding it per request was pure allocation
// churn. The returned structure is shared read-only: json.Marshal never
// mutates its argument and dispatch stores no per-request state in it.
var mcpToolsList = sync.OnceValue(func() interface{} {
	return map[string]interface{}{
		"tools": []map[string]interface{}{
			{
				"name":        "report_telemetry",
				"description": "Record an agent run's intent, model, token usage, and cost so WrongTrace can correlate it to subsequent AST churn.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"model":             map[string]string{"type": "string"},
						"model_name":        map[string]string{"type": "string"},
						"provider":          map[string]string{"type": "string"},
						"agent_name":        map[string]string{"type": "string"},
						"task_id":           map[string]string{"type": "string"},
						"intent":            map[string]string{"type": "string"},
						"tokens_used":       map[string]string{"type": "integer"},
						"prompt_tokens":     map[string]string{"type": "integer"},
						"completion_tokens": map[string]string{"type": "integer"},
						"cost":              map[string]string{"type": "number"},
						"cost_usd":          map[string]string{"type": "number"},
					},
					"required": []string{"model", "provider", "task_id", "intent"},
				},
			},
			{
				"name":        "get_file_health_score",
				"description": "Inspect a file's recent churn and lock status. Returns a 0-100 health score, fragile flag, and guardrail lock status.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]string{"type": "string"},
					},
					"required": []string{"file_path"},
				},
			},
			{
				"name":        "lock_file",
				"description": "Lock a fragile file against unwanted edits with optional ownership and duration (TTL).",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path":    map[string]string{"type": "string"},
						"reason":       map[string]string{"type": "string"},
						"owner":        map[string]string{"type": "string"},
						"owner_run_id": map[string]string{"type": "string"},
						"ttl_minutes":  map[string]string{"type": "integer"},
						"ttl_seconds":  map[string]string{"type": "integer"},
					},
					"required": []string{"file_path"},
				},
			},
			{
				"name":        "unlock_file",
				"description": "Unlock a previously locked file when edits or refactoring is completed.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]string{"type": "string"},
					},
					"required": []string{"file_path"},
				},
			},
			{
				"name":        "list_locks",
				"description": "List all active guardrail file locks and their TTL expiry times.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"filter": map[string]string{"type": "string"},
					},
				},
			},
			{
				"name":        "check_guardrail",
				"description": "Check if a file is safe to modify before performing automated AI refactoring.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]string{"type": "string"},
					},
					"required": []string{"file_path"},
				},
			},
			{
				"name":        "report_file_read",
				"description": "Record a file read/inspection tool event executed by an AI agent.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path":     map[string]string{"type": "string"},
						"model":         map[string]string{"type": "string"},
						"provider":      map[string]string{"type": "string"},
						"tool_name":     map[string]string{"type": "string"},
						"start_line":    map[string]string{"type": "integer"},
						"end_line":      map[string]string{"type": "integer"},
						"prompt_tokens": map[string]string{"type": "integer"},
						"cost":          map[string]string{"type": "number"},
						"intent":        map[string]string{"type": "string"},
					},
					"required": []string{"file_path", "model"},
				},
			},
			{
				"name":        "get_file_read_stats",
				"description": "Get file read counts, model breakdown, and recent inspection history.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]string{"type": "string"},
					},
					"required": []string{"file_path"},
				},
			},
			{
				"name":        "get_file_diff_history",
				"description": "Inspect recent line-by-line diffs, AST mutations, and churn for a file or entire codebase.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]string{"type": "string"},
						"limit":     map[string]string{"type": "integer"},
					},
					"required": []string{"file_path"},
				},
			},
		},
	}
})
