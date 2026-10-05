package profiler

import (
	"fmt"
	"testing"
)

func otlpTimestampDoc(start, end string) []byte {
	return fmt.Appendf(nil, `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"payments"}}]},"scopeSpans":[{"spans":[{"traceId":"trace-time","spanId":"span-time","name":"ChargeCard","startTimeUnixNano":%s,"endTimeUnixNano":%s}]}]}]}`, start, end)
}

func TestIngestOTLP_Fixed64TimestampSpellings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start string
		end   string
	}{
		{"quoted decimal strings", `"1700000000000000000"`, `"1700000000025000000"`},
		{"bare decimal numbers", `1700000000000000000`, `1700000000025000000`},
		{"near max uint64", `18446744073684551615`, `18446744073709551615`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured []TraceEvent
			collector := NewCollector(Config{OnTrace: func(ev TraceEvent) { captured = append(captured, ev) }})
			count, err := collector.IngestOTLP(otlpTimestampDoc(tc.start, tc.end))
			if err != nil || count != 1 || len(captured) != 1 {
				t.Fatalf("valid fixed64 spelling rejected or lost: count=%d captured=%d err=%v", count, len(captured), err)
			}
			if captured[0].DurationMs != 25 {
				t.Fatalf("duration = %v, want 25", captured[0].DurationMs)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		value string
	}{
		{"empty string", `""`},
		{"non-numeric string", `"not-a-timestamp"`},
		{"negative number", `-1`},
		{"overflow number", `18446744073709551616`},
		{"overflow string", `"18446744073709551616"`},
	} {
		t.Run(tc.name+" is rejected", func(t *testing.T) {
			var captured []TraceEvent
			collector := NewCollector(Config{OnTrace: func(ev TraceEvent) { captured = append(captured, ev) }})
			count, err := collector.IngestOTLP(otlpTimestampDoc(`"1700000000000000000"`, tc.value))
			if err == nil {
				t.Fatalf("invalid fixed64 value accepted: count=%d captured=%d", count, len(captured))
			}
			if count != 0 || len(captured) != 0 {
				t.Fatalf("invalid fixed64 value reached ingestion: count=%d captured=%d", count, len(captured))
			}
		})
	}
}
