package profiler

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// TestIngestOTLP_NonFiniteDoublesIngestAndStayMarshalable pins AnyValue's
// double_value member against its external contract, not against in-repo
// convention. Canonical ProtoJSON (protobuf.dev/programming-guides/json,
// verified against the fetched artifact 2026-09-08) on float/double:
//
//	"JSON value will be a number or one of the special string values "NaN",
//	 "Infinity", and "-Infinity". Either numbers or strings are accepted.
//	 Empty strings are invalid. Exponent notation is also accepted."
//
// DoubleValue was a plain float64 with no custom decoder, so every QUOTED form
// failed. Measured pre-fix, all four of "NaN", "Infinity", "-Infinity" and Go's
// own "Inf" produced
// `json: cannot unmarshal string into Go struct field ...doubleValue of type
// float64`, which aborts encoding/json for the WHOLE document. IngestOTLP then
// returned (0, err) and handlers.go answered HTTP 400 on POST /v1/traces, so a
// single non-finite gauge reading discarded every span in the batch -- the blast
// radius already removed for the enum fields (round 12) and intValue
// (round 16). double_value was the third and last numeric member.
//
// The STORED form matters as much as the parse. Non-finite values arrive in
// metadata as their canonical TEXT because encoding/json cannot marshal a raw
// NaN/Inf -- asserted by TestIngestOTLP_RawNaNIsUnmarshalable rather than
// assumed -- and IngestOTLP builds metadata_json with
// `metaBytes, _ := json.Marshal(meta)`, DISCARDING that error. Carrying the
// float64 directly would therefore store an empty metadata_json and silently
// lose every attribute of the span, so the wire's own spelling is kept, exactly
// as bytes_value keeps base64 verbatim.
//
// Builds every document through the existing otlpSpanDoc helper (same package)
// so no hand-written envelope can silently unbalance, and declares no
// package-level helper of its own.
func TestIngestOTLP_NonFiniteDoublesIngestAndStayMarshalable(t *testing.T) {
	// attrDoc puts one double_value attribute on the span. The member must be
	// wrapped in its AnyValue object -- handing the bare member to "value":
	// makes the payload invalid OTLP and the failure then describes my
	// fixture, not the code under test.
	attrDoc := func(key, doubleJSON string) []byte {
		return []byte(otlpSpanDoc(`"attributes":[{"key":"` + key +
			`","value":{"doubleValue":` + doubleJSON + `}}]`))
	}

	cases := []struct {
		name      string
		valueJSON string
		wantMeta  any
	}{
		// Controls that must hold before AND after: finite values keep their
		// float64 type, so round 14's and round 15's type pins are unaffected.
		{"finite control stays float64", `1.5`, float64(1.5)},
		{"zero stays float64 (round 14 contract)", `0`, float64(0)},
		// The spec's three canonical quoted forms.
		{"NaN", `"NaN"`, "NaN"},
		{"Infinity", `"Infinity"`, "Infinity"},
		{"negative infinity", `"-Infinity"`, "-Infinity"},
		// Go's own spelling is tolerated and NORMALISED to the spec's text, so
		// the recorded value is the one a compliant reader expects.
		{"Go's Inf normalises to spec text", `"Inf"`, "Infinity"},
		// Spec: "Exponent notation is also accepted."
		{"exponent notation", `"1.5e2"`, float64(150)},
		{"bare exponent", `1.5e2`, float64(150)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []TraceEvent
			c := NewCollector(Config{OnTrace: func(e TraceEvent) { events = append(events, e) }})
			n, err := c.IngestOTLP(attrDoc("PROBE", tc.valueJSON))
			if err != nil || n != 1 {
				t.Fatalf("SETUP/spelling: count=%d err=%v", n, err)
			}
			got, ok := events[0].Metadata["PROBE"]
			if !ok {
				t.Fatalf("attribute vanished: %#v", events[0].Metadata)
			}
			if !reflect.DeepEqual(got, tc.wantMeta) {
				t.Errorf("metadata[PROBE] = %#v (%T), want %#v (%T)",
					got, got, tc.wantMeta, tc.wantMeta)
			}
			// Whatever it is, it must survive the marshal that builds
			// metadata_json, or the attributes are lost silently.
			if _, err := json.Marshal(events[0].Metadata); err != nil {
				t.Errorf("metadata is unmarshalable (%v); collector.go discards that error, so metadata_json would be stored EMPTY", err)
			}
		})
	}
}

// TestIngestOTLP_RawNaNIsUnmarshalable asserts the PREMISE of the text
// representation instead of assuming it: the float64 is kept out of metadata
// only because encoding/json refuses to encode NaN/±Inf. Should that ever
// change, this test says so and metadataValue() should return the float64
// directly rather than text.
func TestIngestOTLP_RawNaNIsUnmarshalable(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := json.Marshal(map[string]any{"probe": v}); err == nil {
			t.Errorf("PREMISE CHANGED: encoding/json now marshals %v; metadataValue()'s text form is no longer required", v)
		}
	}
}

// TestIngestOTLP_NonFiniteDoubleInsideArrayKeepsText covers the path round 15
// created: arrayValue/kvlistValue members are real data now, so a non-finite
// double can arrive one level down, and the same single rejection would still
// cost the entire batch.
func TestIngestOTLP_NonFiniteDoubleInsideArrayKeepsText(t *testing.T) {
	var events []TraceEvent
	c := NewCollector(Config{OnTrace: func(e TraceEvent) { events = append(events, e) }})
	doc := []byte(otlpSpanDoc(`"attributes":[{"key":"PROBE","value":{"arrayValue":{"values":[` +
		`{"doubleValue":"NaN"},{"doubleValue":"-Infinity"},{"doubleValue":2.5}]}}}]`))
	n, err := c.IngestOTLP(doc)
	if err != nil || n != 1 {
		t.Fatalf("non-finite double inside arrayValue lost the whole batch: count=%d err=%v", n, err)
	}
	want := []any{"NaN", "-Infinity", float64(2.5)}
	if got := events[0].Metadata["PROBE"]; !reflect.DeepEqual(got, want) {
		t.Errorf("metadata[PROBE] = %#v, want %#v", got, want)
	}
	if _, err := json.Marshal(events[0].Metadata); err != nil {
		t.Errorf("composite metadata is unmarshalable: %v", err)
	}
}

// TestIngestOTLP_NonFiniteCPUPercentageLeavesColumnZero pins the deliberate
// policy for the NUMERIC column: a non-finite cpu.usage_pct is stored as 0 (the
// unset value) rather than as NaN, because a NaN in RuntimeTraceRecord would
// break every later marshal of that row -- worse than losing one unusable gauge
// reading. The attribute itself is still preserved as text in metadata.
func TestIngestOTLP_NonFiniteCPUPercentageLeavesColumnZero(t *testing.T) {
	var events []TraceEvent
	c := NewCollector(Config{OnTrace: func(e TraceEvent) { events = append(events, e) }})
	n, err := c.IngestOTLP([]byte(otlpSpanDoc(
		`"attributes":[{"key":"cpu.usage_pct","value":{"doubleValue":"Infinity"}}]`)))
	if err != nil || n != 1 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if ev := events[0]; ev.CPUUsagePct != 0 {
		t.Errorf("CPUUsagePct = %v, want 0 (non-finite must not enter the numeric column)", ev.CPUUsagePct)
	}
	if got := events[0].Metadata["cpu.usage_pct"]; !reflect.DeepEqual(got, "Infinity") {
		t.Errorf("metadata lost the real value: %#v, want \"Infinity\"", got)
	}
}

// TestIngestOTLP_InvalidDoubleStillRejected keeps the tolerance narrow: values
// the spec calls invalid must stay errors rather than silently becoming 0.
// Empty strings are explicitly invalid per the spec, so they must not collapse
// to zero either.
func TestIngestOTLP_InvalidDoubleStillRejected(t *testing.T) {
	for _, tc := range []struct{ name, valueJSON string }{
		{"non numeric", `"abc"`},
		{"empty string is invalid per spec", `""`},
		{"partial", `"12abc"`},
		{"object", `{"x":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCollector(Config{})
			n, err := c.IngestOTLP([]byte(otlpSpanDoc(`"attributes":[{"key":"PROBE","value":{"doubleValue":` +
				tc.valueJSON + `}}]`)))
			if err == nil {
				t.Fatalf("invalid doubleValue %s was accepted (count=%d)", tc.valueJSON, n)
			}
			if !strings.Contains(err.Error(), "invalid otlp doubleValue") {
				t.Errorf("invalid value produced an unrelated error: %v", err)
			}
		})
	}
}
