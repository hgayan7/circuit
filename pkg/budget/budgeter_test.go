package budget_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hgayan7/circuit/pkg/budget"
	"github.com/hgayan7/circuit/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBudgeter_MaxCalls_RollingWindow(t *testing.T) {
	b := budget.NewMemoryBudgeter()
	ctx := context.Background()

	cfg := &config.BudgetConfig{
		Window:         "1h",
		WindowDuration: time.Hour,
		MaxCalls:       3,
	}

	key := "agent-1:github.create_pr"

	// 1st call -> Allowed
	res, err := b.RecordAndCheck(ctx, key, cfg, 0)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, int64(1), res.CurrentCalls)

	// 2nd call -> Allowed
	res, err = b.RecordAndCheck(ctx, key, cfg, 0)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, int64(2), res.CurrentCalls)

	// 3rd call -> Allowed
	res, err = b.RecordAndCheck(ctx, key, cfg, 0)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, int64(3), res.CurrentCalls)

	// 4th call -> Exceeded!
	res, err = b.RecordAndCheck(ctx, key, cfg, 0)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Contains(t, res.Reason, "call limit exceeded")
}

func TestBudgeter_MaxAmount_CumulativeSpend(t *testing.T) {
	b := budget.NewMemoryBudgeter()
	ctx := context.Background()

	cfg := &config.BudgetConfig{
		Window:         "1h",
		WindowDuration: time.Hour,
		MaxAmount:      500.0,
	}

	key := "agent-2:stripe.refunds"

	// $200 -> Allowed
	res, err := b.RecordAndCheck(ctx, key, cfg, 200.0)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 200.0, res.CurrentAmount)

	// $250 -> Allowed (Total $450)
	res, err = b.RecordAndCheck(ctx, key, cfg, 250.0)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 450.0, res.CurrentAmount)

	// $100 -> Blocked (would make $550 > $500)
	res, err = b.RecordAndCheck(ctx, key, cfg, 100.0)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Contains(t, res.Reason, "amount budget exceeded")
	// The rejected amount must NOT be recorded into the cumulative budget
	assert.Equal(t, 450.0, res.CurrentAmount)
}

func TestBudgeter_SlidingWindowExpiry(t *testing.T) {
	b := budget.NewMemoryBudgeter()
	ctx := context.Background()

	cfg := &config.BudgetConfig{
		Window:         "50ms",
		WindowDuration: 50 * time.Millisecond,
		MaxCalls:       1,
	}

	key := "agent-3:temp-action"

	// 1st call -> Allowed
	res, err := b.RecordAndCheck(ctx, key, cfg, 0)
	require.NoError(t, err)
	assert.True(t, res.Allowed)

	// Immediate 2nd call -> Blocked
	res, err = b.RecordAndCheck(ctx, key, cfg, 0)
	require.NoError(t, err)
	assert.False(t, res.Allowed)

	// Wait for window to expire
	time.Sleep(70 * time.Millisecond)

	// 3rd call -> Allowed again because old event rolled out
	res, err = b.RecordAndCheck(ctx, key, cfg, 0)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
}

func TestBudgeter_ConcurrentAccess(t *testing.T) {
	b := budget.NewMemoryBudgeter()
	ctx := context.Background()

	cfg := &config.BudgetConfig{
		Window:         "1m",
		WindowDuration: time.Minute,
		MaxCalls:       100,
	}

	key := "agent-concurrent:tool"
	var wg sync.WaitGroup
	workers := 20
	callsPerWorker := 5

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < callsPerWorker; j++ {
				res, err := b.RecordAndCheck(ctx, key, cfg, 0)
				assert.NoError(t, err)
				assert.NotNil(t, res)
			}
		}()
	}

	wg.Wait()

	// 101st call should exceed
	res, err := b.RecordAndCheck(ctx, key, cfg, 0)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
}
