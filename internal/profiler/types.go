package profiler

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	Kind              otlpEnumName    `json:"kind"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano"`
	Attributes        []OTLPAttribute `json:"attributes"`
	Status            *OTLPStatus     `json:"status,omitempty"`
}

// otlpEnumName is an OTLP enum field kept in the canonical textual form
// protojson emits ("SPAN_KIND_INTERNAL"), while still tolerating the numeric
// spellings the spec says parsers must accept. It exists for the same reason as
// OTLPStatus.UnmarshalJSON: a plain `Kind string` field made Go abort the ENTIRE
// document on a bare integer -- measured as
// `json: cannot unmarshal number into Go struct field ...spans.0.kind of type
// string`, which cost every span in the batch, not just the field.
type otlpEnumName string

// UnmarshalJSON accepts a name, a quoted number, or a bare number. An absent or
// null value leaves the field empty rather than failing the document.
func (e *otlpEnumName) UnmarshalJSON(b []byte) error {
	text, ok := otlpEnumText(b)
	if !ok {
		return nil
	}
	*e = otlpEnumName(text)
	return nil
}

// otlpEnumText returns the textual form of an OTLP enum value however a sender
// spelled it: the canonical NAME string, a quoted integer, or a bare integer
// (whose literal text is returned verbatim). ok is false only for an absent or
// null member. One routine serves both enum fields in this file so their
// tolerance cannot drift apart again.
func otlpEnumText(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", false
	}
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		return s, true
	}
	return trimmed, true
}

// otlpInt64 is AnyValue's int_value member. It exists for the same reason as
// otlpEnumName: the field was declared `int64` with a `,string` tag, and Go's
// `,string` option REQUIRES the quoted spelling, so a bare number raised
// UnmarshalTypeError that aborted json.Unmarshal for the WHOLE document --
// IngestOTLP returned (0, err) and handlers.go answered HTTP 400 on the standard
// /v1/traces endpoint, costing every span in the batch over one field spelling
// (measured: bare 42, bare 0, bare MaxInt64 and a bare int nested inside an
// arrayValue all lost the batch; the quoted forms worked).
//
// Canonical ProtoJSON (protobuf.dev/programming-guides/json, quoted from the
// fetched page 2026-09-08) says of int64/fixed64/uint64: "JSON value will be a
// decimal string. Either numbers or strings are accepted. Empty strings are
// invalid." So both spellings decode here, while "" and non-numeric text stay
// errors rather than silently becoming 0 -- the same line round 12 held for the
// enum names. Exponent notation (1e2) is also permitted by that sentence but was
// never accepted here or by the old `,string` path; senders do not emit it for
// int64, so it is left unimplemented rather than added speculatively.
//
// The JSON tag keeps no `,string`: tolerance now lives in this method, and the
// field is only ever decoded through OTLPVal.UnmarshalJSON's alias type.
type otlpInt64 int64

// UnmarshalJSON accepts both spec-permitted spellings of a 64-bit integer. An
// absent or null member leaves the value 0 rather than failing the document.
func (n *otlpInt64) UnmarshalJSON(b []byte) error {
	text, ok := otlpEnumText(b)
	if !ok {
		*n = 0
		return nil
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid otlp intValue %s: %w", string(b), err)
	}
	*n = otlpInt64(parsed)
	return nil
}

// otlpFloat64 is AnyValue's double_value member. Third and last numeric member
// of the oneof, and the same defect a third time: a plain float64 with no
// custom decoder rejects the quoted spellings, and encoding/json aborts the
// WHOLE document on a type error -- so POST /v1/traces answered 400 and the
// entire batch was lost over one attribute spelling. Measured pre-fix, ALL FOUR
// quoted forms failed with
// `cannot unmarshal string into Go struct field ...doubleValue of type float64`:
// the spec's "NaN", "Infinity", "-Infinity" AND Go's own "Inf".
//
// Canonical ProtoJSON (quoted from the fetched artifact, verified 2026-09-08) on
// float/double: "JSON value will be a number or one of the special string values
// "NaN", "Infinity", and "-Infinity". Either numbers or strings are accepted.
// Empty strings are invalid. Exponent notation is also accepted."
//
// strconv.ParseFloat covers that whole set (it accepts NaN/Inf/Infinity
// case-insensitively, signed, plus exponent notation), so one call replaces the
// enumeration -- and unlike the float bug in round 16's recollection, that
// coverage was MEASURED by the regression test rather than assumed.
type otlpFloat64 float64

// UnmarshalJSON accepts a bare number or any spec-permitted quoted form. An
// absent or null member leaves the value 0 rather than failing the document.
func (f *otlpFloat64) UnmarshalJSON(b []byte) error {
	text, ok := otlpEnumText(b)
	if !ok {
		*f = 0
		return nil
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("invalid otlp doubleValue %s: %w", string(b), err)
	}
	*f = otlpFloat64(parsed)
	return nil
}

// isNonFinite reports NaN or +/-Inf without importing math: NaN is the only
// value unequal to itself, and doubling changes nothing only for zero and the
// infinities, so excluding zero leaves the infinities alone.
func (f otlpFloat64) isNonFinite() bool {
	x := float64(f)
	return x != x || (x*2 == x && x != 0)
}

// metadataValue is what the double member contributes to TraceEvent.Metadata.
// Non-finite values are carried as their canonical ProtoJSON TEXT, not as the
// float64: encoding/json cannot marshal NaN/Inf ("unsupported value: NaN"), and
// collector.go builds metadata_json with `metaBytes, _ := json.Marshal(meta)` --
// it DISCARDS that error, so a raw NaN would silently store an empty
// metadata_json and every attribute of the span would vanish. Keeping the
// wire's own spelling preserves the information and stays marshalable, exactly
// as bytesValue keeps base64 verbatim rather than decoding it.
func (f otlpFloat64) metadataValue() any {
	x := float64(f)
	if x != x {
		return "NaN"
	}
	if x*2 == x && x != 0 {
		if x > 0 {
			return "Infinity"
		}
		return "-Infinity"
	}
	return x
}

// columnValue is the value for the numeric float64 columns (cpu.usage_pct).
// Non-finite collapses to 0 -- an unmarshalable NaN in a stored row would break
// every later read of that span, which is worse than losing one unusable gauge
// reading; the attribute itself is still preserved as text in metadata.
func (f otlpFloat64) columnValue() float64 {
	if f.isNonFinite() {
		return 0
	}
	return float64(f)
}

// OTLPAttribute represents an OpenTelemetry key-value attribute.
type OTLPAttribute struct {
	Key   string  `json:"key"`
	Value OTLPVal `json:"value"`
}

// OTLPVal represents the polymorphic value inside an OTLP attribute.
//
// AnyValue is a protobuf `oneof`, and its presence rules are load-bearing here:
// the proto comment says "it is valid for all values to be unspecified in which
// case this AnyValue is considered to be 'empty'", so an unspecified value is a
// DIFFERENT state from a member explicitly set to its default. protojson signals
// the chosen member by emitting its key -- {"boolValue":false} is not the same
// document as {} -- yet four plain Go fields cannot record that distinction,
// because both decode to the same all-zero struct. Inferring presence from
// "which field is non-zero" therefore stored false, 0 and 0.0 as "" (measured:
// 4 of 4 default-valued members). `present` carries the key the sender actually
// named; only UnmarshalJSON sets it, so it stays right as members are added.
type OTLPVal struct {
	StringValue string      `json:"stringValue,omitempty"`
	IntValue    otlpInt64   `json:"intValue,omitempty"`
	DoubleValue otlpFloat64 `json:"doubleValue,omitempty"`
	BoolValue   bool        `json:"boolValue,omitempty"`

	// Composite members (common.proto fields 5-7). Without these fields Go's
	// decoder silently IGNORED the keys, so an array or key-value attribute was
	// recorded as "" with no error raised -- measured at 7 of 7 composite shapes
	// losing their content. Elements are OTLPVal, so each nested value runs
	// through this same UnmarshalJSON and keeps its oneof presence recursively.
	ArrayValue  *OTLPArrayValue   `json:"arrayValue,omitempty"`
	KvlistValue *OTLPKeyValueList `json:"kvlistValue,omitempty"`
	// BytesValue holds protojson's base64 text VERBATIM rather than decoding to
	// []byte: json.Marshal([]byte) re-emits the same base64 string, so decoding
	// would add a failure path (invalid base64) for no change in the stored JSON.
	BytesValue string `json:"bytesValue,omitempty"`

	// present is the AnyValue member key named by the sender, in proto field
	// order. Empty means the value was unspecified, which the protocol allows.
	present string
}

// OTLPArrayValue is AnyValue.array_value: a list of AnyValue. The proto notes
// the array MAY BE EMPTY, so an empty list is a value, not an absence -- it must
// not collapse into the unspecified branch.
type OTLPArrayValue struct {
	Values []OTLPVal `json:"values"`
}

// OTLPKeyValueList is AnyValue.kvlist_value: a list of KeyValue. The proto
// requires the keys to be unique, which is why otlpAttributeValue renders it as
// a map.
type OTLPKeyValueList struct {
	Values []OTLPKeyValue `json:"values"`
}

// OTLPKeyValue is one common.proto KeyValue entry.
type OTLPKeyValue struct {
	Key   string  `json:"key"`
	Value OTLPVal `json:"value"`
}

// otlpAnyValueMembers lists AnyValue's oneof member keys in proto field order
// (common.proto fields 1-7). A payload naming several is malformed; the first
// listed wins deterministically instead of by Go map iteration order.
var otlpAnyValueMembers = []string{
	"stringValue", "boolValue", "intValue", "doubleValue",
	"arrayValue", "kvlistValue", "bytesValue",
}

// UnmarshalJSON records which oneof member the sender named before decoding the
// value, because the fields alone cannot express that. arrayValue/kvlistValue/
// bytesValue are recognised here AND unwrapped by otlpAttributeValue.
//
// Drift hazard: presence comes from otlpAnyValueMembers, values from the struct
// fields, and the two are maintained separately. A member added to one but not
// the other silently decodes to "" -- which is exactly how the composite members
// were lost before this existed.
func (v *OTLPVal) UnmarshalJSON(b []byte) error {
	present, err := otlpMemberKey(b)
	if err != nil {
		return err
	}
	// Alias type: same fields and tags, without this method, so the value decode
	// below cannot recurse.
	type otlpValWire OTLPVal
	var wire otlpValWire
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	*v = OTLPVal(wire)
	v.present = present
	return nil
}

// otlpMemberKey returns the first AnyValue member key present in an attribute
// value object, or "" when the value was left unspecified.
func otlpMemberKey(raw []byte) (string, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return "", err
	}
	for _, name := range otlpAnyValueMembers {
		if _, ok := keys[name]; ok {
			return name, nil
		}
	}
	return "", nil
}

// OTLPStatus represents the status of an OTLP span.
type OTLPStatus struct {
	Message string `json:"message,omitempty"`
	Code    int    `json:"code"`
}

// UnmarshalJSON accepts every spelling an OTLP/JSON sender may legitimately use
// for the StatusCode enum, because a plain int field accepts only one of them.
//
// Canonical ProtoJSON (protobuf.dev/programming-guides/json, verified 2026-09-08)
// renders an enum as its NAME string -- "Parsers accept both enum names and
// integer values" -- so a spec-compliant exporter sends
// {"code":"STATUS_CODE_ERROR"}. Go's encoding/json cannot put a JSON string into
// an int: it raises UnmarshalTypeError, which aborts json.Unmarshal for the WHOLE
// document. IngestOTLP therefore returned (0, err) and handlers.go answered
// HTTP 400 on the standard /v1/traces endpoint, discarding every span in the
// batch -- silently losing the error spans that matter most. Hand-rolled senders
// also appear in the wild with a bare integer (already worked) and a quoted
// integer (also rejected), so all three forms are accepted here.
//
// Code stays an int so the "== 2" ERROR comparison in collector.go is unchanged.
// An unrecognised enum NAME is still an error: this endpoint's job is to stop
// rejecting valid payloads, not to invent status values for invalid ones.
func (s *OTLPStatus) UnmarshalJSON(b []byte) error {
	var raw struct {
		Message string          `json:"message,omitempty"`
		Code    json.RawMessage `json:"code"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	s.Message = raw.Message
	s.Code = 0 // STATUS_CODE_UNSET
	text, ok := otlpEnumText(raw.Code)
	if !ok {
		return nil
	}
	if n, err := strconv.Atoi(text); err == nil {
		s.Code = n
		return nil
	}
	switch text {
	case "STATUS_CODE_UNSET":
		s.Code = 0
	case "STATUS_CODE_OK":
		s.Code = 1
	case "STATUS_CODE_ERROR":
		s.Code = 2
	default:
		return fmt.Errorf("unknown otlp status.code enum name %q", text)
	}
	return nil
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
