package mcp

import (
	"testing"
)

// TestToolsList_AdvertisedRequiredMatchesHandlers pins the round-39 contract:
// every tool's advertised inputSchema.required must EQUAL the required-set the
// dispatch handlers actually enforce — in both directions.
//
// Schema-validating MCP clients validate arguments against inputSchema BEFORE
// calling. An over-declared entry (advertised required, handler lenient — the
// round-39 report_telemetry "intent" and get_file_diff_history "file_path"
// bugs) makes such clients silently refuse calls the server fully accepts.
// An under-declared entry (handler strict, schema lenient) is the inverse:
// calls the schema permits get rejected by the server at runtime.
//
// The enforced set is derived BEHAVIORALLY through the real dispatch: for each
// candidate argument, call the tool with exactly that argument omitted (dummy
// values for everything else) and record whether a -32602 error returns.
func TestToolsList_AdvertisedRequiredMatchesHandlers(t *testing.T) {
	resp := dispatch(&fakeSink{}, &jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/list",
	})
	if resp.Error != nil {
		t.Fatalf("tools/list failed: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("tools/list result has unexpected shape: %T", resp.Result)
	}
	tools, ok := result["tools"].([]map[string]interface{})
	if !ok {
		t.Fatalf("tools/list tools has unexpected shape: %T", result["tools"])
	}

	dummy := map[string]interface{}{
		"model": "test-model", "provider": "test-provider", "task_id": "test-task", "intent": "test-intent",
		"file_path": "test/file.go", "reason": "test", "owner": "test", "owner_run_id": "test",
		"tool_name": "test_tool", "start_line": 1, "end_line": 2, "prompt_tokens": 1,
		"cost": 0.0, "limit": 5, "ttl_minutes": 10,
	}

	call := func(name string, args map[string]interface{}) *rpcError {
		resp := dispatch(&fakeSink{}, &jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      2,
			Method:  "tools/call",
			Params:  map[string]interface{}{"name": name, "arguments": args},
		})
		return resp.Error
	}

	for _, tool := range tools {
		name, _ := tool["name"].(string)
		schema, _ := tool["inputSchema"].(map[string]interface{})
		props, _ := schema["properties"].(map[string]interface{})
		required, _ := schema["required"].([]string)
		reqSet := make(map[string]bool, len(required))
		for _, r := range required {
			reqSet[r] = true
		}

		for _, r := range required {
			args := make(map[string]interface{}, len(dummy))
			for k, v := range dummy {
				args[k] = v
			}
			delete(args, r)
			if err := call(name, args); err == nil {
				t.Errorf("%s: advertised required %q is NOT enforced — schema-validating clients silently refuse calls the server accepts", name, r)
			}
		}

		for prop := range props {
			if reqSet[prop] {
				continue
			}
			args := make(map[string]interface{}, len(dummy))
			for k, v := range dummy {
				args[k] = v
			}
			if _, declared := props[prop]; declared {
				args[prop] = dummy[prop]
			}
			if err := call(name, args); err != nil {
				t.Errorf("%s: handler enforces %q but it is not advertised as required — schema-permitting calls get rejected at runtime (%v)", name, prop, err)
			}
		}
	}
}
