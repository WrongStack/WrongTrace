package models

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ModelInfo captures metadata and token pricing for an LLM model under a specific provider.
type ModelInfo struct {
	ID                 string  `json:"id"`                     // Unique key e.g. "anthropic/claude-3-7-sonnet" or "openrouter/deepseek-r1"
	ModelID            string  `json:"model_id"`               // Bare model name e.g. "claude-3-7-sonnet"
	Name               string  `json:"name"`                   // Display name e.g. "Claude 3.7 Sonnet"
	Provider           string  `json:"provider"`               // Provider name e.g. "Anthropic", "OpenRouter", "DeepInfra"
	ProviderID         string  `json:"provider_id"`            // Provider slug e.g. "anthropic", "openrouter", "deepinfra"
	ProviderAPI        string  `json:"provider_api,omitempty"` // Base API endpoint e.g. "https://api.anthropic.com/v1"
	NpmPackage         string  `json:"npm_package,omitempty"`  // Provider adapter slug e.g. "@ai-sdk/anthropic"
	InputPricePerM     float64 `json:"input_price_per_m"`      // USD per 1M prompt tokens
	OutputPricePerM    float64 `json:"output_price_per_m"`     // USD per 1M completion tokens
	CacheReadPricePerM float64 `json:"cache_read_price_per_m"` // USD per 1M cached prompt tokens
	ContextWindow      int     `json:"context_window"`         // max context tokens
	Description        string  `json:"description"`
	IsCustom           bool    `json:"is_custom"`
	IsCanonical        bool    `json:"is_canonical"`
}

// ProviderInfo captures an AI Provider and its complete list of supported models.
type ProviderInfo struct {
	ID         string      `json:"id"`            // Slug e.g. "anthropic", "openai", "groq"
	Name       string      `json:"name"`          // Display Name e.g. "Anthropic", "OpenAI"
	API        string      `json:"api,omitempty"` // Upstream base API endpoint
	NPM        string      `json:"npm,omitempty"` // SDK package adapter e.g. "@ai-sdk/anthropic"
	Doc        string      `json:"doc,omitempty"` // Documentation link
	ModelCount int         `json:"model_count"`   // Total models available
	Models     []ModelInfo `json:"models"`        // List of models provided
}

// Registry maintains an up-to-date catalog of multi-provider LLM pricing and specifications.
type Registry struct {
	mu         sync.RWMutex
	models     map[string]ModelInfo    // keyed by full provider/model ID or custom ID
	providers  map[string]ProviderInfo // keyed by provider slug
	canonicals map[string]string       // bare model ID -> canonical full ID
}

// Global default registry singleton with built-in provider defaults.
var Global = NewDefaultRegistry()

// NewRegistry initializes an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		models:     make(map[string]ModelInfo),
		providers:  make(map[string]ProviderInfo),
		canonicals: make(map[string]string),
	}
}

// NewDefaultRegistry initializes a Registry populated with built-in coding model defaults.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	r.seedDefaults()
	return r
}

func (r *Registry) seedDefaults() {
	defaults := []ModelInfo{
		// Anthropic
		{ID: "anthropic/claude-3-7-sonnet", ModelID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", Provider: "Anthropic", ProviderID: "anthropic", InputPricePerM: 3.00, OutputPricePerM: 15.00, CacheReadPricePerM: 0.30, ContextWindow: 200000, IsCanonical: true},
		{ID: "anthropic/claude-3-5-sonnet", ModelID: "claude-3-5-sonnet", Name: "Claude 3.5 Sonnet", Provider: "Anthropic", ProviderID: "anthropic", InputPricePerM: 3.00, OutputPricePerM: 15.00, CacheReadPricePerM: 0.30, ContextWindow: 200000, IsCanonical: true},
		{ID: "anthropic/claude-3-5-haiku", ModelID: "claude-3-5-haiku", Name: "Claude 3.5 Haiku", Provider: "Anthropic", ProviderID: "anthropic", InputPricePerM: 0.80, OutputPricePerM: 4.00, CacheReadPricePerM: 0.08, ContextWindow: 200000, IsCanonical: true},

		// OpenAI
		{ID: "openai/gpt-4o", ModelID: "gpt-4o", Name: "GPT-4o", Provider: "OpenAI", ProviderID: "openai", InputPricePerM: 2.50, OutputPricePerM: 10.00, CacheReadPricePerM: 1.25, ContextWindow: 128000, IsCanonical: true},
		{ID: "openai/gpt-4o-mini", ModelID: "gpt-4o-mini", Name: "GPT-4o Mini", Provider: "OpenAI", ProviderID: "openai", InputPricePerM: 0.15, OutputPricePerM: 0.60, CacheReadPricePerM: 0.075, ContextWindow: 128000, IsCanonical: true},
		{ID: "openai/o3-mini", ModelID: "o3-mini", Name: "o3-mini", Provider: "OpenAI", ProviderID: "openai", InputPricePerM: 1.10, OutputPricePerM: 4.40, CacheReadPricePerM: 0.55, ContextWindow: 200000, IsCanonical: true},
		{ID: "openai/o1", ModelID: "o1", Name: "o1", Provider: "OpenAI", ProviderID: "openai", InputPricePerM: 15.00, OutputPricePerM: 60.00, CacheReadPricePerM: 7.50, ContextWindow: 200000, IsCanonical: true},
		{ID: "openai/gpt-4.5-preview", ModelID: "gpt-4-5-preview", Name: "GPT-4.5 Preview", Provider: "OpenAI", ProviderID: "openai", InputPricePerM: 75.00, OutputPricePerM: 150.00, CacheReadPricePerM: 37.50, ContextWindow: 128000, IsCanonical: true},

		// Google
		{ID: "google/gemini-3.7-flash", ModelID: "gemini-3-7-flash", Name: "Gemini 3.7 Flash", Provider: "Google", ProviderID: "google", InputPricePerM: 0.075, OutputPricePerM: 0.30, CacheReadPricePerM: 0.01875, ContextWindow: 1000000, IsCanonical: true},
		{ID: "google/gemini-2.5-pro", ModelID: "gemini-2-5-pro", Name: "Gemini 2.5 Pro", Provider: "Google", ProviderID: "google", InputPricePerM: 1.25, OutputPricePerM: 5.00, CacheReadPricePerM: 0.3125, ContextWindow: 2000000, IsCanonical: true},
		{ID: "google/gemini-2.0-flash", ModelID: "gemini-2-0-flash", Name: "Gemini 2.0 Flash", Provider: "Google", ProviderID: "google", InputPricePerM: 0.10, OutputPricePerM: 0.40, CacheReadPricePerM: 0.025, ContextWindow: 1000000, IsCanonical: true},

		// DeepSeek
		{ID: "deepseek/deepseek-r1", ModelID: "deepseek-r1", Name: "DeepSeek R1", Provider: "DeepSeek", ProviderID: "deepseek", InputPricePerM: 0.55, OutputPricePerM: 2.19, CacheReadPricePerM: 0.14, ContextWindow: 64000, IsCanonical: true},
		{ID: "deepseek/deepseek-v3", ModelID: "deepseek-v3", Name: "DeepSeek V3", Provider: "DeepSeek", ProviderID: "deepseek", InputPricePerM: 0.14, OutputPricePerM: 0.28, CacheReadPricePerM: 0.014, ContextWindow: 64000, IsCanonical: true},
		{ID: "deepseek/deepseek-coder", ModelID: "deepseek-coder", Name: "DeepSeek Coder", Provider: "DeepSeek", ProviderID: "deepseek", InputPricePerM: 0.14, OutputPricePerM: 0.28, CacheReadPricePerM: 0.014, ContextWindow: 64000, IsCanonical: true},

		// Qwen / Alibaba
		{ID: "qwen/qwen-2.5-coder-32b", ModelID: "qwen-2-5-coder-32b", Name: "Qwen 2.5 Coder 32B", Provider: "Alibaba Cloud", ProviderID: "alibaba", InputPricePerM: 0.20, OutputPricePerM: 0.60, CacheReadPricePerM: 0.05, ContextWindow: 128000, IsCanonical: true},

		// MiniMax
		{ID: "minimax/minimax-text-01", ModelID: "minimax-text-01", Name: "MiniMax Text-01", Provider: "MiniMax", ProviderID: "minimax", InputPricePerM: 0.20, OutputPricePerM: 1.10, CacheReadPricePerM: 0.02, ContextWindow: 1000000, IsCanonical: true},
		{ID: "minimax/minimax-01", ModelID: "minimax-01", Name: "MiniMax-01", Provider: "MiniMax", ProviderID: "minimax", InputPricePerM: 0.20, OutputPricePerM: 1.10, CacheReadPricePerM: 0.02, ContextWindow: 1000000, IsCanonical: false},
		{ID: "minimax/abab6.5s", ModelID: "abab6-5s", Name: "MiniMax abab 6.5s", Provider: "MiniMax", ProviderID: "minimax", InputPricePerM: 0.14, OutputPricePerM: 0.28, CacheReadPricePerM: 0.02, ContextWindow: 245000, IsCanonical: true},
		{ID: "minimax/abab7-chat-preview", ModelID: "abab7-chat-preview", Name: "MiniMax abab 7 Preview", Provider: "MiniMax", ProviderID: "minimax", InputPricePerM: 0.20, OutputPricePerM: 1.10, CacheReadPricePerM: 0.02, ContextWindow: 1000000, IsCanonical: false},

		// Kimi / Moonshot AI
		{ID: "moonshot/kimi-k2", ModelID: "kimi-k2", Name: "Kimi K2 Coding", Provider: "Moonshot", ProviderID: "moonshot", InputPricePerM: 0.30, OutputPricePerM: 1.20, CacheReadPricePerM: 0.075, ContextWindow: 200000, IsCanonical: true},
		{ID: "moonshot/moonshot-v1-128k", ModelID: "moonshot-v1-128k", Name: "Moonshot v1 128k", Provider: "Moonshot", ProviderID: "moonshot", InputPricePerM: 0.84, OutputPricePerM: 0.84, CacheReadPricePerM: 0.21, ContextWindow: 128000, IsCanonical: true},

		// ZCode / Z.ai / GLM
		{ID: "zcode/zcode-1", ModelID: "zcode-1", Name: "ZCode Pro", Provider: "Z.ai", ProviderID: "zcode", InputPricePerM: 0.50, OutputPricePerM: 2.00, CacheReadPricePerM: 0.125, ContextWindow: 128000, IsCanonical: true},
		{ID: "zhipu/glm-4-plus", ModelID: "glm-4-plus", Name: "GLM-4 Plus", Provider: "Zhipu AI", ProviderID: "zhipu", InputPricePerM: 1.40, OutputPricePerM: 1.40, CacheReadPricePerM: 0.35, ContextWindow: 128000, IsCanonical: true},
		{ID: "zhipu/glm-4-flash", ModelID: "glm-4-flash", Name: "GLM-4 Flash", Provider: "Zhipu AI", ProviderID: "zhipu", InputPricePerM: 0.01, OutputPricePerM: 0.01, CacheReadPricePerM: 0.002, ContextWindow: 128000, IsCanonical: true},
	}

	for _, m := range defaults {
		r.models[m.ID] = m
		r.canonicals[m.ModelID] = m.ID

		p := r.providers[m.ProviderID]
		p.ID = m.ProviderID
		p.Name = m.Provider
		p.ModelCount++
		p.Models = append(p.Models, m)
		r.providers[m.ProviderID] = p
	}
}

// AllProviders returns all providers sorted by model count descending then name.
func (r *Registry) AllProviders() []ProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ProviderInfo, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ModelCount != out[j].ModelCount {
			return out[i].ModelCount > out[j].ModelCount
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// GetProvider retrieves a provider by ID slug or name.
func (r *Registry) GetProvider(id string) (ProviderInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	slug := sanitizeSlug(id)
	if p, ok := r.providers[slug]; ok {
		return p, true
	}
	for _, p := range r.providers {
		if strings.EqualFold(p.Name, id) {
			return p, true
		}
	}
	return ProviderInfo{}, false
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

// Get finds a model by ID, full provider/model key, or bare model alias.
func (r *Registry) Get(id string) (ModelInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	raw := strings.ToLower(strings.TrimSpace(id))
	if m, ok := r.models[raw]; ok {
		return m, true
	}

	// 1. If input contains a provider prefix (e.g. "ai-router/claude-opus-4.7")
	if strings.Contains(raw, "/") {
		parts := strings.SplitN(raw, "/", 2)
		provID := sanitizeSlug(parts[0])
		mID := normalizeModelID(parts[1])
		fullKey := provID + "/" + mID
		if m, ok := r.models[fullKey]; ok {
			return m, true
		}
	}

	// 2. Direct exact normalized lookup
	norm := normalizeModelID(id)
	if m, ok := r.models[norm]; ok {
		return m, true
	}

	// 3. Check canonical map for bare model ID (e.g. "claude-opus-4-7" -> "anthropic/claude-opus-4-7")
	if canonKey, ok := r.canonicals[norm]; ok {
		if m, ok := r.models[canonKey]; ok {
			return m, true
		}
	}

	// 4. Try prefix match for versioned snapshots (e.g. claude-3-7-sonnet-20250219).
	// Map iteration order is random, so collect candidates and pick the most
	// specific match deterministically (longest matching key, lexical
	// tie-break) — otherwise the same query could resolve to different
	// providers' prices on consecutive calls.
	type candidate struct {
		key   string
		model ModelInfo
	}
	var candidates []candidate
	for k, model := range r.models {
		if strings.HasPrefix(norm, model.ModelID) || (model.ModelID != "" && strings.Contains(norm, model.ModelID)) {
			candidates = append(candidates, candidate{k, model})
			continue
		}
		if strings.HasPrefix(norm, k) || strings.Contains(norm, k) {
			candidates = append(candidates, candidate{k, model})
		}
	}
	if len(candidates) > 0 {
		sort.Slice(candidates, func(i, j int) bool {
			ci, cj := candidates[i], candidates[j]
			li, lj := len(ci.model.ModelID), len(cj.model.ModelID)
			if li != lj {
				return li > lj // longest (most specific) model id first
			}
			if len(ci.key) != len(cj.key) {
				return len(ci.key) > len(cj.key)
			}
			return ci.key < cj.key // stable lexical tie-break
		})
		// Prefer the canonical first-party entry when multiple providers
		// share the top specificity.
		for _, c := range candidates {
			if c.model.IsCanonical {
				return c.model, true
			}
		}
		return candidates[0].model, true
	}

	return ModelInfo{}, false
}

// GetWithProvider finds the model matching the specific provider and model ID.
func (r *Registry) GetWithProvider(provider, modelID string) (ModelInfo, bool) {
	if provider == "" {
		return r.Get(modelID)
	}

	provSlug := sanitizeSlug(provider)
	normModel := normalizeModelID(modelID)
	fullKey := provSlug + "/" + normModel

	// Provider-scoped lookups happen under one RLock critical section; the
	// general-Get fallback is called AFTER the lock is released. Unlocking
	// manually here while a deferred RUnlock is pending caused a double-unlock
	// panic on every provider miss.
	var result ModelInfo
	found := func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()

		if m, ok := r.models[fullKey]; ok {
			result = m
			return true
		}

		// Match by provider name if slug didn't match directly
		for _, m := range r.models {
			if (strings.EqualFold(m.Provider, provider) || strings.EqualFold(m.ProviderID, provSlug)) &&
				(m.ModelID == normModel || strings.EqualFold(m.Name, modelID)) {
				result = m
				return true
			}
		}
		return false
	}()

	if found {
		return result, true
	}
	// Fallback to general lookup (acquires its own lock)
	return r.Get(modelID)
}

// Upsert adds or updates a model in the catalog.
func (r *Registry) Upsert(m ModelInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()

	normModel := normalizeModelID(m.ID)
	if m.ModelID == "" {
		m.ModelID = normModel
	}
	if m.Provider == "" {
		m.Provider = "Custom"
	}
	if m.ProviderID == "" {
		m.ProviderID = sanitizeSlug(m.Provider)
	}

	key := normModel
	if m.ID != "" {
		key = m.ID
	}
	m.ID = key

	r.models[key] = m
	r.canonicals[normModel] = key
}

// CalculateCost computes total dollar cost from prompt and completion token counts.
func (r *Registry) CalculateCost(modelID string, promptTokens, completionTokens int64) float64 {
	return r.CalculateCostWithProvider("", modelID, promptTokens, completionTokens)
}

// CalculateCostWithProvider computes dollar cost respecting the exact provider's pricing.
func (r *Registry) CalculateCostWithProvider(provider, modelID string, promptTokens, completionTokens int64) float64 {
	cost, _ := r.CalculateCostDetailed(provider, modelID, promptTokens, completionTokens, 0)
	return cost
}

// CalculateCostDetailed computes exact cost taking cached prompt tokens into account and returns total cost and estimated cache savings.
func (r *Registry) CalculateCostDetailed(provider, modelID string, promptTokens, completionTokens, cachedTokens int64) (costUSD float64, cacheSavingsUSD float64) {
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > promptTokens {
		cachedTokens = promptTokens
	}
	if promptTokens == 0 && completionTokens == 0 {
		return 0.0, 0.0
	}

	model, ok := r.GetWithProvider(provider, modelID)
	if !ok {
		// Default fallback estimate ($2/1M in, $8/1M out, $0.50/1M cached)
		nonCached := promptTokens - cachedTokens
		inCost := (float64(nonCached) * 2.0 / 1e6) + (float64(cachedTokens) * 0.5 / 1e6)
		outCost := (float64(completionTokens) * 8.0 / 1e6)
		savings := (float64(cachedTokens) * 1.5 / 1e6)
		return inCost + outCost, savings
	}

	nonCached := promptTokens - cachedTokens
	cacheReadRate := model.CacheReadPricePerM
	if cacheReadRate <= 0 && model.InputPricePerM > 0 {
		cacheReadRate = model.InputPricePerM * 0.25 // standard 75% prompt cache discount if not specified
	}

	inCost := (float64(nonCached) * model.InputPricePerM / 1e6) + (float64(cachedTokens) * cacheReadRate / 1e6)
	outCost := (float64(completionTokens) * model.OutputPricePerM) / 1e6
	savings := (float64(cachedTokens) * (model.InputPricePerM - cacheReadRate)) / 1e6
	if savings < 0 {
		savings = 0
	}
	return inCost + outCost, savings
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

func sanitizeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			sb.WriteRune(r)
		} else if r == ' ' || r == '/' || r == '\\' || r == '.' {
			sb.WriteRune('-')
		}
	}
	return strings.Trim(sb.String(), "-")
}

// modelsDevModel mirrors a single model entry nested under a provider in models.dev/api.json.
type modelsDevModel struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Cost        struct {
		Input     float64 `json:"input"`
		Output    float64 `json:"output"`
		CacheRead float64 `json:"cache_read"`
	} `json:"cost"`
	Limit struct {
		Context int `json:"context"`
	} `json:"limit"`
}

// modelsDevProvider mirrors a provider entry in models.dev/api.json.
type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	API    string                    `json:"api"`
	NPM    string                    `json:"npm"`
	Doc    string                    `json:"doc"`
	Models map[string]modelsDevModel `json:"models"`
}

// IsJunkModel reports whether a model name is an artifact of schema typing, code variables, or placeholders.
func IsJunkModel(s string) bool {
	raw := strings.TrimSpace(strings.ToLower(s))
	if raw == "" || len(raw) < 2 || len(raw) > 80 {
		return true
	}
	switch raw {
	case "unknown-model", "unknown_model", "unknown-model-detected", "unknown-provider",
		"omit", "inherit", "none", "null", "undefined", "default", "custom", "agent", "model",
		"string", "boolean", "number", "integer", "object", "array", "any", "void", "function",
		"this.meta.model", "this.model", "self.model", "meta.model", "process.env.model",
		"t.model", "row.model", "m.model", "item.model", "data.model", "obj.model", "props.model",
		"state.model", "req.model", "res.model", "msg.model", "record.model", "entry.model",
		"livemodel", "mock", "mock-model", "test", "dummy", "placeholder", "n/a":
		return true
	}
	if strings.Contains(raw, "this.") || strings.Contains(raw, "self.") || strings.Contains(raw, "process.env") ||
		strings.Contains(raw, "{") || strings.Contains(raw, "}") || strings.Contains(raw, "(") || strings.Contains(raw, ")") ||
		strings.Contains(raw, "=") || strings.Contains(raw, ";") || strings.Contains(raw, "$") || strings.Contains(raw, "<") ||
		strings.Contains(raw, ">") || strings.Contains(raw, "[") || strings.Contains(raw, "]") || strings.HasPrefix(raw, ".") ||
		strings.HasSuffix(raw, ".model") || strings.HasSuffix(raw, ".ts") || strings.HasSuffix(raw, ".tsx") ||
		strings.HasSuffix(raw, ".js") || strings.HasSuffix(raw, ".go") || strings.HasSuffix(raw, ".py") ||
		strings.HasSuffix(raw, ".json") || strings.HasSuffix(raw, ".md") {
		return true
	}

	// Reject property expressions like "t.model", "a.b" unless it's a numeric version like "gpt-4.5" or "claude-3.5"
	if strings.Contains(raw, ".") {
		parts := strings.Split(raw, ".")
		if len(parts) == 2 {
			// If neither part is numeric or part of a recognized version prefix, it's code property access
			if parts[1] == "model" || parts[1] == "name" || parts[1] == "id" || parts[1] == "type" || parts[1] == "value" {
				return true
			}
			if len(parts[0]) <= 2 && !strings.ContainsAny(parts[0], "0123456789") {
				return true
			}
		}
	}

	return false
}

// ImportModelsDevJSON parses the provider→models catalog published at https://models.dev/api.json
// and populates all providers and their respective model pricings into the registry.
func (r *Registry) ImportModelsDevJSON(data []byte) (int, error) {
	var providers map[string]modelsDevProvider
	if err := json.Unmarshal(data, &providers); err != nil {
		return 0, fmt.Errorf("parse models.dev api.json: %w", err)
	}

	providerKeys := make([]string, 0, len(providers))
	for k := range providers {
		providerKeys = append(providerKeys, k)
	}
	sort.Strings(providerKeys)

	nextModels := make(map[string]ModelInfo)
	nextProviders := make(map[string]ProviderInfo)
	nextCanonicals := make(map[string]string)

	for _, pk := range providerKeys {
		p := providers[pk]
		providerName := p.Name
		if providerName == "" {
			providerName = pk
		}
		provSlug := sanitizeSlug(pk)
		provAPI := p.API
		provNPM := p.NPM

		// Sort model keys deterministically
		modelKeys := make([]string, 0, len(p.Models))
		for mk := range p.Models {
			modelKeys = append(modelKeys, mk)
		}
		sort.Strings(modelKeys)

		provModels := make([]ModelInfo, 0, len(modelKeys))

		for _, mk := range modelKeys {
			if IsJunkModel(mk) {
				continue
			}
			m := p.Models[mk]
			normModel := normalizeModelID(mk)
			if normModel == "" || IsJunkModel(normModel) {
				continue
			}
			// Skip junk entries carrying no cost and no context window
			if m.Cost.Input == 0 && m.Cost.Output == 0 && m.Limit.Context == 0 {
				continue
			}
			name := m.Name
			if name == "" {
				name = mk
			}

			// First-party / canonical owner check
			ns, _, _ := strings.Cut(mk, "/")
			isCanonical := (ns == pk)

			fullKey := provSlug + "/" + normModel
			entry := ModelInfo{
				ID:                 fullKey,
				ModelID:            normModel,
				Name:               name,
				Provider:           providerName,
				ProviderID:         provSlug,
				ProviderAPI:        provAPI,
				NpmPackage:         provNPM,
				InputPricePerM:     m.Cost.Input,
				OutputPricePerM:    m.Cost.Output,
				CacheReadPricePerM: m.Cost.CacheRead,
				ContextWindow:      m.Limit.Context,
				Description:        m.Description,
				IsCanonical:        isCanonical,
			}

			nextModels[fullKey] = entry
			provModels = append(provModels, entry)

			// Set canonical lookup mapping
			if isCanonical {
				nextCanonicals[normModel] = fullKey
			} else if _, exists := nextCanonicals[normModel]; !exists {
				nextCanonicals[normModel] = fullKey
			}
		}

		if len(provModels) > 0 {
			nextProviders[provSlug] = ProviderInfo{
				ID:         provSlug,
				Name:       providerName,
				API:        provAPI,
				NPM:        provNPM,
				Doc:        p.Doc,
				ModelCount: len(provModels),
				Models:     provModels,
			}
		}
	}

	if len(nextModels) == 0 {
		return 0, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Preserve custom models
	for k, v := range r.models {
		if v.IsCustom {
			nextModels[k] = v
			if v.ModelID != "" {
				nextCanonicals[v.ModelID] = k
			}
		}
	}

	r.models = nextModels
	r.providers = nextProviders
	r.canonicals = nextCanonicals
	return len(nextModels), nil
}
