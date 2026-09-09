package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestOTLPIngestRejectsOversizedBodyNamingTheLimit pins that an OTLP export
// larger than the handler's cap is REJECTED with a message that names the
// server's limit, rather than being truncated and blamed on the sender.
//
// The handler read io.ReadAll(io.LimitReader(r.Body, 16MB)) and never compared
// the length, so an over-cap body lost its tail. encoding/json then failed on
// the truncated prefix and IngestOTLPTraces answered
//
//	400 {"error":"parse otlp traces: unmarshal otlp: unexpected end of JSON input"}
//
// for a perfectly well-formed payload. The exporter sees "your JSON is broken",
// retries identically, and no log points at the real cause -- the exact failure
// proxy.go warns about in its own comment ("a silently truncated body would
// be ... corrupt JSON ... with no trace of why"). Truncating-without-detection
// was the one outlier among this repo's untrusted-body reads: proxy.go's
// maxBodyBytes+1, engine.go's readBoundedResponse(limit+1) and decodeJSON's
// http.MaxBytesReader all detect.
//
// Status stays 400 by design: a sender whose body fits is untouched, so this is
// a diagnostic fix, not a contract change. Both mounts are covered because they
// share the one line -- POST /v1/traces on the root router (server.go:549) and,
// inside r.Route("/api") opened at :454, POST /api/profiler/otlp/v1/traces (:532).
//
// The exactly-at-cap row is the off-by-one guard for the new "+1" read: a body
// of exactly the limit is legitimate data, so it must NOT be reported as too
// large. Controls (under-cap, genuinely malformed) pass before AND after the
// fix, so a failure there means the fixture or a real regression, not this
// round's change.

func otlpDocPadded(n int) string {
	return `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name",` +
		`"value":{"stringValue":"big-svc"}}]},"scopeSpans":[{"spans":[{"traceId":"t-1",` +
		`"spanId":"s-1","name":"Big","startTimeUnixNano":"1700000000000000000",` +
		`"endTimeUnixNano":"1700000000050000000","attributes":[{"key":"blob","value":` +
		`{"stringValue":"` + strings.Repeat("a", n) + `"}}]}]}]}]}`
}

func postOTLPBody(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// otlpEnvelopeLen is the length of otlpDocPadded(0), so a case can size a body
// to an exact byte total without hand-counting the JSON.
const otlpEnvelopeLen = len(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name",` +
	`"value":{"stringValue":"big-svc"}}]},"scopeSpans":[{"spans":[{"traceId":"t-1",` +
	`"spanId":"s-1","name":"Big","startTimeUnixNano":"1700000000000000000",` +
	`"endTimeUnixNano":"1700000000050000000","attributes":[{"key":"blob","value":` +
	`{"stringValue":""}}]}]}]}]}`)

func TestOTLPIngestRejectsOversizedBodyNamingTheLimit(t *testing.T) {
	_, _, ts := newTestServer(t)

	const cap = 16 * 1024 * 1024

	// Both mounts share the fixed line; each must report the limit.
	for _, route := range []string{"/v1/traces", "/api/profiler/otlp/v1/traces"} {
		t.Run("oversized_rejected_by_name"+route, func(t *testing.T) {
			doc := otlpDocPadded(cap - otlpEnvelopeLen + 1024) // one KiB over the cap
			if len(doc) <= cap {
				t.Fatalf("SETUP: payload %d bytes is not over the %d cap", len(doc), cap)
			}
			status, body := postOTLPBody(t, ts.URL+route, doc)
			// A wrong path would fall through to the SPA catch-all and answer
			// 200 + "<!doctype html>", making every text check below vacuously
			// true -- a bug that actually happened while writing this test.
			if !strings.Contains(body, `"error"`) {
				t.Fatalf("SETUP: not a handler error envelope (wrong route?): %.200s", body)
			}
			if status != http.StatusBadRequest {
				t.Errorf("FAIL: status = %d, want 400", status)
			}
			low := strings.ToLower(body)
			if !strings.Contains(low, "too large") || !strings.Contains(low, "exceeds") {
				t.Errorf("FAIL: response does not name the server's body limit: %.300s", body)
			}
			if strings.Contains(body, "unexpected end of JSON input") {
				t.Errorf("FAIL: server-side truncation still surfaced as malformed sender JSON: %.300s", body)
			}
			if strings.Contains(low, "unmarshal") {
				t.Errorf("FAIL: parse error reported for a body the server refused to read whole: %.300s", body)
			}
		})
	}

	// Off-by-one guard for the limit+1 read: exactly at the cap is legal data.
	t.Run("exactly_at_cap_is_accepted", func(t *testing.T) {
		doc := otlpDocPadded(cap - otlpEnvelopeLen)
		if len(doc) != cap {
			t.Fatalf("SETUP: wanted a body of exactly %d bytes, built %d", cap, len(doc))
		}
		status, body := postOTLPBody(t, ts.URL+"/v1/traces", doc)
		if status != http.StatusOK {
			t.Errorf("FAIL: body of exactly the cap rejected -- off-by-one in the overflow check: status=%d body=%.200s", status, body)
		}
	})

	// Controls: unchanged by this fix.
	t.Run("controls", func(t *testing.T) {
		if status, body := postOTLPBody(t, ts.URL+"/v1/traces", otlpDocPadded(16)); status != http.StatusOK {
			t.Errorf("CONTROL BROKEN (not this round's bug): valid under-cap payload rejected: status=%d body=%.200s", status, body)
		}
		if status, body := postOTLPBody(t, ts.URL+"/v1/traces", `{"resourceSpans":[`); status != http.StatusBadRequest ||
			!strings.Contains(body, "parse otlp") {
			t.Errorf("CONTROL BROKEN (not this round's bug): malformed payload not reported as a parse failure: status=%d body=%.200s", status, body)
		}
	})
}
