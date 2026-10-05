package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCompactIPCValueSerializedSummaryBudget(t *testing.T) {
	many := make(map[string]interface{})
	escaped := make(map[string]interface{})
	for i := 0; i < 32; i++ {
		many[fmt.Sprintf("%02d", i)+strings.Repeat("k", 1024)] = strings.Repeat("v", 1024)
		escaped[fmt.Sprintf("%02d", i)] = strings.Repeat("\x00", 1024)
	}
	for name, value := range map[string]interface{}{
		"oversized key":             map[string]interface{}{strings.Repeat("k", 70000): 1},
		"combined keys and scalars": many,
		"escaping expansion":        escaped,
	} {
		t.Run(name, func(t *testing.T) {
			out := compactIPCValue(value)
			data, err := json.Marshal(out)
			if err != nil || len(data) > maxStoredIPCValueBytes {
				t.Fatalf("retained summary bytes=%d cap=%d err=%v", len(data), maxStoredIPCValueBytes, err)
			}
			if out.(map[string]interface{})["_truncated"] != true {
				t.Fatal("summary lost truncation marker")
			}
		})
	}
	for _, value := range []interface{}{nil, map[string]interface{}{}, strings.Repeat("x", maxStoredIPCValueBytes-2)} {
		if out := compactIPCValue(value); !reflect.DeepEqual(out, value) {
			t.Fatalf("value at or below bound changed: %T", value)
		}
	}
}
