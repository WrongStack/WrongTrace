package proxy

import (
	"fmt"
	"sync"
	"time"
)

// QuotaLimiter tracks and enforces daily spending budgets per project or agent.
type QuotaLimiter struct {
	mu           sync.RWMutex
	dailyBudgets map[string]float64 // key -> daily limit in USD (0 = unlimited)
	dailySpend   map[string]float64 // key -> accumulated USD spend today
	lastResetDay int
}

// NewQuotaLimiter creates a new QuotaLimiter instance.
func NewQuotaLimiter() *QuotaLimiter {
	return &QuotaLimiter{
		dailyBudgets: make(map[string]float64),
		dailySpend:   make(map[string]float64),
		lastResetDay: time.Now().UTC().Day(),
	}
}

// SetBudget sets a daily spending limit in USD for a given key (project slug or agent name or "global").
func (q *QuotaLimiter) SetBudget(key string, limitUSD float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dailyBudgets[key] = limitUSD
}

// CheckAndRecordSpend verifies if adding the cost is within budget.
func (q *QuotaLimiter) CheckAndRecordSpend(key string, estimatedCostUSD float64) (allowed bool, remainingUSD float64, warning string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	currentDay := time.Now().UTC().Day()
	if currentDay != q.lastResetDay {
		q.dailySpend = make(map[string]float64)
		q.lastResetDay = currentDay
	}

	limit, hasLimit := q.dailyBudgets[key]
	if !hasLimit {
		// Also check global budget
		limit, hasLimit = q.dailyBudgets["global"]
	}

	if !hasLimit || limit <= 0 {
		// Unlimited
		q.dailySpend[key] += estimatedCostUSD
		return true, 999999.0, ""
	}

	currentSpend := q.dailySpend[key]
	if currentSpend+estimatedCostUSD > limit {
		return false, limit - currentSpend, fmt.Sprintf("WrongTrace Budget Guardrail: daily budget of $%.2f exceeded (current spend: $%.2f)", limit, currentSpend)
	}

	q.dailySpend[key] += estimatedCostUSD
	return true, limit - q.dailySpend[key], ""
}

// GetSpend returns today's spend for a given key.
func (q *QuotaLimiter) GetSpend(key string) (spend float64, limit float64) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.dailySpend[key], q.dailyBudgets[key]
}
