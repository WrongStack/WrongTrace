package proxy

import (
	"fmt"
	"sync"
	"time"
)

// QuotaLimiter tracks and enforces daily spending budgets per project or agent.
type QuotaLimiter struct {
	mu            sync.RWMutex
	dailyBudgets  map[string]float64 // key -> daily limit in USD (0 = unlimited)
	dailySpend    map[string]float64 // key -> accumulated USD spend today
	lastResetDate string
}

// NewQuotaLimiter creates a new QuotaLimiter instance.
func NewQuotaLimiter() *QuotaLimiter {
	return &QuotaLimiter{
		dailyBudgets:  make(map[string]float64),
		dailySpend:    make(map[string]float64),
		lastResetDate: time.Now().UTC().Format("2006-01-02"),
	}
}

// SetBudget sets a daily spending limit in USD for a given key (project slug or agent name or "global").
func (q *QuotaLimiter) SetBudget(key string, limitUSD float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dailyBudgets[key] = limitUSD
}

// checkResetDayLocked checks if the day has rolled over in UTC and resets daily spend if so.
func (q *QuotaLimiter) checkResetDayLocked() {
	today := time.Now().UTC().Format("2006-01-02")
	if today != q.lastResetDate {
		q.dailySpend = make(map[string]float64)
		q.lastResetDate = today
	}
}

// CheckSpend verifies if adding the cost is within budget without mutating state.
func (q *QuotaLimiter) CheckSpend(key string, estimatedCostUSD float64) (allowed bool, remainingUSD float64, warning string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.checkResetDayLocked()

	limit, hasLimit := q.dailyBudgets[key]
	isGlobal := false
	if !hasLimit {
		// Also check global budget
		limit, hasLimit = q.dailyBudgets["global"]
		isGlobal = true
	}

	if !hasLimit || limit <= 0 {
		return true, 999999.0, ""
	}

	currentSpend := q.dailySpend[key]
	if isGlobal {
		currentSpend = q.dailySpend["global"]
	}
	if currentSpend+estimatedCostUSD > limit {
		return false, limit - currentSpend, fmt.Sprintf("WrongTrace Budget Guardrail: daily budget of $%.2f exceeded (current spend: $%.2f)", limit, currentSpend)
	}

	return true, limit - currentSpend, ""
}

// CheckAndRecordSpend verifies if adding the cost is within budget and records it immediately.
func (q *QuotaLimiter) CheckAndRecordSpend(key string, estimatedCostUSD float64) (allowed bool, remainingUSD float64, warning string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.checkResetDayLocked()

	limit, hasLimit := q.dailyBudgets[key]
	isGlobal := false
	if !hasLimit {
		// Also check global budget
		limit, hasLimit = q.dailyBudgets["global"]
		isGlobal = true
	}

	if !hasLimit || limit <= 0 {
		// Unlimited
		q.dailySpend[key] += estimatedCostUSD
		if key != "global" {
			q.dailySpend["global"] += estimatedCostUSD
		}
		return true, 999999.0, ""
	}

	currentSpend := q.dailySpend[key]
	if isGlobal {
		currentSpend = q.dailySpend["global"]
	}
	if currentSpend+estimatedCostUSD > limit {
		return false, limit - currentSpend, fmt.Sprintf("WrongTrace Budget Guardrail: daily budget of $%.2f exceeded (current spend: $%.2f)", limit, currentSpend)
	}

	q.dailySpend[key] += estimatedCostUSD
	if key != "global" {
		q.dailySpend["global"] += estimatedCostUSD
	}
	return true, limit - currentSpend - estimatedCostUSD, ""
}

// RecordSpend records actual completed spend after an LLM request completes.
func (q *QuotaLimiter) RecordSpend(key string, costUSD float64) {
	if costUSD <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.checkResetDayLocked()
	q.dailySpend[key] += costUSD
	if key != "global" {
		q.dailySpend["global"] += costUSD
	}
}

// GetSpend returns today's spend for a given key.
func (q *QuotaLimiter) GetSpend(key string) (spend float64, limit float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.checkResetDayLocked()
	return q.dailySpend[key], q.dailyBudgets[key]
}
