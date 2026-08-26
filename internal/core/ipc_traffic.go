package core

import (
	"encoding/json"
	"sort"

	"github.com/wrongstack/wrongtrace/internal/ipc"
)

const (
	maxStoredIPCValueBytes = 64 * 1024
	maxIPCSummaryScalars   = 32
	maxIPCSummaryString    = 1024
)

// compactIPCTraffic detaches the retained inspector record from arbitrarily
// large JSON-RPC request/response objects. Normal calls keep their complete
// shape; oversized values become a small scalar summary with an explicit byte
// count instead of pinning up to the protocol's 16 MiB frame limit.
func compactIPCTraffic(rec ipc.IPCTrafficRecord) ipc.IPCTrafficRecord {
	rec.Params = compactIPCParams(rec.Params)
	rec.Result = compactIPCValue(rec.Result)
	if rec.Error != nil && len(rec.Error.Message) > maxIPCSummaryString {
		copyErr := *rec.Error
		copyErr.Message = copyErr.Message[:maxIPCSummaryString] + "…[truncated]"
		rec.Error = &copyErr
	}
	return rec
}

func compactIPCParams(params map[string]interface{}) map[string]interface{} {
	if params == nil {
		return nil
	}
	compact := compactIPCValue(params)
	if out, ok := compact.(map[string]interface{}); ok {
		return out
	}
	return map[string]interface{}{"_truncated": true}
}

func compactIPCValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]interface{}{"_truncated": true, "_reason": "value is not JSON serializable"}
	}
	if len(data) <= maxStoredIPCValueBytes {
		return value
	}

	summary := map[string]interface{}{
		"_truncated":      true,
		"_original_bytes": len(data),
	}
	if object, ok := value.(map[string]interface{}); ok {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		kept := 0
		for _, key := range keys {
			if kept >= maxIPCSummaryScalars {
				break
			}
			if scalar, ok := compactIPCScalar(object[key]); ok {
				summary[key] = scalar
				kept++
			}
		}
	}
	return summary
}

func compactIPCScalar(value interface{}) (interface{}, bool) {
	switch v := value.(type) {
	case nil, bool, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, json.Number:
		return v, true
	case string:
		if len(v) > maxIPCSummaryString {
			return v[:maxIPCSummaryString] + "…[truncated]", true
		}
		return v, true
	default:
		return nil, false
	}
}

func ipcTrafficSummary(rec ipc.IPCTrafficRecord) ipc.IPCTrafficRecord {
	result := rec
	result.Result = nil
	result.Params = make(map[string]interface{})
	for _, key := range []string{"file_path", "path", "run_id", "model_name", "model", "agent_name", "agent"} {
		if value, ok := rec.Params[key]; ok {
			if scalar, keep := compactIPCScalar(value); keep {
				result.Params[key] = scalar
			}
		}
	}
	return result
}

// GetIPCTrafficSummaries returns metadata-only rows for the WebUI list.
func (e *Engine) GetIPCTrafficSummaries() []ipc.IPCTrafficRecord {
	out := e.GetIPCTraffic()
	for i := range out {
		out[i] = ipcTrafficSummary(out[i])
	}
	return out
}

// GetIPCTrafficRecord returns the bounded full inspector record for one row.
func (e *Engine) GetIPCTrafficRecord(id string) (ipc.IPCTrafficRecord, bool) {
	e.ipcMu.RLock()
	defer e.ipcMu.RUnlock()
	for i := len(e.ipcTraffic) - 1; i >= 0; i-- {
		if e.ipcTraffic[i].ID == id {
			return e.ipcTraffic[i], true
		}
	}
	return ipc.IPCTrafficRecord{}, false
}
