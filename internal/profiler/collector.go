package profiler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// Config configures the runtime profiler collector.
type Config struct {
	Store   *db.Store
	OnTrace func(TraceEvent)
}

// Collector processes incoming profiler traces and runtime telemetry.
type Collector struct {
	cfg       Config
	mu        sync.RWMutex
	recent    []TraceEvent
	maxRecent int
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

	if c.cfg.Store != nil {
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
		_ = c.cfg.Store.InsertTrace(rec)
	}

	c.recordRecent(ev)

	if c.cfg.OnTrace != nil {
		c.cfg.OnTrace(ev)
	}

	return ev, nil
}

// IngestOTLP parses OpenTelemetry traces JSON payload and persists each span.
func (c *Collector) IngestOTLP(data []byte) (int, error) {
	var root OTLPResourceSpans
	if err := json.Unmarshal(data, &root); err != nil {
		return 0, fmt.Errorf("unmarshal otlp: %w", err)
	}

	count := 0
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
					meta[attr.Key] = attr.Value.StringValue
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
						cpuPct = attr.Value.DoubleValue
					case "memory.bytes":
						memBytes = attr.Value.IntValue
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

				if c.cfg.Store != nil {
					metaBytes, _ := json.Marshal(meta)
					rec := db.RuntimeTraceRecord{
						TraceID:       ev.TraceID,
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
					_ = c.cfg.Store.InsertTrace(rec)
				}

				c.recordRecent(ev)
				if c.cfg.OnTrace != nil {
					c.cfg.OnTrace(ev)
				}
				count++
			}
		}
	}

	return count, nil
}

// Hotspots returns functions with high latency or errors.
func (c *Collector) Hotspots(limit int) ([]db.ProfilerHotspotRow, error) {
	if c.cfg.Store == nil {
		return nil, nil
	}
	return c.cfg.Store.ProfilerHotspots(limit)
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
	if c.cfg.Store == nil {
		return db.ProfilerOverviewRow{}, nil
	}
	return c.cfg.Store.ProfilerOverview()
}

func (c *Collector) recordRecent(ev TraceEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.recent) >= c.maxRecent {
		c.recent = c.recent[1:]
	}
	c.recent = append(c.recent, ev)
}

func randomID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(b))
}
