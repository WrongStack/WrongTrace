package models

import "testing"

// models.dev files a provider's own models under bare keys, the same way
// resellers file theirs. The bare name must still resolve to the owner, keep
// doing so across syncs and custom reseller overrides, and a gateway listing
// one model under two upstreams must keep both as distinct entries.
func TestImportModelsDev_BareNameOwner(t *testing.T) {
	const bare = `{
 "aihubmix": {"name":"AIHubMix","models":{"gpt-4o":{"name":"GPT-4o","cost":{"input":5,"output":20},"limit":{"context":128000}},
                                          "only-here-1":{"name":"X","cost":{"input":1,"output":1},"limit":{"context":1000}}}},
 "openai":   {"name":"OpenAI",  "models":{"gpt-4o":{"name":"GPT-4o","cost":{"input":2.5,"output":10},"limit":{"context":128000}}}}
}`
	get := func(r *Registry, id string) string {
		m, _ := r.Get(id)
		return m.ID
	}

	r := NewDefaultRegistry()
	for sync := 1; sync <= 2; sync++ {
		if _, err := r.ImportModelsDevJSON([]byte(bare)); err != nil {
			t.Fatal(err)
		}
		if got := get(r, "gpt-4o"); got != "openai/gpt-4o" {
			t.Fatalf("sync %d: Get(gpt-4o) = %s; want openai/gpt-4o", sync, got)
		}
	}
	if got := get(r, "only-here-1"); got != "aihubmix/only-here-1" {
		t.Fatalf("reseller-only model = %s", got)
	}

	// A custom reseller override does not take the bare name on the next sync.
	r.Upsert(ModelInfo{ID: "openrouter/gpt-4o", Provider: "OpenRouter", InputPricePerM: 5, OutputPricePerM: 15, IsCustom: true})
	if _, err := r.ImportModelsDevJSON([]byte(bare)); err != nil {
		t.Fatal(err)
	}
	if got := get(r, "gpt-4o"); got != "openai/gpt-4o" {
		t.Fatalf("after custom reseller + sync: Get(gpt-4o) = %s; want openai/gpt-4o", got)
	}

	// Without a seeded owner, other providers' namespaces name it.
	e := NewRegistry()
	const hinted = `{
 "aihubmix":   {"models":{"gpt-4o":{"cost":{"input":5,"output":20},"limit":{"context":1}}}},
 "openai":     {"models":{"gpt-4o":{"cost":{"input":2.5,"output":10},"limit":{"context":1}}}},
 "openrouter": {"models":{"openai/gpt-4o":{"cost":{"input":3,"output":12},"limit":{"context":1}}}}
}`
	if _, err := e.ImportModelsDevJSON([]byte(hinted)); err != nil {
		t.Fatal(err)
	}
	if got := get(e, "gpt-4o"); got != "openai/gpt-4o" {
		t.Fatalf("hinted catalog: Get(gpt-4o) = %s; want openai/gpt-4o", got)
	}
}

func TestImportModelsDev_GatewayUpstreamsStayDistinct(t *testing.T) {
	const gw = `{"gw":{"name":"Gateway","models":{
  "openai/gpt-4o":{"cost":{"input":2.5,"output":10},"limit":{"context":1}},
  "azure/gpt-4o": {"cost":{"input":5,"output":15},"limit":{"context":1}}}}}`
	r := NewRegistry()
	n, err := r.ImportModelsDevJSON([]byte(gw))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := r.GetProvider("gw")
	seen := map[string]bool{}
	for _, m := range p.Models {
		if seen[m.ID] {
			t.Fatalf("provider lists %s twice", m.ID)
		}
		seen[m.ID] = true
	}
	if n != 2 || p.ModelCount != 2 {
		t.Fatalf("imported %d, ModelCount %d; want 2, 2", n, p.ModelCount)
	}
	for id, want := range map[string]float64{"gw/azure/gpt-4o": 5, "gw/openai/gpt-4o": 2.5} {
		if m, _ := r.Get(id); m.InputPricePerM != want {
			t.Fatalf("Get(%s) input = %v; want %v", id, m.InputPricePerM, want)
		}
	}
}
