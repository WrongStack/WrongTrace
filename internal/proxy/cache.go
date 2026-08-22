package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// CachedResponse stores a cached LLM response for identical prompt requests.
type CachedResponse struct {
	Key          string            `json:"key"`
	StatusCode   int               `json:"status_code"`
	Headers      map[string]string `json:"headers"`
	Body         []byte            `json:"body"`
	IsStream     bool              `json:"is_stream"`
	Model        string            `json:"model"`
	Provider     string            `json:"provider"`
	TokensSaved  int64             `json:"tokens_saved"`
	CostSavedUSD float64           `json:"cost_saved_usd"`
	CreatedAt    time.Time         `json:"created_at"`
	ExpiresAt    time.Time         `json:"expires_at"`
	HitCount     int               `json:"hit_count"`
}

// ResponseCache manages an in-memory LRU cache for LLM completions.
type ResponseCache struct {
	mu          sync.RWMutex
	items       map[string]*CachedResponse
	maxEntries  int
	defaultTTL  time.Duration
	totalHits   int64
	totalMisses int64
	totalSaved  float64
}

// NewResponseCache creates a new in-memory response cache.
func NewResponseCache(maxEntries int, defaultTTL time.Duration) *ResponseCache {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	if defaultTTL <= 0 {
		defaultTTL = 24 * time.Hour
	}
	return &ResponseCache{
		items:      make(map[string]*CachedResponse, maxEntries),
		maxEntries: maxEntries,
		defaultTTL: defaultTTL,
	}
}

// ComputeKey calculates a deterministic SHA-256 hash of the provider, model, and sanitized request body.
func ComputeKey(provider, model string, body []byte) string {
	hasher := sha256.New()
	hasher.Write([]byte(provider))
	hasher.Write([]byte(":"))
	hasher.Write([]byte(model))
	hasher.Write([]byte(":"))
	hasher.Write(body)
	return hex.EncodeToString(hasher.Sum(nil))
}

// Get retrieves a valid, non-expired cached response.
func (c *ResponseCache) Get(key string) (*CachedResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, found := c.items[key]
	if !found {
		c.totalMisses++
		return nil, false
	}

	if time.Now().After(item.ExpiresAt) {
		delete(c.items, key)
		c.totalMisses++
		return nil, false
	}

	item.HitCount++
	c.totalHits++
	c.totalSaved += item.CostSavedUSD
	return item, true
}

// Set saves a response in the cache.
func (c *ResponseCache) Set(key, provider, model string, statusCode int, headers map[string]string, body []byte, isStream bool, tokensSaved int64, costSavedUSD float64, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ttl <= 0 {
		ttl = c.defaultTTL
	}

	if len(c.items) >= c.maxEntries {
		// Evict oldest item
		var oldestKey string
		var oldestTime time.Time
		for k, v := range c.items {
			if oldestTime.IsZero() || v.CreatedAt.Before(oldestTime) {
				oldestTime = v.CreatedAt
				oldestKey = k
			}
		}
		if oldestKey != "" {
			delete(c.items, oldestKey)
		}
	}

	now := time.Now()
	c.items[key] = &CachedResponse{
		Key:          key,
		StatusCode:   statusCode,
		Headers:      headers,
		Body:         body,
		IsStream:     isStream,
		Model:        model,
		Provider:     provider,
		TokensSaved:  tokensSaved,
		CostSavedUSD: costSavedUSD,
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
	}
}

// Clear flushes all cached entries.
func (c *ResponseCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*CachedResponse, c.maxEntries)
}

// Stats returns cache efficiency metrics.
func (c *ResponseCache) Stats() (entries int, hits int64, misses int64, hitRate float64, totalSavedUSD float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.totalHits + c.totalMisses
	var rate float64
	if total > 0 {
		rate = (float64(c.totalHits) / float64(total)) * 100.0
	}
	return len(c.items), c.totalHits, c.totalMisses, rate, c.totalSaved
}
