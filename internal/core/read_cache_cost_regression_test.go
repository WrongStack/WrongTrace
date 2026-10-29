package core

import (
	"math"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/models"
)

func TestRecordReadEventFallbackPricesCachedInput(t *testing.T) {
	previous := models.Global
	models.Global = models.NewRegistry()
	t.Cleanup(func() { models.Global = previous })
	models.Global.Upsert(models.ModelInfo{ID: "fixture-model", ModelID: "fixture-model", Provider: "owner", ProviderID: "owner", InputPricePerM: 2, CacheReadPricePerM: 0.25, IsCanonical: true})
	models.Global.Upsert(models.ModelInfo{ID: "reseller/fixture-model", ModelID: "fixture-model", Provider: "reseller", ProviderID: "reseller", InputPricePerM: 4, CacheReadPricePerM: 0.5})
	models.Global.Upsert(models.ModelInfo{ID: "missing-cache-fixture", Provider: "owner", ProviderID: "owner", InputPricePerM: 2, CacheReadPricePerM: 0})
	models.Global.Upsert(models.ModelInfo{ID: "free-model-fixture", Provider: "owner", ProviderID: "owner"})
	for _, tc := range []struct {
		name, model, provider string
		prompt, cached        int64
		explicit, want        float64
	}{
		{"uncached-control", "fixture-model", "owner", 1000, 0, 0, 0.002},
		{"partial-cache", "fixture-model", "owner", 1000, 800, 0, 0.0006},
		{"all-cached", "fixture-model", "owner", 1000, 1000, 0, 0.00025},
		{"missing-cache-price-uses-estimate", "missing-cache-fixture", "owner", 1000, 1000, 0, 0.0005},
		{"free-model-control", "free-model-fixture", "owner", 1000, 1000, 0, 0},
		{"provider-specific-cache", "fixture-model", "reseller", 1000, 800, 0, 0.0012},
		{"explicit-cost-control", "fixture-model", "owner", 1000, 800, 0.123, 0.123},
		{"empty-usage-control", "fixture-model", "owner", 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, store := newTestEngine(t)
			t.Cleanup(e.Close)
			if err := e.RecordReadEvent(db.FileReadRecord{ReadID: "read", FilePath: "subject.go", ModelName: tc.model, Provider: tc.provider, PromptTokens: tc.prompt, CachedTokens: tc.cached, CostUSD: tc.explicit}); err != nil {
				t.Fatal(err)
			}
			rows, err := store.GetRecentFileReads(10)
			if err != nil || len(rows) != 1 {
				t.Fatalf("reads=%+v err=%v", rows, err)
			}
			t.Logf("EXPECTED: recorded cost=%.8f | ACTUAL: %.8f", tc.want, rows[0].CostUSD)
			if math.Abs(rows[0].CostUSD-tc.want) > 1e-12 {
				t.Fatal("fallback charged cached input at the uncached/canonical rate")
			}
		})
	}
}
