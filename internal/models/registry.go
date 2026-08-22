package models

import (
	"sort"
	"strings"
	"sync"
)

// ModelInfo captures metadata and token pricing for an LLM model.
type ModelInfo struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Provider           string  `json:"provider"`
	InputPricePerM     float64 `json:"input_price_per_m"`     // USD per 1M prompt tokens
	OutputPricePerM    float64 `json:"output_price_per_m"`    // USD per 1M completion tokens
	CacheReadPricePerM float64 `json:"cache_read_price_per_m"`// USD per 1M cached prompt tokens
	ContextWindow      int     `json:"context_window"`        // max context tokens
	Description        string  `json:"description"`
	IsCustom           bool    `json:"is_custom"`
}

// Registry maintains an up-to-date catalog of LLM pricing and specifications.
type Registry struct {
	mu     sync.RWMutex
	models map[string]ModelInfo
}

// DefaultCatalog contains up-to-date pricing for major AI coding models.
var defaultCatalog = []ModelInfo{
	// --- Anthropic ---
	{
		ID:                 "claude-3-7-sonnet",
		Name:               "Claude 3.7 Sonnet (Hybrid Reasoning)",
		Provider:           "Anthropic",
		InputPricePerM:     3.00,
		OutputPricePerM:    15.00,
		CacheReadPricePerM: 0.30,
		ContextWindow:      200000,
		Description:        "State-of-the-art hybrid reasoning & coding model by Anthropic.",
	},
	{
		ID:                 "claude-3-5-sonnet",
		Name:               "Claude 3.5 Sonnet",
		Provider:           "Anthropic",
		InputPricePerM:     3.00,
		OutputPricePerM:    15.00,
		CacheReadPricePerM: 0.30,
		ContextWindow:      200000,
		Description:        "Industry standard coding model with strong agentic performance.",
	},
	{
		ID:                 "claude-3-5-haiku",
		Name:               "Claude 3.5 Haiku",
		Provider:           "Anthropic",
		InputPricePerM:     0.80,
		OutputPricePerM:    4.00,
		CacheReadPricePerM: 0.08,
		ContextWindow:      200000,
		Description:        "High-speed, cost-efficient model for subagents and quick edits.",
	},
	{
		ID:                 "claude-3-opus",
		Name:               "Claude 3 Opus",
		Provider:           "Anthropic",
		InputPricePerM:     15.00,
		OutputPricePerM:    75.00,
		CacheReadPricePerM: 1.50,
		ContextWindow:      200000,
		Description:        "Heavy-duty reasoning model for complex architectural analysis.",
	},

	// --- OpenAI ---
	{
		ID:                 "gpt-4.5",
		Name:               "GPT-4.5 Preview",
		Provider:           "OpenAI",
		InputPricePerM:     75.00,
		OutputPricePerM:    150.00,
		CacheReadPricePerM: 37.50,
		ContextWindow:      128000,
		Description:        "OpenAI's largest world knowledge and coding model.",
	},
	{
		ID:                 "gpt-4o",
		Name:               "GPT-4o",
		Provider:           "OpenAI",
		InputPricePerM:     2.50,
		OutputPricePerM:    10.00,
		CacheReadPricePerM: 1.25,
		ContextWindow:      128000,
		Description:        "Flagship multimodal omni model with fast inference.",
	},
	{
		ID:                 "gpt-4o-mini",
		Name:               "GPT-4o mini",
		Provider:           "OpenAI",
		InputPricePerM:     0.15,
		OutputPricePerM:    0.60,
		CacheReadPricePerM: 0.075,
		ContextWindow:      128000,
		Description:        "Ultra-low cost high-speed model for lightweight routines.",
	},
	{
		ID:                 "o1",
		Name:               "o1 (Reasoning)",
		Provider:           "OpenAI",
		InputPricePerM:     15.00,
		OutputPricePerM:    60.00,
		CacheReadPricePerM: 7.50,
		ContextWindow:      200000,
		Description:        "Chain-of-thought reasoning model for hard algorithm design.",
	},
	{
		ID:                 "o3-mini",
		Name:               "o3-mini (Fast Reasoning)",
		Provider:           "OpenAI",
		InputPricePerM:     1.10,
		OutputPricePerM:    4.40,
		CacheReadPricePerM: 0.55,
		ContextWindow:      200000,
		Description:        "Fast, cost-effective STEM and coding reasoning model.",
	},

	// --- Google ---
	{
		ID:                 "gemini-2.5-pro",
		Name:               "Gemini 2.5 Pro",
		Provider:           "Google",
		InputPricePerM:     1.25,
		OutputPricePerM:    5.00,
		CacheReadPricePerM: 0.31,
		ContextWindow:      2000000,
		Description:        "Google's flagship 2M token context coding & reasoning model.",
	},
	{
		ID:                 "gemini-2.5-flash",
		Name:               "Gemini 2.5 Flash",
		Provider:           "Google",
		InputPricePerM:     0.075,
		OutputPricePerM:    0.30,
		CacheReadPricePerM: 0.02,
		ContextWindow:      1000000,
		Description:        "High-throughput 1M context model with sub-second latency.",
	},
	{
		ID:                 "gemini-2.0-flash-thinking",
		Name:               "Gemini 2.0 Flash Thinking",
		Provider:           "Google",
		InputPricePerM:     0.10,
		OutputPricePerM:    0.40,
		CacheReadPricePerM: 0.025,
		ContextWindow:      1000000,
		Description:        "Experimental reasoning model by Google DeepMind.",
	},

	// --- DeepSeek ---
	{
		ID:                 "deepseek-r1",
		Name:               "DeepSeek R1",
		Provider:           "DeepSeek",
		InputPricePerM:     0.55,
		OutputPricePerM:    2.19,
		CacheReadPricePerM: 0.14,
		ContextWindow:      64000,
		Description:        "Open-weights reasoning model with high coding capability.",
	},
	{
		ID:                 "deepseek-v3",
		Name:               "DeepSeek V3",
		Provider:           "DeepSeek",
		InputPricePerM:     0.27,
		OutputPricePerM:    1.10,
		CacheReadPricePerM: 0.07,
		ContextWindow:      64000,
		Description:        "High-performance MoE model for fast code generation.",
	},

	// --- Meta / Mistral / Qwen ---
	{
		ID:                 "llama-3.3-70b",
		Name:               "Llama 3.3 70B Instruct",
		Provider:           "Meta",
		InputPricePerM:     0.70,
		OutputPricePerM:    0.80,
		CacheReadPricePerM: 0.10,
		ContextWindow:      128000,
		Description:        "Meta's top open-weights instruction-tuned model.",
	},
	{
		ID:                 "qwen-2.5-coder-32b",
		Name:               "Qwen 2.5 Coder 32B",
		Provider:           "Alibaba / Qwen",
		InputPricePerM:     0.20,
		OutputPricePerM:    0.20,
		CacheReadPricePerM: 0.05,
		ContextWindow:      128000,
		Description:        "Specialized open-weights programming model.",
	},
	{
		ID:                 "codestral",
		Name:               "Mistral Codestral 2501",
		Provider:           "Mistral AI",
		InputPricePerM:     0.30,
		OutputPricePerM:    0.90,
		CacheReadPricePerM: 0.10,
		ContextWindow:      256000,
		Description:        "Mistral's purpose-built code generation engine.",
	},
}

// Global default registry singleton.
var Global = NewRegistry()

// NewRegistry initializes a Registry preloaded with the default model catalog.
func NewRegistry() *Registry {
	r := &Registry{
		models: make(map[string]ModelInfo, len(defaultCatalog)),
	}
	for _, m := range defaultCatalog {
		r.models[normalizeModelID(m.ID)] = m
	}
	return r
}

// AllModels returns a slice of all models sorted by provider then name.
func (r *Registry) AllModels() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ModelInfo, 0, len(r.models))
	for _, m := range r.models {
		out = append(out, m)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Get finds a model by ID or alias.
func (r *Registry) Get(id string) (ModelInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	norm := normalizeModelID(id)
	m, ok := r.models[norm]
	if ok {
		return m, true
	}

	// Try prefix match for versioned snapshots (e.g. claude-3-7-sonnet-20250219)
	for k, model := range r.models {
		if strings.HasPrefix(norm, k) || strings.Contains(norm, k) {
			return model, true
		}
	}
	return ModelInfo{}, false
}

// Upsert adds or updates a model in the catalog.
func (r *Registry) Upsert(m ModelInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()

	norm := normalizeModelID(m.ID)
	m.ID = norm
	r.models[norm] = m
}

// CalculateCost computes total dollar cost from prompt and completion token counts.
func (r *Registry) CalculateCost(modelID string, promptTokens, completionTokens int64) float64 {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	if promptTokens == 0 && completionTokens == 0 {
		return 0.0
	}

	model, ok := r.Get(modelID)
	if !ok {
		// Default fallback estimate ($2/1M in, $8/1M out)
		return (float64(promptTokens) * 2.0 / 1e6) + (float64(completionTokens) * 8.0 / 1e6)
	}

	inCost := (float64(promptTokens) * model.InputPricePerM) / 1e6
	outCost := (float64(completionTokens) * model.OutputPricePerM) / 1e6
	return inCost + outCost
}

func normalizeModelID(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	// Strip provider prefixes if given (e.g. "anthropic/claude-3.7-sonnet" -> "claude-3.7-sonnet")
	if idx := strings.LastIndex(s, "/"); idx != -1 {
		s = s[idx+1:]
	}
	s = strings.ReplaceAll(s, ".", "-")
	return s
}
