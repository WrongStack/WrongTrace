package profiler

import (
	"time"
)

// TraceEvent represents a captured runtime execution or profiler span event.
type TraceEvent struct {
	TraceID       string                 `json:"trace_id"`
	RunID         string                 `json:"run_id,omitempty"`
	ServiceName   string                 `json:"service_name"`
	NodeSignature string                 `json:"node_signature,omitempty"`
	FilePath      string                 `json:"file_path,omitempty"`
	DurationMs    float64                `json:"duration_ms"`
	CPUUsagePct   float64                `json:"cpu_usage_pct"`
	MemoryBytes   int64                  `json:"memory_bytes"`
	StatusCode    int                    `json:"status_code"`
	ErrorMsg      string                 `json:"error_msg,omitempty"`
	ProfilerType  string                 `json:"profiler_type"` // "otlp", "pprof", "test_runner", "custom"
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	Timestamp     time.Time              `json:"timestamp"`
}

// OTLPSpan represents a single OpenTelemetry span structure.
type OTLPSpan struct {
	TraceID           string          `json:"traceId"`
	SpanID            string          `json:"spanId"`
	Name              string          `json:"name"`
	Kind              string          `json:"kind"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano"`
	Attributes        []OTLPAttribute `json:"attributes"`
	Status            *OTLPStatus     `json:"status,omitempty"`
}

// OTLPAttribute represents an OpenTelemetry key-value attribute.
type OTLPAttribute struct {
	Key   string  `json:"key"`
	Value OTLPVal `json:"value"`
}

// OTLPVal represents the polymorphic value inside an OTLP attribute.
type OTLPVal struct {
	StringValue string  `json:"stringValue,omitempty"`
	IntValue    int64   `json:"intValue,string,omitempty"`
	DoubleValue float64 `json:"doubleValue,omitempty"`
	BoolValue   bool    `json:"boolValue,omitempty"`
}

// OTLPStatus represents the status of an OTLP span.
type OTLPStatus struct {
	Message string `json:"message,omitempty"`
	Code    int    `json:"code"`
}

// OTLPResourceSpans represents the root envelope of OTLP traces payload.
type OTLPResourceSpans struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []OTLPAttribute `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []OTLPSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

// ProfilerReportPayload represents a standardized profiler/test ingestion payload.
type ProfilerReportPayload struct {
	ServiceName   string                 `json:"service_name"`
	RunID         string                 `json:"run_id,omitempty"`
	NodeSignature string                 `json:"node_signature,omitempty"`
	FilePath      string                 `json:"file_path,omitempty"`
	DurationMs    float64                `json:"duration_ms"`
	CPUUsagePct   float64                `json:"cpu_usage_pct,omitempty"`
	MemoryBytes   int64                  `json:"memory_bytes,omitempty"`
	StatusCode    int                    `json:"status_code,omitempty"`
	ErrorMsg      string                 `json:"error_msg,omitempty"`
	ProfilerType  string                 `json:"profiler_type,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}
