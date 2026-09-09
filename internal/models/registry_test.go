package models

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fixture mirrors the real models.dev/api.json shape: top-level provider
// objects, each with a nested "models" map whose entries carry cost (USD per
// 1M tokens) and limit.context (tokens). Field values copied from the live
// endpoint (2026-08) so parsing assertions match production data.
const fixture = `{
  "hpc-ai": {
    "id": "hpc-ai",
    "name": "HPC-AI",
    "api": "https://api.hpc-ai.com/inference/v1",
    "models": {
      "deepseek/deepseek-v4-pro": {
        "id": "deepseek/deepseek-v4-pro",
        "name": "DeepSeek V4 Pro",
        "description": "Open MoE flagship with million-token context",
        "limit": {"context": 1002000, "output": 128000},
        "cost": {"input": 1.74, "output": 3.48, "cache_read": 0.145}
      },
      "moonshotai/kimi-k2.5": {
        "id": "moonshotai/kimi-k2.5",
        "name": "Kimi K2.5",
        "description": "Earlier Kimi frontier model",
        "limit": {"context": 256000, "output": 256000},
        "cost": {"input": 0.6, "output": 3, "cache_read": 0.1}
      }
    }
  },
  "anthropic": {
    "id": "anthropic",
    "name": "Anthropic",
    "models": {
      "anthropic/claude-opus-4.7": {
        "id": "anthropic/claude-opus-4.7",
        "name": "Claude Opus 4.7",
        "description": "Stronger Opus tier for advanced software work",
        "limit": {"context": 1000000, "output": 128000},
        "cost": {"input": 5, "output": 25, "cache_read": 0.5}
      }
    }
  },
  "ai-router": {
    "id": "ai-router",
    "name": "AI-ROUTER",
    "models": {
      "anthropic/claude-opus-4.7": {
        "id": "anthropic/claude-opus-4.7",
        "name": "Claude Opus 4.7 (via gateway)",
        "limit": {"context": 1000000, "output": 128000},
        "cost": {"input": 7, "output": 35, "cache_read": 0.7}
      },
      "empty/junk-entry": {
        "id": "empty/junk-entry",
        "name": "Junk"
      }
    }
  }
}`

func TestRegistry_StartsEmptyAndFallbackCosts(t *testing.T) {
	r := NewRegistry()

	if got := r.AllModels(); len(got) != 0 {
		t.Fatalf("fresh registry must be empty, got %d models", len(got))
	}

	if _, ok := r.Get("claude-oppus-4-7"); ok {
		t.Fatal("unknown model must not resolve before a sync")
	}

	// Unknown models use the documented fallback estimate ($2/1M in, $8/1M out)
	fallback := r.CalculateCost("my-custom-unregistered-model", 1_000_000, 1_000_000)
	if fallback < 9.99 || fallback > 10.01 {
		t.Errorf("expected $10.00 fallback, got %f", fallback)
	}

	if zero := r.CalculateCost("gpt-4o", 0, 0); zero != 0.0 {
		t.Errorf("expected 0.0 cost for 0 tokens, got %f", zero)
	}
}

func TestImportModelsDevJSON_ParsesRealSchema(t *testing.T) {
	r := NewRegistry()
	n, err := r.ImportModelsDevJSON([]byte(fixture))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// 4 real entries across providers (including multiple providers for claude-opus-4.7)
	if n != 4 {
		t.Fatalf("imported %d models, want 4", n)
	}

	// Canonical resolution without provider specified (Anthropic canonical wins)
	m, ok := r.Get("claude-opus-4-7")
	if !ok {
		t.Fatal("claude-opus-4-7 not found after import")
	}
	if m.Provider != "Anthropic" {
		t.Errorf("provider = %q, want Anthropic (canonical owner must win over gateway copy)", m.Provider)
	}
	if m.InputPricePerM != 5 || m.OutputPricePerM != 25 || m.CacheReadPricePerM != 0.5 {
		t.Errorf("pricing = %+v, want 5/25/0.5 (first-party, not gateway 7/35)", m)
	}
	if m.ContextWindow != 1000000 {
		t.Errorf("context window = %d, want 1000000", m.ContextWindow)
	}
	if !strings.Contains(m.Description, "Opus") {
		t.Errorf("description = %q", m.Description)
	}

	// Gateway specific provider resolution
	gatewayModel, ok := r.Get("ai-router/claude-opus-4.7")
	if !ok {
		t.Fatal("ai-router/claude-opus-4.7 not found")
	}
	if gatewayModel.Provider != "AI-ROUTER" || gatewayModel.InputPricePerM != 7 || gatewayModel.OutputPricePerM != 35 {
		t.Errorf("gateway model pricing incorrect: %+v", gatewayModel)
	}

	// GetWithProvider resolution
	gwProvModel, ok := r.GetWithProvider("ai-router", "claude-opus-4.7")
	if !ok || gwProvModel.InputPricePerM != 7 {
		t.Errorf("GetWithProvider failed: %+v", gwProvModel)
	}

	if _, ok := r.Get("deepseek-v4-pro"); !ok {
		t.Error("nested namespace id deepseek/deepseek-v4-pro must normalize to deepseek-v4-pro")
	}

	// Alias with provider prefix and dots normalizes.
	if _, ok := r.Get("anthropic/claude-opus-4.7"); !ok {
		t.Error("anthropic/claude-opus-4.7 alias must resolve")
	}
}

func TestImportModelsDevJSON_CalculateCostWithLivePricing(t *testing.T) {
	r := NewRegistry()
	if _, err := r.ImportModelsDevJSON([]byte(fixture)); err != nil {
		t.Fatalf("import: %v", err)
	}

	// Canonical Claude Opus 4.7 (Anthropic): 1M in + 1M out = $5 + $25 = $30.
	cost := r.CalculateCost("claude-opus-4-7", 1_000_000, 1_000_000)
	if cost < 29.99 || cost > 30.01 {
		t.Errorf("expected $30.00, got %f", cost)
	}

	// Gateway Claude Opus 4.7 (AI-ROUTER): 1M in + 1M out = $7 + $35 = $42.
	gwCost := r.CalculateCostWithProvider("ai-router", "claude-opus-4-7", 1_000_000, 1_000_000)
	if gwCost < 41.99 || gwCost > 42.01 {
		t.Errorf("expected $42.00 on ai-router, got %f", gwCost)
	}

	// 1M in + 1M out on DeepSeek V4 Pro = $1.74 + $3.48 = $5.22.
	cost = r.CalculateCost("deepseek-v4-pro", 1_000_000, 1_000_000)
	if cost < 5.21 || cost > 5.23 {
		t.Errorf("expected $5.22, got %f", cost)
	}
}

func TestImportModelsDevJSON_ReplacesButKeepsCustom(t *testing.T) {
	r := NewRegistry()
	r.Upsert(ModelInfo{
		ID:            "custom-ollama-qwen",
		Name:          "Custom Ollama Qwen",
		Provider:      "Local / Self-Hosted",
		ContextWindow: 32000,
		IsCustom:      true,
	})

	if _, err := r.ImportModelsDevJSON([]byte(fixture)); err != nil {
		t.Fatalf("import: %v", err)
	}

	m, ok := r.Get("custom-ollama-qwen")
	if !ok || !m.IsCustom {
		t.Fatal("custom model must survive a models.dev import")
	}

	// A custom entry that happens to collide with an imported id is also
	// preserved: the user's local override outranks the remote catalog.
	r.Upsert(ModelInfo{
		ID:              "claude-opus-4-7",
		Name:            "Local Opus Override",
		Provider:        "Local",
		InputPricePerM:  99,
		OutputPricePerM: 99,
		IsCustom:        true,
	})
	if _, err := r.ImportModelsDevJSON([]byte(fixture)); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	m, ok = r.Get("claude-opus-4-7")
	if !ok || !m.IsCustom || m.InputPricePerM != 99 {
		t.Fatalf("custom override must survive re-import: %+v", m)
	}

	all := r.AllModels()
	if len(all) != 6 { // 4 imported across providers + custom-ollama-qwen + custom local opus
		t.Errorf("expected 6 models after import, got %d", len(all))
	}
}

func TestImportModelsDevJSON_RejectsMalformed(t *testing.T) {
	r := NewRegistry()
	if _, err := r.ImportModelsDevJSON([]byte(`{"anthropic": 42}`)); err == nil {
		t.Error("malformed payload must return an error")
	}

	// Valid JSON that yields no models must not wipe the catalog.
	r.Upsert(ModelInfo{ID: "keep-me", Name: "Keep Me", IsCustom: true})
	n, err := r.ImportModelsDevJSON([]byte(`{}`))
	if err != nil || n != 0 {
		t.Fatalf("empty object: n=%d err=%v, want 0/nil", n, err)
	}
	if _, ok := r.Get("keep-me"); !ok {
		t.Error("empty payload must leave existing models untouched")
	}
}

func TestRegistry_UpsertAndAllModels(t *testing.T) {
	r := NewRegistry()

	r.Upsert(ModelInfo{
		ID:              "custom-ollama-qwen",
		Name:            "Custom Ollama Qwen",
		Provider:        "Local / Self-Hosted",
		InputPricePerM:  0.0,
		OutputPricePerM: 0.0,
		ContextWindow:   32000,
		IsCustom:        true,
	})

	m, ok := r.Get("custom-ollama-qwen")
	if !ok || !m.IsCustom {
		t.Fatalf("custom model not found or not marked custom: %+v", m)
	}
}

func TestRegistry_ProvidersAndCacheCalculations(t *testing.T) {
	r := NewDefaultRegistry()

	// 1. AllProviders and GetProvider
	providers := r.AllProviders()
	if len(providers) == 0 {
		t.Errorf("expected providers in default registry")
	}

	if p, ok := r.GetProvider("anthropic"); !ok || p.Name != "Anthropic" {
		t.Errorf("expected Anthropic provider: %+v", p)
	}

	// 2. CalculateCostDetailed and CalculateCostWithProvider
	cost, savings := r.CalculateCostDetailed("anthropic", "claude-3-7-sonnet", 1_000_000, 1_000_000, 500_000)
	if cost <= 0 || savings <= 0 {
		t.Errorf("expected positive cost and savings, got cost=%f savings=%f", cost, savings)
	}

	provCost := r.CalculateCostWithProvider("openai", "gpt-4o", 100_000, 50_000)
	if provCost <= 0 {
		t.Errorf("expected positive provider cost, got %f", provCost)
	}

	// 3. GetWithProvider
	if m, ok := r.GetWithProvider("google", "gemini-3-7-flash"); !ok || m.Name != "Gemini 3.7 Flash" {
		t.Errorf("expected Gemini 3.7 Flash: %+v", m)
	}
}

func TestRegistry_AliasCacheIsBounded(t *testing.T) {
	r := NewDefaultRegistry()
	for i := 0; i < maxAliasCacheEntries+500; i++ {
		id := fmt.Sprintf("claude-3-7-sonnet-snapshot-%d", i)
		if _, ok := r.Get(id); !ok {
			t.Fatalf("fuzzy alias %q did not resolve", id)
		}
	}

	r.mu.RLock()
	got := len(r.aliasCache)
	r.mu.RUnlock()
	if got > maxAliasCacheEntries {
		t.Fatalf("alias cache grew to %d entries, cap is %d", got, maxAliasCacheEntries)
	}
}

// TestGet_EmptyIdentityIsNotAWildcard pins the empty-identity fix: an entry
// whose ModelID or registry key normalized to "" (e.g. the shape
// POST /api/models/catalog accepts as {"id": "/"}) must never act as a
// universal prefix in Get's fuzzy fallback — it used to resolve for every
// unknown-model lookup and price them at its zero rates instead of the
// documented fallback.
func TestGet_EmptyIdentityIsNotAWildcard(t *testing.T) {
	r := NewDefaultRegistry()
	r.Upsert(ModelInfo{ID: "/", Name: "Ghost"})

	if m, ok := r.Get("totally-unknown-model"); ok {
		t.Fatalf("empty-identity entry %q (ModelID=%q) resolved for an unknown model", m.ID, m.ModelID)
	}

	// Documented fallback: 100k in * $2/1M + 50k out * $8/1M = $0.60.
	cost := r.CalculateCost("totally-unknown-model", 100_000, 50_000)
	if cost < 0.59 || cost > 0.61 {
		t.Errorf("unknown model must price at the documented $0.60 fallback, got %f", cost)
	}

	// The guards narrow fuzzy matching only: the literal key stays directly
	// addressable.
	if m, ok := r.Get("/"); !ok || m.ID != "/" {
		t.Fatalf("exact retrieval of the \"/\" key must keep working: ok=%v m=%+v", ok, m)
	}

	// Legitimate versioned snapshots must still fuzzy-match their canonical.
	if m, ok := r.Get("claude-3-7-sonnet-20250219"); !ok || m.ID != "anthropic/claude-3-7-sonnet" {
		got := "(none)"
		if ok {
			got = m.ID
		}
		t.Errorf("versioned snapshot recall broken, got %s", got)
	}
}

// TestRegistry_ProviderIndexStaysInSync pins the provider-index contract:
// Upsert registers the model's provider (name, count, listing), a model that
// moves providers leaves no stale index entry behind, and ImportModelsDevJSON
// preserves custom-only providers when the remote catalog replaces the maps.
// Space-only separators keep the slug intuitive: "Local Self Hosted" slugs to
// "local-self-hosted" (a "/" becomes one dash per separator, so "Local /
// Self-Hosted" would slug to "local---self-hosted").
func TestRegistry_ProviderIndexStaysInSync(t *testing.T) {
	r := NewRegistry()
	r.Upsert(ModelInfo{
		ID:            "custom-ollama-qwen",
		Name:          "Custom Ollama Qwen",
		Provider:      "Local Self Hosted",
		ContextWindow: 32000,
		IsCustom:      true,
	})

	p, ok := r.GetProvider("local-self-hosted")
	if !ok || p.Name != "Local Self Hosted" {
		t.Fatalf("Upsert must register the model's provider: ok=%v p=%+v", ok, p)
	}
	if p.ModelCount != 1 || len(p.Models) != 1 || p.Models[0].ID != "custom-ollama-qwen" {
		t.Fatalf("provider listing wrong: ModelCount=%d Models=%+v", p.ModelCount, p.Models)
	}

	// Catalog swap: 4 fixture models + 1 preserved custom = 5, and the
	// custom-only provider must survive the index rebuild.
	n, err := r.ImportModelsDevJSON([]byte(fixture))
	if err != nil || n != 5 {
		t.Fatalf("import: n=%d err=%v, want 5 (4 fixture models + 1 preserved custom)", n, err)
	}
	if p, ok := r.GetProvider("local-self-hosted"); !ok {
		t.Fatal("custom-only provider vanished from the index after ImportModelsDevJSON")
	} else if p.ModelCount != 1 || len(p.Models) != 1 || p.Models[0].ID != "custom-ollama-qwen" {
		t.Fatalf("custom provider listing wrong after import: ModelCount=%d Models=%+v", p.ModelCount, p.Models)
	}
	if _, ok := r.GetProvider("anthropic"); !ok {
		t.Error("remote provider missing after import")
	}

	// Move: re-upserting the model under a different provider must delete the
	// emptied entry and register the new one — not duplicate the listing.
	r.Upsert(ModelInfo{
		ID:       "custom-ollama-qwen",
		Name:     "Custom Ollama Qwen",
		Provider: "Other Host",
		IsCustom: true,
	})
	if _, ok := r.GetProvider("local-self-hosted"); ok {
		t.Fatal("stale provider entry survived after its only model moved away")
	}
	if p, ok := r.GetProvider("other-host"); !ok {
		t.Fatal("new provider not registered after the move")
	} else if p.ModelCount != 1 || len(p.Models) != 1 || p.Models[0].ID != "custom-ollama-qwen" {
		t.Fatalf("moved-model listing wrong: ModelCount=%d Models=%+v", p.ModelCount, p.Models)
	}

	// Update-in-place: re-upserting the same model under the same provider
	// must replace, not append.
	r.Upsert(ModelInfo{
		ID:              "custom-ollama-qwen",
		Name:            "Custom Ollama Qwen v2",
		Provider:        "Other Host",
		InputPricePerM:  0.5,
		OutputPricePerM: 1.5,
		IsCustom:        true,
	})
	if p, ok := r.GetProvider("other-host"); !ok {
		t.Fatal("provider lost after in-place update")
	} else if p.ModelCount != 1 || len(p.Models) != 1 || p.Models[0].Name != "Custom Ollama Qwen v2" {
		t.Fatalf("in-place update must replace the listing: ModelCount=%d Models=%+v", p.ModelCount, p.Models)
	}
}

// The three tests below pin the snapshot-ownership contract that
// TestRegistry_ProviderIndexStaysInSync could not see: it asserts the LIVE index,
// while the defect was in the value already handed to a caller. Registry stores
// providers by value (map[string]ProviderInfo), so copying the struct copies only
// the Models slice HEADER -- the returned ProviderInfo aliased registry storage,
// and Upsert writes into that array in place (registry.go:386 shift, :404
// overwrite, :410 append growth).
//
// Symptom 1: a value the caller already holds changes underneath it.
// Symptom 2: an in-place shift duplicates an entry in a stale snapshot.
// Symptom 3: reading a catalog concurrently with any Upsert is unsynchronised --
// reproduced as a DATA RACE at registry.go:404 and :410 under -race.
//
// All three fail without snapshotProvider's clone. Fresh Registry instances are
// used throughout, never models.Global: Upsert stores under the raw ID, so
// asserting growth against shared singleton state is rerun-unsafe.

func TestRegistry_ProviderSnapshotIsCallerOwned(t *testing.T) {
	r := NewRegistry()
	r.Upsert(ModelInfo{ID: "alpha/one", Provider: "Alpha", ProviderID: "alpha", InputPricePerM: 1, OutputPricePerM: 1})
	r.Upsert(ModelInfo{ID: "alpha/two", Provider: "Alpha", ProviderID: "alpha", InputPricePerM: 2, OutputPricePerM: 2})

	snap, ok := r.GetProvider("alpha")
	if !ok || len(snap.Models) != 2 {
		t.Fatalf("setup: GetProvider(alpha) ok=%v models=%+v", ok, snap.Models)
	}
	beforePrice := snap.Models[0].InputPricePerM

	// Update-in-place branch (registry.go:404).
	r.Upsert(ModelInfo{ID: "alpha/one", Provider: "Alpha", ProviderID: "alpha", InputPricePerM: 99, OutputPricePerM: 99})

	if got := snap.Models[0].InputPricePerM; got != beforePrice {
		t.Errorf("snapshot mutated under its caller: price %v -> %v; GetProvider returned live registry storage", beforePrice, got)
	}
	// Guard against "fixing" this by freezing the catalog: a FRESH read must see
	// the update, and the clone must not be served from a stale cache.
	live, ok := r.GetProvider("alpha")
	if !ok {
		t.Fatal("provider missing after update")
	}
	if live.ModelCount != 2 || len(live.Models) != 2 {
		t.Fatalf("live index wrong: ModelCount=%d len=%d", live.ModelCount, len(live.Models))
	}
	if live.Models[0].InputPricePerM != 99 {
		t.Errorf("fresh read did not observe the update: %v, want 99", live.Models[0].InputPricePerM)
	}
}

func TestRegistry_SnapshotSurvivesProviderMoveWithoutDuplicates(t *testing.T) {
	r := NewRegistry()
	r.Upsert(ModelInfo{ID: "alpha/one", Provider: "Alpha", ProviderID: "alpha"})
	r.Upsert(ModelInfo{ID: "alpha/two", Provider: "Alpha", ProviderID: "alpha"})

	snap, ok := r.GetProvider("alpha")
	if !ok || len(snap.Models) != 2 {
		t.Fatalf("setup: snapshot=%+v", snap.Models)
	}

	// Move index 0 away: the removal loop runs append(p.Models[:0], p.Models[1:]...)
	// on the SHARED array, copying "two" over slot 0.
	r.Upsert(ModelInfo{ID: "alpha/one", Provider: "Beta", ProviderID: "beta"})

	seen := map[string]int{}
	for _, m := range snap.Models {
		seen[m.ID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("stale snapshot lists %q %d times (ModelCount=%d agrees with the duplicate)", id, n, snap.ModelCount)
		}
	}
	if snap.ModelCount != len(snap.Models) {
		t.Errorf("snapshot self-inconsistent: ModelCount=%d len(Models)=%d", snap.ModelCount, len(snap.Models))
	}

	// And the live index must be right: alpha kept one, beta gained one.
	if a, ok := r.GetProvider("alpha"); !ok || a.ModelCount != 1 || a.Models[0].ID != "alpha/two" {
		t.Errorf("live alpha wrong after move: ok=%v %+v", ok, a.Models)
	}
	if b, ok := r.GetProvider("beta"); !ok || b.ModelCount != 1 || b.Models[0].ID != "alpha/one" {
		t.Errorf("live beta wrong after move: ok=%v %+v", ok, b.Models)
	}
}

func TestRegistry_AllProvidersSnapshotIsCallerOwned(t *testing.T) {
	// The production path: engine.go ProviderCatalog hands AllProviders() straight
	// to json.Marshal, which walks Models after the RLock is released.
	r := NewRegistry()
	r.Upsert(ModelInfo{ID: "alpha/one", Provider: "Alpha", ProviderID: "alpha", InputPricePerM: 1, OutputPricePerM: 1})
	r.Upsert(ModelInfo{ID: "alpha/two", Provider: "Alpha", ProviderID: "alpha", InputPricePerM: 2, OutputPricePerM: 2})

	out := r.AllProviders()
	var snap *ProviderInfo
	for i := range out {
		if out[i].ID == "alpha" {
			snap = &out[i]
		}
	}
	if snap == nil || len(snap.Models) != 2 {
		t.Fatalf("setup: no alpha provider in %+v", out)
	}
	beforePrice := snap.Models[0].InputPricePerM

	r.Upsert(ModelInfo{ID: "alpha/one", Provider: "Alpha", ProviderID: "alpha", InputPricePerM: 99, OutputPricePerM: 99})

	if got := snap.Models[0].InputPricePerM; got != beforePrice {
		t.Errorf("AllProviders snapshot mutated under its caller: %v -> %v", beforePrice, got)
	}
	for _, p := range r.AllProviders() {
		if p.ModelCount != len(p.Models) {
			t.Errorf("provider %q: ModelCount=%d but len(Models)=%d", p.ID, p.ModelCount, len(p.Models))
		}
	}
}

func TestRegistry_CatalogReadsDoNotRaceUpsert(t *testing.T) {
	// Permanent net for the DATA RACE that CI's -race gate never saw because no
	// test read the catalog concurrently with a write. Meaningless without -race,
	// harmless with it.
	r := NewRegistry()
	for i := 0; i < 40; i++ {
		r.Upsert(ModelInfo{ID: fmt.Sprintf("alpha/m%d", i), Provider: "Alpha", ProviderID: "alpha", InputPricePerM: float64(i)})
	}

	stop := make(chan struct{})
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			r.Upsert(ModelInfo{ID: "alpha/zzz", Provider: "Alpha", ProviderID: "alpha", InputPricePerM: float64(i)})
			r.Upsert(ModelInfo{ID: "alpha/zzz", Provider: "Beta", ProviderID: "beta"}) // forces a shift
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, p := range r.AllProviders() {
				for _, m := range p.Models {
					_ = m.ID
					_ = m.Provider
				}
			}
		}
	}()

	// Bounded so the test cannot hang on a fast/slow machine mismatch.
	deadline := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		close(deadline)
	}()
	<-deadline
	close(stop)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("goroutines did not exit after stop")
		}
	}
}

func TestSnapshotProviderKeepsNilModels(t *testing.T) {
	// The clone must not turn an absent listing into an empty one: callers
	// (and the JSON encoder) distinguish nil from [].
	if got := snapshotProvider(ProviderInfo{ID: "x"}); got.Models != nil {
		t.Errorf("nil Models became %#v", got.Models)
	}
	src := ProviderInfo{ID: "x", Models: []ModelInfo{{ID: "a"}}, ModelCount: 1}
	cp := snapshotProvider(src)
	cp.Models[0].ID = "mutated"
	if src.Models[0].ID != "a" {
		t.Error("snapshotProvider returned an aliased slice")
	}
}
