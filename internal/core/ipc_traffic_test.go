package core

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

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

// Round-12 regression: oversized strings are truncated by BYTE budget, and
// the cut can land inside a multi-byte UTF-8 rune. Truncation must back off
// to the rune boundary so retained inspector records and SSE/WS payloads
// stay valid UTF-8 (json.Marshal would otherwise coerce the partial rune to
// U+FFFD mojibake).
func TestCompactIPCScalarTruncatesWithoutSplittingRunes(t *testing.T) {
	// 400 three-byte runes = 1200 bytes: the 1024-byte cut lands inside a rune.
	got, ok := compactIPCScalar(strings.Repeat("日", 400))
	if !ok {
		t.Fatal("compactIPCScalar refused a plain string")
	}
	s, isStr := got.(string)
	if !isStr {
		t.Fatalf("compactIPCScalar returned %T, want string", got)
	}
	if !utf8.ValidString(s) {
		t.Fatalf("truncated summary is not valid UTF-8 (len=%d)", len(s))
	}
	if !strings.HasSuffix(s, "…[truncated]") {
		t.Fatal("truncated summary missing suffix")
	}
	if len(s) > maxIPCSummaryString+len("…[truncated]") {
		t.Fatalf("truncated summary exceeds budget: %d bytes", len(s))
	}

	// A cut that already lands on a rune boundary must stay byte-identical.
	clean := strings.Repeat("a", 1024) + "日"
	got2, _ := compactIPCScalar(clean)
	if got2.(string) != clean[:1024]+"…[truncated]" {
		t.Fatal("boundary-clean truncation changed the prefix")
	}
}

func TestCompactIPCTrafficTruncatesErrorMessageWithoutSplittingRunes(t *testing.T) {
	rec := compactIPCTraffic(ipc.IPCTrafficRecord{
		ID:     "round-12",
		Method: "check_guardrail",
		Error:  &ipc.RPCError{Message: strings.Repeat("日", 400)},
	})
	if rec.Error == nil {
		t.Fatal("error record dropped")
	}
	if !utf8.ValidString(rec.Error.Message) {
		t.Fatalf("truncated error message is not valid UTF-8 (len=%d)", len(rec.Error.Message))
	}
	if !strings.HasSuffix(rec.Error.Message, "…[truncated]") {
		t.Fatal("truncated error message missing suffix")
	}
}
