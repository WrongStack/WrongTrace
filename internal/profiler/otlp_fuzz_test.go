package profiler

// FuzzIngestOTLPDecode fuzzes the OTLP JSON decode path — the newest
// untrusted-input surface (custom unmarshalers in types.go plus the recursive
// otlpAttributeValue walk in collector.go) behind POST /v1/traces.
//
// Contract: IngestOTLP never panics on arbitrary bytes — malformed input is
// rejected with an error, valid shapes decode. The collector is built with a
// nil store (Config{}), so the whole decode + metadata path runs without any
// database.
//
// History: promoted 2026-09-09 from a temporary round-7 probe after a clean
// 60s campaign (12,638,573 execs, zero failures). The same-named target
// recovers that campaign's ~534-input corpus from GOCACHE, so every
// `-fuzz=FuzzIngestOTLPDecode` run keeps accruing coverage; plain `go test`
// runs only the seeds below (microseconds).

import (
	"testing"
)

func FuzzIngestOTLPDecode(f *testing.F) {
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"t","spanId":"s","name":"n","attributes":[{"key":"k","value":{"stringValue":"v"}}]}]}]}]}`))
	f.Add([]byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}}]},"scopeSpans":[{"spans":[{"traceId":"t","spanId":"s","name":"n","status":{"code":"STATUS_CODE_ERROR","message":"m"},"attributes":[{"key":"cpu.usage_pct","value":{"doubleValue":1.5}},{"key":"memory.bytes","value":{"intValue":42}},{"key":"arr","value":{"arrayValue":{"values":[{"stringValue":"a"},{"intValue":1},{"boolValue":true}}]}},{"key":"kv","value":{"kvlistValue":{"values":[{"key":"inner","value":{"bytesValue":"YmFzZTY0"}}]}}}]}}]}]}]}`))
	f.Add([]byte(`{"resourceSpans":[]}`))
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"","spanId":"","name":"","kind":2,"status":{"code":"2"}}]}]}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		c := NewCollector(Config{})
		_, _ = c.IngestOTLP(data)
	})
}
