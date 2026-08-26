package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/ipc"
)

func TestIPCTrafficRetentionIsBoundedAndDetailedOnDemand(t *testing.T) {
	engine := &Engine{}
	huge := strings.Repeat("payload", 40_000)
	engine.RecordIPCTraffic(ipc.IPCTrafficRecord{
		ID:     "ipc-large",
		Method: "get_atlas",
		Params: map[string]interface{}{
			"file_path": "internal/core/atlas.go",
			"content":   huge,
		},
		Result: map[string]interface{}{"graph": huge},
	})

	detail, ok := engine.GetIPCTrafficRecord("ipc-large")
	if !ok {
		t.Fatal("stored IPC detail not found")
	}
	for name, value := range map[string]interface{}{"params": detail.Params, "result": detail.Result} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if len(data) > maxStoredIPCValueBytes {
			t.Fatalf("stored %s = %d bytes, cap = %d", name, len(data), maxStoredIPCValueBytes)
		}
	}
	if detail.Params["_truncated"] != true || detail.Result.(map[string]interface{})["_truncated"] != true {
		t.Fatalf("oversized IPC values were not marked truncated: params=%v result=%v", detail.Params, detail.Result)
	}

	summaries := engine.GetIPCTrafficSummaries()
	if len(summaries) != 1 || summaries[0].Result != nil {
		t.Fatalf("unexpected summaries: %+v", summaries)
	}
	if summaries[0].Params["file_path"] != "internal/core/atlas.go" {
		t.Fatalf("summary lost file_path: %+v", summaries[0].Params)
	}
	if _, retained := summaries[0].Params["content"]; retained {
		t.Fatalf("summary retained large content: %+v", summaries[0].Params)
	}
}
