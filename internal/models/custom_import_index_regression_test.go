package models

import "testing"

func TestCustomImportProviderIndexMatchesModel(t *testing.T) {
	for _, provider := range []string{"remote", "local"} {
		r := NewRegistry()
		r.Upsert(ModelInfo{ID: "remote/fixture-model", ModelID: "fixture-model", Provider: provider, ProviderID: provider, InputPricePerM: 99, IsCustom: true})
		body := []byte(`{"remote":{"models":{"fixture-model":{"cost":{"input":2},"limit":{"context":1000}},"other-model":{"cost":{"input":3},"limit":{"context":1000}}}}}`)
		for round := 0; round < 2; round++ {
			if _, err := r.ImportModelsDevJSON(body); err != nil {
				t.Fatal(err)
			}
			stored, _ := r.Get("remote/fixture-model")
			count := 0
			for _, p := range r.AllProviders() {
				if p.ModelCount != len(p.Models) {
					t.Fatal("provider count mismatch")
				}
				for _, m := range p.Models {
					if m.ID == stored.ID {
						count++
						if m != stored {
							t.Fatal("catalog differs from custom model")
						}
					}
				}
			}
			if count != 1 {
				t.Fatalf("override occurrences=%d", count)
			}
			if _, ok := r.Get("remote/other-model"); !ok {
				t.Fatal("neighbor model lost")
			}
		}
	}
}
