package models

import "testing"

func TestVersionedAliasKeepsMostSpecificModel(t *testing.T) {
	r := NewRegistry()
	r.Upsert(ModelInfo{ID: "owner/audit-model", ModelID: "audit-model", Provider: "Owner", ProviderID: "owner", InputPricePerM: 1, IsCanonical: true})
	r.Upsert(ModelInfo{ID: "gateway/audit-model-pro", ModelID: "audit-model-pro", Provider: "Gateway", ProviderID: "gateway", InputPricePerM: 9})
	check := func(alias, want string, price float64) {
		t.Helper()
		got, ok := r.Get(alias)
		if !ok || got.ID != want || r.CalculateCost(alias, 1000000, 0) != price {
			t.Fatalf("alias %s chose %+v, want %s", alias, got, want)
		}
	}
	check("audit-model-pro-20261001", "gateway/audit-model-pro", 9)
	check("audit-model-20261001", "owner/audit-model", 1)
	r.Upsert(ModelInfo{ID: "owner/audit-model-pro", ModelID: "audit-model-pro", Provider: "Owner", ProviderID: "owner", InputPricePerM: 5, IsCanonical: true})
	check("audit-model-pro-20261001", "owner/audit-model-pro", 5)
	check("gateway/audit-model-pro", "gateway/audit-model-pro", 9)
}
