package models

import (
	"encoding/json"
	"math"
	"testing"
)

// FuzzCalculateCostDetailed drives the cost arithmetic with arbitrary token
// counts and prices.
//
// Both inputs are externally influenced: token counts arrive from agent
// telemetry (IPC/MCP/proxy) and prices from the models.dev sync or a POST to
// /api/models. The result is persisted and then JSON-serialized on nearly every
// dashboard endpoint -- and encoding/json REFUSES to marshal NaN or Inf, so a
// single poisoned figure does not just look wrong, it breaks the response.
func FuzzCalculateCostDetailed(f *testing.F) {
	seeds := []struct {
		prompt, completion, cached int64
		in, out, cache             float64
	}{
		{1000, 500, 0, 3.0, 15.0, 0.3},
		{0, 0, 0, 0, 0, 0},
		{-1, -1, -1, 1, 1, 1},
		{1 << 40, 1 << 40, 1 << 39, 1e6, 1e6, 1e6},
		{math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxFloat64, math.MaxFloat64, 0},
		{100, 100, 200, -5, -5, -5},
	}
	for _, s := range seeds {
		f.Add(s.prompt, s.completion, s.cached, s.in, s.out, s.cache)
	}

	f.Fuzz(func(t *testing.T, prompt, completion, cached int64, inP, outP, cacheP float64) {
		// Skip prices that are already NaN/Inf: the registry is what must
		// reject those, and it is asserted separately below.
		if math.IsNaN(inP) || math.IsInf(inP, 0) ||
			math.IsNaN(outP) || math.IsInf(outP, 0) ||
			math.IsNaN(cacheP) || math.IsInf(cacheP, 0) {
			t.Skip()
		}

		r := NewRegistry()
		r.Upsert(ModelInfo{
			ID:                 "fuzz-model",
			ModelID:            "fuzz-model",
			Provider:           "Fuzz",
			InputPricePerM:     inP,
			OutputPricePerM:    outP,
			CacheReadPricePerM: cacheP,
		})

		cost, savings := r.CalculateCostDetailed("Fuzz", "fuzz-model", prompt, completion, cached)

		for name, v := range map[string]float64{"cost": cost, "savings": savings} {
			if math.IsNaN(v) {
				t.Fatalf("%s is NaN (prompt=%d completion=%d cached=%d in=%g out=%g cache=%g); "+
					"encoding/json cannot marshal it and every endpoint carrying this record fails",
					name, prompt, completion, cached, inP, outP, cacheP)
			}
			if math.IsInf(v, 0) {
				t.Fatalf("%s is %g (prompt=%d completion=%d cached=%d in=%g out=%g cache=%g); "+
					"encoding/json cannot marshal it and every endpoint carrying this record fails",
					name, v, prompt, completion, cached, inP, outP, cacheP)
			}
			if v < 0 {
				t.Fatalf("%s is negative (%g); spend and savings are accumulated into budget "+
					"totals, where a negative value refunds the caller's quota", name, v)
			}
			if _, err := json.Marshal(v); err != nil {
				t.Fatalf("%s (%g) is not JSON-serializable: %v", name, v, err)
			}
		}
	})
}

// TestUpsert_RejectsPoisonPrices covers the two shapes fuzzing surfaced, at the
// boundary where they actually enter: POST /api/models feeds Upsert directly,
// with no validation of its own.
func TestUpsert_RejectsPoisonPrices(t *testing.T) {
	cases := []struct {
		name          string
		in, out, cach float64
	}{
		{"NaN", math.NaN(), math.NaN(), math.NaN()},
		{"positive infinity", math.Inf(1), math.Inf(1), math.Inf(1)},
		{"negative infinity", math.Inf(-1), math.Inf(-1), math.Inf(-1)},
		{"negative rates", -5, -5, -5},
		{"max float", math.MaxFloat64, math.MaxFloat64, math.MaxFloat64},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			r.Upsert(ModelInfo{
				ID: "poison", ModelID: "poison", Provider: "Test",
				InputPricePerM: tc.in, OutputPricePerM: tc.out, CacheReadPricePerM: tc.cach,
			})

			m, ok := r.GetWithProvider("Test", "poison")
			if !ok {
				t.Fatal("model was not stored")
			}
			for name, v := range map[string]float64{
				"InputPricePerM":     m.InputPricePerM,
				"OutputPricePerM":    m.OutputPricePerM,
				"CacheReadPricePerM": m.CacheReadPricePerM,
			} {
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
					t.Errorf("%s stored as %g; a non-finite or negative rate must be "+
						"normalized away before it reaches cost arithmetic", name, v)
				}
			}

			cost, savings := r.CalculateCostDetailed("Test", "poison", 1_000_000, 500_000, 100_000)
			if _, err := json.Marshal(map[string]float64{"cost": cost, "savings": savings}); err != nil {
				t.Errorf("computed figures are not JSON-serializable: %v", err)
			}
			if cost < 0 || savings < 0 {
				t.Errorf("negative cost=%g savings=%g; these accumulate into the quota "+
					"guardrail's spend totals, where a negative value lifts the cap", cost, savings)
			}
		})
	}
}

// TestUpsert_KeepsLegitimatePrices: the sanitizer must not round real pricing
// away. Sub-cent per-million rates are ordinary for cached input tokens.
func TestUpsert_KeepsLegitimatePrices(t *testing.T) {
	r := NewRegistry()
	r.Upsert(ModelInfo{
		ID: "real", ModelID: "real", Provider: "Test",
		InputPricePerM: 3.0, OutputPricePerM: 15.0, CacheReadPricePerM: 0.30,
	})

	m, ok := r.GetWithProvider("Test", "real")
	if !ok {
		t.Fatal("model was not stored")
	}
	if m.InputPricePerM != 3.0 || m.OutputPricePerM != 15.0 || m.CacheReadPricePerM != 0.30 {
		t.Fatalf("legitimate prices were altered: in=%g out=%g cache=%g",
			m.InputPricePerM, m.OutputPricePerM, m.CacheReadPricePerM)
	}

	// 1M input + 1M output at those rates, no cache.
	cost, _ := r.CalculateCostDetailed("Test", "real", 1_000_000, 1_000_000, 0)
	if want := 18.0; cost != want {
		t.Errorf("cost = %g, want %g", cost, want)
	}
}
