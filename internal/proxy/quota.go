package proxy

import (
	"fmt"
	"sync"
	"time"
)

// globalBudgetKey is the aggregate daily budget shared by every key.
const globalBudgetKey = "global"

// remainingUnbounded is the remainingUSD value reported when no daily budget
// applies at all. It is deliberately not a dollar-shaped number: a positive
// figure (the former 999999) is indistinguishable from a real balance, so a
// caller cannot tell "unlimited" from "almost out of money".
const remainingUnbounded = -1.0

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

// recordLocked adds cost to the key's own daily spend and to the global
// aggregate. Callers must hold q.mu. A key that IS the global budget is counted
// once, never twice.
func (q *QuotaLimiter) recordLocked(key string, costUSD float64) {
	q.dailySpend[key] += costUSD
	if key != globalBudgetKey {
		q.dailySpend[globalBudgetKey] += costUSD
	}
}

// bindingBudgetLocked returns the applicable budget with the LEAST headroom.
//
// Both the key's own budget and the global aggregate constrain a request,
// because every recorded spend is accumulated into the global total as well.
// Resolving only the key budget and falling back to global when absent let a
// key that configured its own (larger) budget spend straight through an already
// exhausted global cap.
//
// bounded is false when no positive budget is configured anywhere, which means
// unlimited. Callers must hold q.mu.
func (q *QuotaLimiter) bindingBudgetLocked(key string) (limit, spend float64, scope string, bounded bool) {
	for _, candidate := range []string{key, globalBudgetKey} {
		lim, has := q.dailyBudgets[candidate]
		if !has || lim <= 0 {
			continue // unset or documented "0 = unlimited"
		}
		sp := q.dailySpend[candidate]
		if !bounded || lim-sp < limit-spend {
			limit, spend, scope, bounded = lim, sp, candidate, true
		}
	}
	return limit, spend, scope, bounded
}

// budgetWarning names the budget that actually bound the request, so an operator
// can tell a project overrunning its own cap from an org-wide cap running dry.
func budgetWarning(scope string, limit, spend float64) string {
	if scope != globalBudgetKey {
		scope = "project"
	}
	return fmt.Sprintf("WrongTrace Budget Guardrail: %s daily budget of $%.2f exceeded (current spend: $%.2f)", scope, limit, spend)
}

// CheckSpend verifies if adding the cost is within budget without mutating state.
// remainingUSD is the headroom left on the BINDING budget -- the tighter of this
// key's own budget and the global aggregate. When no budget applies, remainingUSD
// is reported as remainingUnbounded rather than a fabricated balance.
func (q *QuotaLimiter) CheckSpend(key string, estimatedCostUSD float64) (allowed bool, remainingUSD float64, warning string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.checkResetDayLocked()

	limit, spend, scope, bounded := q.bindingBudgetLocked(key)
	if !bounded {
		return true, remainingUnbounded, ""
	}
	if spend+estimatedCostUSD > limit {
		return false, limit - spend, budgetWarning(scope, limit, spend)
	}

	return true, limit - spend, ""
}

// CheckAndRecordSpend verifies if adding the cost is within budget and records it
// immediately. It enforces the same binding budget as CheckSpend and charges both
// the key and the global aggregate. A denied request records nothing.
func (q *QuotaLimiter) CheckAndRecordSpend(key string, estimatedCostUSD float64) (allowed bool, remainingUSD float64, warning string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.checkResetDayLocked()

	limit, spend, scope, bounded := q.bindingBudgetLocked(key)
	if !bounded {
		// Unlimited, but keep metering so a later SetBudget sees real history.
		q.recordLocked(key, estimatedCostUSD)
		return true, remainingUnbounded, ""
	}
	if spend+estimatedCostUSD > limit {
		return false, limit - spend, budgetWarning(scope, limit, spend)
	}

	q.recordLocked(key, estimatedCostUSD)
	return true, limit - spend - estimatedCostUSD, ""
}

// RecordSpend records actual completed spend after an LLM request completes.
func (q *QuotaLimiter) RecordSpend(key string, costUSD float64) {
	if costUSD <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.checkResetDayLocked()
	q.recordLocked(key, costUSD)
}

// GetSpend returns today's spend for a given key.
func (q *QuotaLimiter) GetSpend(key string) (spend float64, limit float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.checkResetDayLocked()
	return q.dailySpend[key], q.dailyBudgets[key]
}
