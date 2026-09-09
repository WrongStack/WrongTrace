package profiler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// Config configures the runtime profiler collector.
type Config struct {
	Store    *db.Store
	GetStore func() *db.Store
	OnTrace  func(TraceEvent)
}

// Collector processes incoming profiler traces and runtime telemetry.
type Collector struct {
	cfg       Config
	mu        sync.RWMutex
	recent    []TraceEvent
	maxRecent int
}

func (c *Collector) store() *db.Store {
	if c.cfg.GetStore != nil {
		if s := c.cfg.GetStore(); s != nil {
			return s
		}
	}
	return c.cfg.Store
}

// NewCollector constructs a new runtime profiler collector.
func NewCollector(cfg Config) *Collector {
	return &Collector{
		cfg:       cfg,
		recent:    make([]TraceEvent, 0, 100),
		maxRecent: 200,
	}
}

// IngestReport stores a single structured profiler or test runner record.
func (c *Collector) IngestReport(p ProfilerReportPayload) (TraceEvent, error) {
	if p.ServiceName == "" {
		p.ServiceName = "app"
	}
	if p.ProfilerType == "" {
		p.ProfilerType = "custom"
	}
	if p.StatusCode == 0 {
		if p.ErrorMsg != "" {
			p.StatusCode = 500
		} else {
			p.StatusCode = 200
		}
	}

	metaJSON, _ := json.Marshal(p.Metadata)

	ev := TraceEvent{
		TraceID:       randomID("tr"),
		RunID:         p.RunID,
		ServiceName:   p.ServiceName,
		NodeSignature: p.NodeSignature,
		FilePath:      p.FilePath,
		DurationMs:    p.DurationMs,
		CPUUsagePct:   p.CPUUsagePct,
		MemoryBytes:   p.MemoryBytes,
		StatusCode:    p.StatusCode,
		ErrorMsg:      p.ErrorMsg,
		ProfilerType:  p.ProfilerType,
		Metadata:      p.Metadata,
		Timestamp:     time.Now().UTC(),
	}

	var storeErr error
	if s := c.store(); s != nil {
		rec := db.RuntimeTraceRecord{
			TraceID:       ev.TraceID,
			RunID:         ev.RunID,
			ServiceName:   ev.ServiceName,
			NodeSignature: ev.NodeSignature,
			FilePath:      ev.FilePath,
			DurationMs:    ev.DurationMs,
			CPUUsagePct:   ev.CPUUsagePct,
			MemoryBytes:   ev.MemoryBytes,
			StatusCode:    ev.StatusCode,
			ErrorMsg:      ev.ErrorMsg,
			ProfilerType:  ev.ProfilerType,
			MetadataJSON:  string(metaJSON),
			Timestamp:     ev.Timestamp,
		}
		if err := s.InsertTrace(rec); err != nil {
			log.Printf("profiler: insert trace %s: %v", ev.TraceID, err)
			storeErr = fmt.Errorf("store profiler trace %s: %w", ev.TraceID, err)
		}
	}

	c.recordRecent(ev)

	if c.cfg.OnTrace != nil {
		c.cfg.OnTrace(ev)
	}

	// The error is returned AFTER the ring record and broadcast, so the live
	// dashboard still sees the event; what changes is that the caller can no
	// longer mistake a failed write for a stored one. handlers.go maps this to
	// HTTP 500, which is correct here: a single record either lands or does not,
	// and nothing was persisted, so a retry cannot duplicate anything.
	return ev, storeErr
}

// IngestOTLP parses OpenTelemetry traces JSON payload and persists each span.
func (c *Collector) IngestOTLP(data []byte) (int, error) {
	var root OTLPResourceSpans
	if err := json.Unmarshal(data, &root); err != nil {
		return 0, fmt.Errorf("unmarshal otlp: %w", err)
	}

	// count is spans that actually PERSISTED; failed is spans whose write was
	// rejected. They were previously conflated: `count++` ran even when
	// InsertTrace errored, so callers reported a batch as accepted that the
	// database never received.
	count, failed := 0, 0
	for _, rs := range root.ResourceSpans {
		serviceName := "unknown-service"
		for _, attr := range rs.Resource.Attributes {
			if attr.Key == "service.name" && attr.Value.StringValue != "" {
				serviceName = attr.Value.StringValue
			}
		}

		for _, scope := range rs.ScopeSpans {
			for _, span := range scope.Spans {
				var filePath, functionName, nodeSig, errorMsg string
				var statusCode = 200
				var cpuPct float64
				var memBytes int64

				meta := make(map[string]interface{})
				for _, attr := range span.Attributes {
					meta[attr.Key] = otlpAttributeValue(attr.Value)
					switch attr.Key {
					case "code.filepath", "code.file":
						filePath = attr.Value.StringValue
					case "code.function", "code.name":
						functionName = attr.Value.StringValue
					case "http.status_code":
						if attr.Value.IntValue > 0 {
							statusCode = int(attr.Value.IntValue)
						} else if attr.Value.StringValue != "" {
							if code, err := strconv.Atoi(attr.Value.StringValue); err == nil {
								statusCode = code
							}
						}
					case "cpu.usage_pct":
						cpuPct = attr.Value.DoubleValue.columnValue()
					case "memory.bytes":
						memBytes = int64(attr.Value.IntValue)
					}
				}

				if span.Status != nil {
					if span.Status.Code == 2 { // 2 = ERROR in OTLP
						if statusCode < 400 {
							statusCode = 500
						}
						errorMsg = span.Status.Message
					}
				}

				if nodeSig == "" && functionName != "" {
					if filePath != "" {
						nodeSig = fmt.Sprintf("func:%s::%s", filePath, functionName)
					} else {
						nodeSig = fmt.Sprintf("span:%s", functionName)
					}
				} else if nodeSig == "" && span.Name != "" {
					nodeSig = fmt.Sprintf("span:%s", span.Name)
				}

				var durationMs float64
				startNano, _ := strconv.ParseUint(span.StartTimeUnixNano, 10, 64)
				endNano, _ := strconv.ParseUint(span.EndTimeUnixNano, 10, 64)
				if endNano > startNano && startNano > 0 {
					durationMs = float64(endNano-startNano) / 1e6
				}

				traceID := span.TraceID
				if traceID == "" {
					traceID = randomID("otlp")
				}
				// Row identity: every span of a trace shares one traceId, but
				// runtime_traces.trace_id is the PRIMARY KEY and InsertTrace
				// is a plain INSERT, so keying rows by the group ID made spans
				// 2..N of any multi-span trace fail the UNIQUE constraint and
				// get silently dropped (the error is only logged) while count
				// still reported them. Key the persisted row per span; the
				// broadcast event keeps the raw traceId.
				rowID := traceID
				if span.SpanID != "" {
					if span.TraceID != "" {
						rowID = span.TraceID + "-" + span.SpanID
					} else {
						rowID = "span-" + span.SpanID
					}
				} else if span.TraceID != "" {
					// Only the group identity exists: without a per-span half
					// every span of the trace collapses onto rowID=traceID, and
					// InsertTrace's ON CONFLICT(trace_id) DO NOTHING silently
					// keeps only the first while the returned count still
					// reports them all. Mint the missing half — the same remedy
					// the traceID fallback above applies to an identity-less
					// trace — so every counted span lands in its own row.
					rowID = traceID + "-" + randomID("span")
				}
				// span.TraceID == "" && span.SpanID == "": traceID is already a
				// minted per-span random ID, unique as-is.

				ev := TraceEvent{
					TraceID:       traceID,
					ServiceName:   serviceName,
					NodeSignature: nodeSig,
					FilePath:      filePath,
					DurationMs:    durationMs,
					CPUUsagePct:   cpuPct,
					MemoryBytes:   memBytes,
					StatusCode:    statusCode,
					ErrorMsg:      errorMsg,
					ProfilerType:  "otlp",
					Metadata:      meta,
					Timestamp:     time.Now().UTC(),
				}

				stored := true
				if s := c.store(); s != nil {
					metaBytes, _ := json.Marshal(meta)
					rec := db.RuntimeTraceRecord{
						TraceID:       rowID,
						ServiceName:   ev.ServiceName,
						NodeSignature: ev.NodeSignature,
						FilePath:      ev.FilePath,
						DurationMs:    ev.DurationMs,
						CPUUsagePct:   ev.CPUUsagePct,
						MemoryBytes:   ev.MemoryBytes,
						StatusCode:    ev.StatusCode,
						ErrorMsg:      ev.ErrorMsg,
						ProfilerType:  ev.ProfilerType,
						MetadataJSON:  string(metaBytes),
						Timestamp:     ev.Timestamp,
					}
					if err := s.InsertTrace(rec); err != nil {
						log.Printf("profiler: insert trace %s: %v", ev.TraceID, err)
						stored = false
					}
				}

				c.recordRecent(ev)
				if c.cfg.OnTrace != nil {
					c.cfg.OnTrace(ev)
				}
				// Only a write that reached the database counts as accepted.
				if stored {
					count++
				} else {
					failed++
				}
			}
		}
	}

	// Only a TOTAL failure is signalled as an error. A partial failure must not
	// return one: handlers.go would answer HTTP 500, an OTel SDK then retries the
	// WHOLE batch, and InsertTrace is a plain INSERT -- the spans that already
	// persisted collide with the runtime_traces.trace_id primary key (the exact
	// failure round 20 documented) and fail again, so the client can never drain
	// the batch. Partial loss is reported by the honest count plus the per-span
	// log line above; total failure is safe because nothing landed.
	if count == 0 && failed > 0 {
		return 0, fmt.Errorf("otlp: all %d spans failed to store", failed)
	}
	return count, nil
}

// otlpAttributeValue unwraps an OTLP oneof attribute value into its Go type:
// string, int64, float64, or bool. IngestOTLP stores every attribute into
// TraceEvent.Metadata (broadcast via OnTrace and persisted as metadata_json);
// taking StringValue alone turned numeric and boolean attributes
// (http.status_code, cpu.usage_pct, feature.enabled) into empty strings
// there — even though the typed sibling fields of the same record were
// populated from the very same attributes.
//
// The member is selected by which key the SENDER named (OTLPVal.present), never
// by testing fields for non-zero-ness: a value explicitly set to its default is
// distinguishable on the wire and must not collapse to "". See OTLPVal.
func otlpAttributeValue(v OTLPVal) any {
	switch v.present {
	case "stringValue":
		return v.StringValue
	case "boolValue":
		return v.BoolValue
	case "intValue":
		// Widened back to int64 so the metadata value type is unchanged by the
		// named field type: both this surface and metadata_json stay int64.
		return int64(v.IntValue)
	case "doubleValue":
		return v.DoubleValue.metadataValue()
	case "arrayValue":
		// A named-but-null or valueless array is still an EMPTY array: the proto
		// says array_value "may be empty (contain 0 elements)", so empty must not
		// collapse into the unspecified branch's "".
		items := []any{}
		if v.ArrayValue != nil {
			items = make([]any, 0, len(v.ArrayValue.Values))
			for _, e := range v.ArrayValue.Values {
				items = append(items, otlpAttributeValue(e)) // recursive: keeps nested presence
			}
		}
		return items
	case "kvlistValue":
		pairs := map[string]any{}
		if v.KvlistValue != nil {
			pairs = make(map[string]any, len(v.KvlistValue.Values))
			for _, kv := range v.KvlistValue.Values {
				pairs[kv.Key] = otlpAttributeValue(kv.Value)
			}
		}
		return pairs
	case "bytesValue":
		return v.BytesValue
	default:
		// Only the genuinely unspecified value reaches here now -- legal per
		// AnyValue ("considered to be empty"). string_value_strindex (proto field
		// 8) also lands here on purpose: the proto directs non-Profiling
		// receivers to treat it as if absent, so ignoring it IS the compliance.
		return ""
	}
}

// Hotspots returns functions with high latency or errors.
func (c *Collector) Hotspots(limit int) ([]db.ProfilerHotspotRow, error) {
	if s := c.store(); s != nil {
		return s.ProfilerHotspots(limit)
	}
	return nil, nil
}

// Recent returns the most recent captured runtime traces.
func (c *Collector) Recent(limit int) ([]TraceEvent, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if limit <= 0 || limit > len(c.recent) {
		limit = len(c.recent)
	}
	out := make([]TraceEvent, limit)
	for i := 0; i < limit; i++ {
		out[i] = c.recent[len(c.recent)-1-i]
	}
	return out, nil
}

// Overview returns aggregate stats across all runtime traces.
func (c *Collector) Overview() (db.ProfilerOverviewRow, error) {
	if s := c.store(); s != nil {
		return s.ProfilerOverview()
	}
	return db.ProfilerOverviewRow{}, nil
}

func (c *Collector) recordRecent(ev TraceEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.recent) >= c.maxRecent {
		copy(c.recent, c.recent[1:])
		c.recent[len(c.recent)-1] = ev
		return
	}
	c.recent = append(c.recent, ev)
}

// randomID mints a prefixed 128-bit identifier. trace_id is the PRIMARY KEY
// of runtime_traces: at the previous 32 bits the birthday bound reached ~1%
// collision after only ~9k traces, silently discarding rows on a table that
// only ever accumulates.
func randomID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(b))
}
