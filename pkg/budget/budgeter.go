package budget

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/himshikhargayan/si-shield/pkg/config"
)

// BudgetResult contains the status and current usage metrics of a budget evaluation.
type BudgetResult struct {
	Allowed       bool          `json:"allowed"`
	CurrentCalls  int64         `json:"current_calls"`
	MaxCalls      int64         `json:"max_calls,omitempty"`
	CurrentAmount float64       `json:"current_amount"`
	MaxAmount     float64       `json:"max_amount,omitempty"`
	Window        time.Duration `json:"window"`
	Reason        string        `json:"reason,omitempty"`
}

// Budgeter defines the contract for recording and verifying cumulative action budgets.
type Budgeter interface {
	RecordAndCheck(ctx context.Context, key string, cfg *config.BudgetConfig, amount float64) (*BudgetResult, error)
	Reset(key string)
}

type actionRecord struct {
	timestamp time.Time
	amount    float64
}

type bucket struct {
	mu      sync.Mutex
	records []actionRecord
}

// MemoryBudgeter is a thread-safe in-memory sliding window budget tracker.
type MemoryBudgeter struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
}

// NewMemoryBudgeter creates an initialized in-memory budget tracker.
func NewMemoryBudgeter() *MemoryBudgeter {
	return &MemoryBudgeter{
		buckets: make(map[string]*bucket),
	}
}

func (m *MemoryBudgeter) getBucket(key string) *bucket {
	m.mu.RLock()
	b, exists := m.buckets[key]
	m.mu.RUnlock()
	if exists {
		return b
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Double-check after acquiring write lock
	if b, exists = m.buckets[key]; exists {
		return b
	}
	b = &bucket{
		records: make([]actionRecord, 0, 16),
	}
	m.buckets[key] = b
	return b
}

// RecordAndCheck calculates usage within the sliding window, tests limits, and records on success.
func (m *MemoryBudgeter) RecordAndCheck(ctx context.Context, key string, cfg *config.BudgetConfig, amount float64) (*BudgetResult, error) {
	if cfg == nil {
		return &BudgetResult{Allowed: true}, nil
	}

	windowDuration := cfg.WindowDuration
	if windowDuration <= 0 && cfg.Window != "" {
		var err error
		windowDuration, err = time.ParseDuration(cfg.Window)
		if err != nil {
			return nil, fmt.Errorf("invalid window duration: %w", err)
		}
	}

	b := m.getBucket(key)
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-windowDuration)

	// Prune records older than cutoff
	validIndex := 0
	var sumAmount float64
	for i, rec := range b.records {
		if rec.timestamp.After(cutoff) {
			validIndex = i
			break
		}
		if i == len(b.records)-1 {
			// All records expired
			validIndex = len(b.records)
		}
	}
	b.records = b.records[validIndex:]

	for _, rec := range b.records {
		sumAmount += rec.amount
	}
	currentCalls := int64(len(b.records))

	res := &BudgetResult{
		Allowed:       true,
		CurrentCalls:  currentCalls,
		MaxCalls:      cfg.MaxCalls,
		CurrentAmount: sumAmount,
		MaxAmount:     cfg.MaxAmount,
		Window:        windowDuration,
	}

	// 1. Check calls count limit
	if cfg.MaxCalls > 0 && currentCalls+1 > cfg.MaxCalls {
		res.Allowed = false
		res.Reason = fmt.Sprintf("action call limit exceeded: %d/%d calls within window %s", currentCalls, cfg.MaxCalls, windowDuration)
		return res, nil
	}

	// 2. Check cumulative financial/quantity limit
	if cfg.MaxAmount > 0 && sumAmount+amount > cfg.MaxAmount {
		res.Allowed = false
		res.Reason = fmt.Sprintf("amount budget exceeded: %.2f + %.2f > %.2f within window %s", sumAmount, amount, cfg.MaxAmount, windowDuration)
		return res, nil
	}

	// Record the new action since it passed all checks
	b.records = append(b.records, actionRecord{
		timestamp: now,
		amount:    amount,
	})
	res.CurrentCalls++
	res.CurrentAmount += amount

	return res, nil
}

// Reset clears recorded actions for a specific key.
func (m *MemoryBudgeter) Reset(key string) {
	m.mu.Lock()
	delete(m.buckets, key)
	m.mu.Unlock()
}
