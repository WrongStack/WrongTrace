package profiler

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// Current OTel SDKs send only the stable attribute names. A 404 sent as
// http.response.status_code must be stored and counted like the legacy
// http.status_code, and code.file.path / code.function.name must name the
// span's file and function like code.filepath / code.function.
func TestIngestOTLP_CurrentSemconvNames(t *testing.T) {
	ingest := func(attrs string) TraceEvent {
		t.Helper()
		store, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.Migrate(); err != nil {
			t.Fatal(err)
		}
		var got []TraceEvent
		c := NewCollector(Config{Store: store, OnTrace: func(ev TraceEvent) { got = append(got, ev) }})
		payload := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},
		  "scopeSpans":[{"spans":[{"traceId":"t1","spanId":"s1","name":"GET /orders/{id}","kind":2,
		    "startTimeUnixNano":"1000000000","endTimeUnixNano":"1050000000","attributes":[` + attrs + `]}]}]}]}`
		if n, err := c.IngestOTLP([]byte(payload)); err != nil || n != 1 || len(got) != 1 {
			t.Fatalf("ingest: n=%d err=%v got=%d", n, err, len(got))
		}
		if ov, err := c.Overview(); err != nil || (got[0].StatusCode >= 400) != (ov.TotalErrors == 1) {
			t.Fatalf("overview errors = %d (err %v) for status %d", ov.TotalErrors, err, got[0].StatusCode)
		}
		return got[0]
	}
	for _, key := range []string{"http.status_code", "http.response.status_code"} {
		if ev := ingest(`{"key":"` + key + `","value":{"intValue":"404"}}`); ev.StatusCode != 404 {
			t.Errorf("%s=404 stored as %d", key, ev.StatusCode)
		}
	}
	for _, names := range [][2]string{{"code.filepath", "code.function"}, {"code.file.path", "code.function.name"}} {
		ev := ingest(`{"key":"` + names[0] + `","value":{"stringValue":"svc/orders.go"}},{"key":"` + names[1] + `","value":{"stringValue":"GetOrder"}}`)
		if ev.FilePath != "svc/orders.go" || !strings.Contains(ev.NodeSignature, "GetOrder") {
			t.Errorf("%v: file=%q node=%q", names, ev.FilePath, ev.NodeSignature)
		}
	}
}
