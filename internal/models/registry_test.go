package models

import (
	"fmt"
	"strings"
	"testing"
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
