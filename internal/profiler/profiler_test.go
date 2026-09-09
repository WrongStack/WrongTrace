package profiler

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
)

func TestProfilerCollector(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test-profiler.db")
	store, err := db.Open(tmp)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var capturedEvents []TraceEvent
	collector := NewCollector(Config{
		Store: store,
		OnTrace: func(ev TraceEvent) {
			capturedEvents = append(capturedEvents, ev)
		},
	})

	// 1. Ingest custom report
	ev, err := collector.IngestReport(ProfilerReportPayload{
		ServiceName:   "backend-api",
		NodeSignature: "func:auth.go::ValidateToken",
		FilePath:      "src/auth.go",
		DurationMs:    45.2,
		CPUUsagePct:   12.5,
		MemoryBytes:   1024 * 1024,
		StatusCode:    200,
		ProfilerType:  "pprof",
	})
	if err != nil {
		t.Fatalf("ingest report: %v", err)
	}
	if ev.TraceID == "" {
		t.Errorf("expected trace ID, got empty")
	}

	// 2. Ingest OTLP JSON payload
	otlpJSON := `{
		"resourceSpans": [
			{
				"resource": {
					"attributes": [
						{"key": "service.name", "value": {"stringValue": "payment-svc"}}
					]
				},
				"scopeSpans": [
					{
						"spans": [
							{
								"traceId": "trace-12345678",
								"spanId": "span-123",
								"name": "ProcessPayment",
								"startTimeUnixNano": "1700000000000000000",
								"endTimeUnixNano":   "1700000000050000000",
								"attributes": [
									{"key": "code.filepath", "value": {"stringValue": "src/pay.go"}},
									{"key": "code.function", "value": {"stringValue": "ProcessPayment"}},
									{"key": "http.status_code", "value": {"intValue": "200"}}
								]
							}
						]
					}
				]
			}
		]
	}`

	count, err := collector.IngestOTLP([]byte(otlpJSON))
	if err != nil {
		t.Fatalf("ingest otlp: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 span ingested, got %d", count)
	}

	// 3. Hotspots & Overview
	hotspots, err := collector.Hotspots(10)
	if err != nil {
		t.Fatalf("hotspots: %v", err)
	}
	if len(hotspots) == 0 {
		t.Errorf("expected hotspots, got none")
	}

	overview, err := collector.Overview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.TotalTraces != 2 {
		t.Errorf("expected 2 total traces, got %d", overview.TotalTraces)
	}

	// 4. Recent
	recent, err := collector.Recent(5)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 2 {
		t.Errorf("expected 2 recent traces, got %d", len(recent))
	}

	// 5. Ingest error payload & fallback
	errEv, _ := collector.IngestReport(ProfilerReportPayload{
		ErrorMsg: "syntax error",
	})
	if errEv.StatusCode != 500 {
		t.Errorf("expected status 500 for error payload, got %d", errEv.StatusCode)
	}

	// 6. Malformed OTLP
	if _, err := collector.IngestOTLP([]byte(`{invalid}`)); err == nil {
		t.Errorf("expected error for invalid OTLP JSON")
	}

	// 7. Dynamic GetStore config
	collectorWithFn := NewCollector(Config{
		GetStore: func() *db.Store { return store },
	})
	if collectorWithFn.store() != store {
		t.Errorf("expected store from GetStore callback")
	}
}

// TestIngestOTLP_PreservesNonStringAttributeMetadata pins the metadata type
// contract of Collector.IngestOTLP: OTLP attributes are oneof-typed, and
// TraceEvent.Metadata — the OnTrace broadcast payload and the metadata_json
// column — must carry each attribute's actual Go type (bool, int64, float64,
// string). Storing OTLPVal.StringValue alone turned every numeric/boolean
// attribute (http.status_code, cpu.usage_pct, feature.enabled) into an empty
// string even though the same record's typed fields were populated from the
// very same attributes.
func TestIngestOTLP_PreservesNonStringAttributeMetadata(t *testing.T) {
	var captured []TraceEvent
	collector := NewCollector(Config{
		OnTrace: func(ev TraceEvent) { captured = append(captured, ev) },
	})

	payload := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "checkout"}}]},
			"scopeSpans": [{"spans": [{
				"traceId": "trace-1",
				"spanId": "span-1",
				"name": "controller.handle",
				"attributes": [
					{"key": "feature.enabled", "value": {"boolValue": true}},
					{"key": "cpu.usage_pct", "value": {"doubleValue": 77.5}},
					{"key": "http.status_code", "value": {"intValue": "404"}},
					{"key": "client.id", "value": {"intValue": "42"}},
					{"key": "span.kind", "value": {"stringValue": "web"}}
				]
			}]}]
		}]
	}`

	count, err := collector.IngestOTLP([]byte(payload))
	if err != nil {
		t.Fatalf("IngestOTLP: %v", err)
	}
	if count != 1 || len(captured) != 1 {
		t.Fatalf("expected 1 span captured, got count=%d captured=%d", count, len(captured))
	}

	meta := captured[0].Metadata
	for _, c := range []struct {
		key  string
		want any
	}{
		{"feature.enabled", true},
		{"cpu.usage_pct", 77.5},
		{"http.status_code", int64(404)},
		{"client.id", int64(42)},
		{"span.kind", "web"},
	} {
		got, ok := meta[c.key]
		if !ok {
			t.Errorf("metadata[%q] missing (have %v)", c.key, meta)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("metadata[%q] = %#v (%T), want %#v (%T)", c.key, got, got, c.want, c.want)
		}
	}
}

// TestIngestOTLP_SharedTraceIDKeepsAllSpans pins the persistence contract
// for multi-span OTLP traces: every span of a trace shares one traceId, but
// runtime_traces.trace_id is the PRIMARY KEY and InsertTrace is a plain
// INSERT, so persisted rows must be keyed per span (traceId-spanId;
// span-spanId when only the spanId exists; traceId plus a minted suffix when
// only the traceId exists; randomID when neither). Keying
// by the group ID made spans 2..N fail the UNIQUE constraint and vanish —
// the error was only logged while IngestOTLP still returned the full
// count, so ProfilerOverview/RecentTraces/Hotspots undercounted silently.
// The broadcast event keeps the raw traceId; only the row key changes.
func TestIngestOTLP_SharedTraceIDKeepsAllSpans(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var captured []TraceEvent
	collector := NewCollector(Config{
		Store:   store,
		OnTrace: func(ev TraceEvent) { captured = append(captured, ev) },
	})

	payload := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "checkout"}}]},
			"scopeSpans": [{"spans": [
				{
					"traceId": "trace-shared", "spanId": "span-1",
					"name": "controller.handle",
					"attributes": [
						{"key": "code.filepath", "value": {"stringValue": "src/controller.go"}},
						{"key": "code.function", "value": {"stringValue": "controller.handle"}}
					]
				},
				{
					"traceId": "trace-shared", "spanId": "span-2",
					"name": "db.query",
					"attributes": [
						{"key": "code.filepath", "value": {"stringValue": "src/db.go"}},
						{"key": "code.function", "value": {"stringValue": "db.query"}}
					]
				},
				{
					"traceId": "trace-solo", "spanId": "span-3",
					"name": "solo.run", "attributes": []
				}
			]}]
		}]
	}`

	count, err := collector.IngestOTLP([]byte(payload))
	if err != nil {
		t.Fatalf("IngestOTLP: %v", err)
	}
	if count != 3 || len(captured) != 3 {
		t.Fatalf("expected 3 spans ingested and captured, got count=%d captured=%d", count, len(captured))
	}

	// The broadcast keeps the raw traceId — row keying must not leak into
	// the event stream.
	for _, ev := range captured[:2] {
		if ev.TraceID != "trace-shared" {
			t.Errorf("broadcast TraceID = %q, want the raw \"trace-shared\"", ev.TraceID)
		}
	}

	// Every span must persist: overview counts all three...
	overview, err := store.ProfilerOverview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.TotalTraces != 3 {
		t.Errorf("TotalTraces = %d, want 3 (spans 2..N of a shared traceId must not be dropped)", overview.TotalTraces)
	}

	// ...and the rows are keyed per span under the documented scheme.
	rows, err := store.RecentTraces(10)
	if err != nil {
		t.Fatalf("recent traces: %v", err)
	}
	rowIDs := map[string]bool{}
	for _, r := range rows {
		rowIDs[r.TraceID] = true
	}
	for _, want := range []string{"trace-shared-span-1", "trace-shared-span-2", "trace-solo-span-3"} {
		if !rowIDs[want] {
			t.Errorf("persisted row %q missing (have %v)", want, rowIDs)
		}
	}
}

// otlpSpanDoc builds a single-span OTLP/JSON document from span members given as
// `"key":value` strings. One envelope for every case here: a hand-written second
// literal once produced unbalanced braces, and the resulting SYNTAX error looked
// like a rejected field rather than a broken fixture.
func otlpSpanDoc(members ...string) string {
	span := `"traceId":"t-enum","spanId":"s-enum","name":"ChargeCard"` +
		`,"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000000050000000"`
	for _, m := range members {
		if m != "" {
			span += `,` + m
		}
	}
	return `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name",` +
		`"value":{"stringValue":"payments"}}]},"scopeSpans":[{"spans":[{` + span + `}]}]}]}`
}

// TestIngestOTLP_EnumFieldSpellings pins OTLP/JSON enum decoding for BOTH enum
// fields, which share one root cause: each was declared with a single spelling.
//
// Canonical ProtoJSON (protobuf.dev/programming-guides/json, verified 2026-09-08)
// renders an enum as its NAME string -- "Parsers accept both enum names and
// integer values" -- so a compliant exporter must send {"code":"STATUS_CODE_ERROR"}.
// `Code int` could not hold that, and Go's encoding/json raises UnmarshalTypeError
// for the WHOLE document: IngestOTLP returned (0, err) and handlers.go answered
// HTTP 400 on the standard /v1/traces endpoint, so every span in the batch was
// lost -- silently discarding the error spans that motivated the export.
//
// `Kind string` was the mirror image: fine with the NAME, fatal for a bare
// integer, measured as
// `json: cannot unmarshal number into Go struct field ...spans.0.kind of type string`.
//
// The bare-integer status form is the CONTROL: it worked before the fix and must
// keep working, so a failure here means the fixture broke rather than the code.
func TestIngestOTLP_EnumFieldSpellings(t *testing.T) {
	cases := []struct {
		name    string
		member  string
		wantErr bool
		wantHS  int // expected StatusCode; 0 = not asserted
	}{
		// status.code -- the spec-canonical name form and the tolerated variants.
		{"status_error_name", `"status":{"code":"STATUS_CODE_ERROR","message":"card declined"}`, false, 500},
		{"status_ok_name", `"status":{"code":"STATUS_CODE_OK"}`, false, 200},
		{"status_unset_name", `"status":{"code":"STATUS_CODE_UNSET"}`, false, 200},
		{"status_error_bare_int", `"status":{"code":2}`, false, 500},
		{"status_error_quoted_int", `"status":{"code":"2"}`, false, 500},
		// Unknown enum NAMES stay an error on purpose: the fix stops the endpoint
		// rejecting valid payloads, it does not start inventing status values.
		{"status_unknown_name", `"status":{"code":"STATUS_CODE_BOGUS"}`, true, 0},
		// span.kind -- same root cause, opposite spelling.
		{"kind_name", `"kind":"SPAN_KIND_INTERNAL"`, false, 0},
		{"kind_bare_int", `"kind":3`, false, 0},
		{"kind_quoted_int", `"kind":"3"`, false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ev *TraceEvent
			c := NewCollector(Config{OnTrace: func(e TraceEvent) { ev = &e }})

			count, err := c.IngestOTLP([]byte(otlpSpanDoc(tc.member)))

			if tc.wantErr {
				if err == nil {
					t.Fatalf("want a rejection for %s, got count=%d", tc.member, count)
				}
				return
			}
			if err != nil {
				t.Fatalf("FAIL: valid OTLP enum spelling rejected, losing the whole batch: member=%s err=%v count=%d", tc.member, err, count)
			}
			if count != 1 || ev == nil {
				t.Fatalf("span lost: count=%d captured=%v (member=%s)", count, ev != nil, tc.member)
			}
			if ev.ServiceName != "payments" {
				t.Fatalf("SETUP: service.name = %q, fixture envelope is wrong", ev.ServiceName)
			}
			if tc.wantHS != 0 && ev.StatusCode != tc.wantHS {
				t.Errorf("StatusCode = %d, want %d for %s", ev.StatusCode, tc.wantHS, tc.member)
			}
			if tc.name == "status_error_name" && ev.ErrorMsg != "card declined" {
				// Exact: collector.go assigns status.message verbatim, so equality
				// is stricter than a substring check and needs no extra import.
				t.Errorf("status message = %q, want %q", ev.ErrorMsg, "card declined")
			}
		})
	}
}

// TestIngestOTLP_OneofDefaultsKeepTheirType pins that an AnyValue member set to
// its DEFAULT value survives into metadata carrying its own type.
//
// AnyValue is a protobuf oneof, and common.proto states "it is valid for all
// values to be unspecified in which case this AnyValue is considered to be
// 'empty'" -- unspecified and set-to-default are therefore DIFFERENT states, and
// protojson marks the chosen member by emitting its key: {"boolValue":false} is
// not {}. OTLPVal's four plain fields could not record that distinction, and
// otlpAttributeValue chose a type by testing which field was non-zero, so
// false / 0 / 0.0 were recorded as "" (measured: 4 of 4 default-valued members
// corrupted). For this gateway those are ordinary values: temperature=0.0,
// seed=0, stream=false, retry.count=0.
//
// Damage was confined to metadata -- the typed columns already held zero, so
// CPUUsagePct is asserted UNCHANGED below rather than claimed as fixed.
//
// Controls (non-zero members, the unspecified value, explicit null) must behave
// identically before AND after the fix, so a control failing here means the
// fixture broke rather than the code.
//
// The int64 rows below are quoted for historical reasons only: the old
// `int64,string` tag REQUIRED that spelling. Round 16 removed the tag, so both
// spellings are accepted now and TestIngestOTLP_Int64SpellingTolerance pins that
// -- quoting here is no longer load-bearing.
func TestIngestOTLP_OneofDefaultsKeepTheirType(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  any
	}{
		// The defect: members explicitly set to their default.
		{"bool_false", `{"boolValue":false}`, false},
		{"int_zero", `{"intValue":"0"}`, int64(0)},
		{"double_zero", `{"doubleValue":0}`, float64(0)},
		{"double_written_zero", `{"doubleValue":0.0}`, float64(0)},
		// Controls -- expected to pass on both sides of the fix.
		{"bool_true", `{"boolValue":true}`, true},
		{"int_nonzero", `{"intValue":"42"}`, int64(42)},
		{"double_nonzero", `{"doubleValue":0.5}`, 0.5},
		{"string_value", `{"stringValue":"hello"}`, "hello"},
		{"string_empty", `{"stringValue":""}`, ""},
		{"unspecified", `{}`, ""}, // legal per the proto: AnyValue "empty"
		{"explicit_null", `null`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ev *TraceEvent
			c := NewCollector(Config{OnTrace: func(e TraceEvent) { ev = &e }})
			doc := otlpSpanDoc(`"attributes":[{"key":"probe","value":` + tc.value + `}]`)

			count, err := c.IngestOTLP([]byte(doc))
			if err != nil || count != 1 || ev == nil {
				t.Fatalf("SETUP: ingest failed count=%d err=%v captured=%v", count, err, ev != nil)
			}
			if ev.ServiceName != "payments" {
				t.Fatalf("SETUP: service.name = %q, fixture envelope is wrong", ev.ServiceName)
			}
			got, ok := ev.Metadata["probe"]
			if !ok {
				t.Fatalf("FAIL: attribute vanished from metadata entirely (value=%s)", tc.value)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FAIL: metadata[probe] = %#v (%T), want %#v (%T) for value %s",
					got, got, tc.want, tc.want, tc.value)
			}
			if tc.name == "double_zero" && ev.CPUUsagePct != 0 {
				t.Errorf("typed column moved unexpectedly: CPUUsagePct = %v, want 0", ev.CPUUsagePct)
			}
		})
	}
}

// TestIngestOTLP_CompositeMembersCarryContent pins OTLP/JSON composite attribute
// decoding into TraceEvent.Metadata (the OnTrace broadcast and metadata_json).
//
// Renamed from TestIngestOTLP_CompositeMembersDoNotFailTheBatch, which round 14
// wrote to pin the THEN-OPEN gap: arrayValue/kvlistValue/bytesValue (common.proto
// AnyValue fields 5-7) had no Go field, so Go's decoder ignored those keys and the
// attribute was recorded as "" -- measured at 7 of 7 composite shapes losing their
// content, with no error raised. That test asserted "" on purpose so that whoever
// closed the gap would have to do it deliberately; this round closed it, and this
// is that deliberate update. The batch-safety assertion the old name referred to
// is kept as a subtest, since it is still the contract round 12 established.
//
// Each shape below is fixed by the spec rather than invented. An array becomes
// []any with its elements unwrapped recursively, so a nested default-valued
// member keeps its type ({"intValue":"0"} yields int64(0), not ""). A kvlist
// becomes map[string]any; the proto requires unique keys and the decoded slice
// preserves input order, so a duplicate folds last-one-wins deterministically
// instead of unpredictably. Bytes keep protojson's base64 text verbatim, because
// json.Marshal([]byte) re-emits the identical string while decoding would add an
// invalid-base64 failure path. And an empty container stays a VALUE -- []any{} or
// map[string]any{} -- since the proto says the array "may be empty (contain 0
// elements)", which is distinct from the member being absent.
//
// Controls at the bottom (scalars, unspecified {}, explicit null) pass before AND
// after the fix, so a failure there means the fixture broke or round 14's presence
// fix regressed -- not this round's change.
func TestIngestOTLP_CompositeMembersCarryContent(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  any
	}{
		{"array_of_strings", `{"arrayValue":{"values":[{"stringValue":"END"},{"stringValue":"STOP"}]}}`,
			[]any{"END", "STOP"}},
		{"array_keeps_nested_defaults", `{"arrayValue":{"values":[{"intValue":"0"},{"boolValue":false},{"doubleValue":1.5}]}}`,
			[]any{int64(0), false, 1.5}},
		{"kvlist", `{"kvlistValue":{"values":[{"key":"a","value":{"stringValue":"1"}},{"key":"b","value":{"intValue":"7"}}]}}`,
			map[string]any{"a": "1", "b": int64(7)}},
		// The chosen duplicate-key semantics: deterministic last-one-wins.
		{"kvlist_duplicate_key_last_wins", `{"kvlistValue":{"values":[{"key":"a","value":{"intValue":"1"}},{"key":"a","value":{"intValue":"2"}}]}}`,
			map[string]any{"a": int64(2)}},
		{"bytes_verbatim_base64", `{"bytesValue":"aGk="}`, "aGk="},
		{"empty_array", `{"arrayValue":{"values":[]}}`, []any{}},
		{"empty_kvlist", `{"kvlistValue":{"values":[]}}`, map[string]any{}},
		{"array_no_values_key", `{"arrayValue":{}}`, []any{}},
		{"array_null_member", `{"arrayValue":null}`, []any{}},
		// AnyValue is recursive; a one-level unwrap would still lose the inner value.
		{"nested_composite", `{"arrayValue":{"values":[{"arrayValue":{"values":[{"boolValue":false}]}},` +
			`{"kvlistValue":{"values":[{"key":"k","value":{"intValue":"0"}}]}}]}}`,
			[]any{[]any{false}, map[string]any{"k": int64(0)}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ev *TraceEvent
			c := NewCollector(Config{OnTrace: func(e TraceEvent) { ev = &e }})
			count, err := c.IngestOTLP([]byte(otlpSpanDoc(
				`"attributes":[{"key":"probe","value":` + tc.value + `}]`)))
			// Batch safety first: this is what the old test name promised, and it
			// must stay true for every shape -- rejecting a document for one
			// unmodelled member is the blast radius round 12 removed.
			if err != nil || count != 1 || ev == nil {
				t.Fatalf("composite attribute failed the whole batch: count=%d err=%v", count, err)
			}
			got, ok := ev.Metadata["probe"]
			if !ok {
				t.Fatalf("FAIL: composite attribute vanished from metadata entirely")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FAIL: metadata[probe] = %#v (%T), want %#v (%T) for %s",
					got, got, tc.want, tc.want, tc.value)
			}
		})
	}

	// Controls: unchanged by this round, and expected to pass pre-fix too.
	for _, tc := range []struct {
		name  string
		value string
		want  any
	}{
		{"ctl_bool_true", `{"boolValue":true}`, true},
		{"ctl_int", `{"intValue":"42"}`, int64(42)},
		{"ctl_double", `{"doubleValue":0.5}`, 0.5},
		{"ctl_string", `{"stringValue":"hello"}`, "hello"},
		{"ctl_unspecified", `{}`, ""},
		{"ctl_explicit_null", `null`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ev *TraceEvent
			c := NewCollector(Config{OnTrace: func(e TraceEvent) { ev = &e }})
			count, err := c.IngestOTLP([]byte(otlpSpanDoc(
				`"attributes":[{"key":"probe","value":` + tc.value + `}]`)))
			if err != nil || count != 1 || ev == nil {
				t.Fatalf("CONTROL BROKEN (not this round's change): count=%d err=%v", count, err)
			}
			if got := ev.Metadata["probe"]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("CONTROL BROKEN (not this round's change): metadata[probe] = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestIngestOTLP_Int64SpellingTolerance pins both spec-permitted spellings of
// AnyValue's int_value member.
//
// The field was `IntValue int64` with a `,string` tag. Go's `,string` option
// REQUIRES the quoted form, so a bare number raised UnmarshalTypeError, which
// aborts json.Unmarshal for the WHOLE document: IngestOTLP returned (0, err) and
// handlers.go answered HTTP 400 on the standard POST /v1/traces endpoint, so one
// differently-spelled value cost every span in the batch. Measured pre-fix: bare
// 42, bare 0, bare MaxInt64 and a bare int nested in an arrayValue all lost the
// batch with `cannot unmarshal number into Go struct field ...intValue`.
//
// Canonical ProtoJSON (quoted from the fetched page, verified 2026-09-08) on
// int64/fixed64/uint64: "JSON value will be a decimal string. Either numbers or
// strings are accepted. Empty strings are invalid." The control rows below are
// that last sentence and the pre-existing quoted form -- both must hold before
// AND after tolerance, so over-tolerance (silently turning garbage into 0) fails
// the same test that under-tolerance does.
//
// MaxInt64 is asserted because the quoted spelling partly exists to dodge float64
// rounding: a tolerant decoder must stay exact at 2**63-1. The arrayValue row
// proves the tolerance RECURSES -- nested elements re-enter this same decoder, so
// a fix wired only into the top-level field would still lose it.
//
// The int64 value-type assertion matters too: the named field type is otlpInt64
// but metadata must keep carrying plain int64 (otlpAttributeValue widens it back),
// otherwise this change would silently alter metadata_json for every consumer.
func TestIngestOTLP_Int64SpellingTolerance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  any
	}{
		// The defect: bare numbers were fatal.
		{"bare_int", `{"intValue":42}`, int64(42)},
		{"bare_zero", `{"intValue":0}`, int64(0)},
		{"bare_max_int64_exact", `{"intValue":9223372036854775807}`, int64(9223372036854775807)},
		// Controls: the quoted spelling the repo already relied on, unchanged.
		{"quoted_int", `{"intValue":"42"}`, int64(42)},
		{"quoted_zero", `{"intValue":"0"}`, int64(0)},
		{"quoted_max_int64_exact", `{"intValue":"9223372036854775807"}`, int64(9223372036854775807)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ev *TraceEvent
			c := NewCollector(Config{OnTrace: func(e TraceEvent) { ev = &e }})
			count, err := c.IngestOTLP([]byte(otlpSpanDoc(
				`"attributes":[{"key":"probe","value":` + tc.value + `}]`)))
			if err != nil || count != 1 || ev == nil {
				t.Fatalf("FAIL: spec-permitted intValue spelling rejected, losing the whole batch: value=%s count=%d err=%v",
					tc.value, count, err)
			}
			got := ev.Metadata["probe"]
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FAIL: metadata[probe] = %#v (%T), want %#v (%T)", got, got, tc.want, tc.want)
			}
			if _, isFloat := got.(float64); isFloat {
				t.Errorf("FAIL: int64 attribute arrived as float64 -- precision is not guaranteed")
			}
		})
	}

	// Tolerance must not become inventing values: these stay errors, per the same
	// spec sentence ("Empty strings are invalid") and the overflow boundary.
	for _, tc := range []struct{ name, value string }{
		{"non_numeric", `{"intValue":"abc"}`},
		{"empty_string", `{"intValue":""}`},
		{"overflow", `{"intValue":9223372036854775808}`},
	} {
		t.Run(tc.name+"_stays_an_error", func(t *testing.T) {
			c := NewCollector(Config{})
			count, err := c.IngestOTLP([]byte(otlpSpanDoc(
				`"attributes":[{"key":"probe","value":` + tc.value + `}]`)))
			if err == nil {
				t.Errorf("FAIL: invalid intValue %s accepted (count=%d) -- tolerance hid a sender bug", tc.value, count)
			}
		})
	}

	t.Run("bare_int_inside_array_recurses", func(t *testing.T) {
		var ev *TraceEvent
		c := NewCollector(Config{OnTrace: func(e TraceEvent) { ev = &e }})
		count, err := c.IngestOTLP([]byte(otlpSpanDoc(
			`"attributes":[{"key":"probe","value":{"arrayValue":{"values":[{"intValue":7},{"intValue":"8"}]}}}]`)))
		if err != nil || count != 1 || ev == nil {
			t.Fatalf("FAIL: bare int nested in arrayValue rejected, losing the batch: count=%d err=%v", count, err)
		}
		want := []any{int64(7), int64(8)}
		if got := ev.Metadata["probe"]; !reflect.DeepEqual(got, want) {
			t.Errorf("FAIL: nested intValue = %#v, want %#v -- tolerance did not recurse", got, want)
		}
	})
}
