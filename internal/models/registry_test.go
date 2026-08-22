package models

import (
	"testing"
)

func TestRegistry_GetAndNormalize(t *testing.T) {
	r := NewRegistry()

	// Direct lookup
	m, ok := r.Get("claude-3-7-sonnet")
	if !ok || m.Provider != "Anthropic" {
		t.Fatalf("failed to get claude-3-7-sonnet: %+v", m)
	}

	// Alias with provider prefix and dots
	m, ok = r.Get("anthropic/claude-3.7-sonnet")
	if !ok || m.ID != "claude-3-7-sonnet" {
		t.Fatalf("failed to normalize anthropic/claude-3.7-sonnet: %+v", m)
	}

	// Versioned date suffix prefix match
	m, ok = r.Get("claude-3-7-sonnet-20250219")
	if !ok || m.ID != "claude-3-7-sonnet" {
		t.Fatalf("failed prefix match on versioned date: %+v", m)
	}

	// Unknown model
	_, ok = r.Get("non-existent-model-xyz")
	if ok {
		t.Fatal("expected false for unknown model")
	}
}

func TestRegistry_CalculateCost(t *testing.T) {
	r := NewRegistry()

	// 1M prompt tokens and 1M completion tokens on Claude 3.7 Sonnet ($3 + $15 = $18)
	cost := r.CalculateCost("claude-3-7-sonnet", 1_000_000, 1_000_000)
	if cost < 17.99 || cost > 18.01 {
		t.Errorf("expected $18.00, got %f", cost)
	}

	// 100k prompt, 10k completion on DeepSeek R1 ($0.55/M in, $2.19/M out)
	// (100k * 0.55 / 1e6) + (10k * 2.19 / 1e6) = 0.055 + 0.0219 = 0.0769
	costR1 := r.CalculateCost("deepseek-r1", 100_000, 10_000)
	if costR1 < 0.076 || costR1 > 0.078 {
		t.Errorf("expected ~$0.0769 for DeepSeek R1, got %f", costR1)
	}

	// Zero tokens
	if zero := r.CalculateCost("gpt-4o", 0, 0); zero != 0.0 {
		t.Errorf("expected 0.0 cost for 0 tokens, got %f", zero)
	}

	// Fallback calculation on unknown model
	fallback := r.CalculateCost("my-custom-unregistered-model", 1_000_000, 1_000_000)
	if fallback < 9.99 || fallback > 10.01 {
		t.Errorf("expected $10.00 fallback, got %f", fallback)
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

	all := r.AllModels()
	if len(all) < len(defaultCatalog)+1 {
		t.Errorf("expected at least %d models, got %d", len(defaultCatalog)+1, len(all))
	}
}
