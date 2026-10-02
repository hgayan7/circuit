package policy_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/himshikhargayan/si-shield/pkg/config"
	"github.com/himshikhargayan/si-shield/pkg/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPolicy(t *testing.T, yamlStr string) *config.Policy {
	p, err := config.ParsePolicy(strings.NewReader(yamlStr))
	require.NoError(t, err)
	return p
}

func TestEngine_Evaluate_DenyDestructiveSQL(t *testing.T) {
	yamlPolicy := `
name: "postgres-protection"
default_action: ALLOW
rules:
  - id: "block-ddl"
    description: "Prevent destructive SQL on prod"
    match:
      tool: "postgres.query"
    condition: "args.sql.matches('(?i)(DROP|TRUNCATE|ALTER)')"
    action: DENY
    reason: "Destructive DDL is forbidden"
`
	pol := newTestPolicy(t, yamlPolicy)
	engine, err := policy.NewEngine(pol)
	require.NoError(t, err)

	ctx := context.Background()

	// Safe query -> ALLOW
	evalSafe, err := engine.Evaluate(ctx, &policy.EvaluationContext{
		Tool: "postgres.query",
		Args: map[string]any{
			"sql": "SELECT * FROM users WHERE id = 1",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, config.ActionAllow, evalSafe.Action)

	// Destructive query -> DENY
	evalDestructive, err := engine.Evaluate(ctx, &policy.EvaluationContext{
		Tool: "postgres.query",
		Args: map[string]any{
			"sql": "DROP TABLE users;",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, config.ActionDeny, evalDestructive.Action)
	assert.Equal(t, "block-ddl", evalDestructive.RuleID)
	assert.Equal(t, "Destructive DDL is forbidden", evalDestructive.Reason)
}

func TestEngine_Evaluate_RequireApprovalThreshold(t *testing.T) {
	yamlPolicy := `
name: "payment-protection"
default_action: ALLOW
rules:
  - id: "large-refund-escalation"
    match:
      endpoint: "POST /v1/refunds"
    condition: "args.amount > 10000"
    action: REQUIRE_APPROVAL
    reason: "Refund exceeds $100"
    escalation:
      channel: "slack"
      target: "#finance-approvals"
`
	pol := newTestPolicy(t, yamlPolicy)
	engine, err := policy.NewEngine(pol)
	require.NoError(t, err)

	ctx := context.Background()

	// Under threshold -> ALLOW
	resUnder, err := engine.Evaluate(ctx, &policy.EvaluationContext{
		Endpoint: "POST /v1/refunds",
		Args: map[string]any{
			"amount": 5000,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, config.ActionAllow, resUnder.Action)

	// Over threshold -> REQUIRE_APPROVAL
	resOver, err := engine.Evaluate(ctx, &policy.EvaluationContext{
		Endpoint: "POST /v1/refunds",
		Args: map[string]any{
			"amount": 25000,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, config.ActionRequireApproval, resOver.Action)
	assert.Equal(t, "large-refund-escalation", resOver.RuleID)
	require.NotNil(t, resOver.Escalation)
	assert.Equal(t, "slack", resOver.Escalation.Channel)
	assert.Equal(t, "#finance-approvals", resOver.Escalation.Target)
}

func TestEngine_Evaluate_WildcardToolMatch(t *testing.T) {
	yamlPolicy := `
name: "wildcard-policy"
default_action: ALLOW
rules:
  - id: "block-all-prod-db"
    match:
      tool: "db.*"
    condition: "args.environment == 'production'"
    action: DENY
    reason: "Direct agent access to production DB is blocked"
`
	pol := newTestPolicy(t, yamlPolicy)
	engine, err := policy.NewEngine(pol)
	require.NoError(t, err)

	ctx := context.Background()

	resMatch, err := engine.Evaluate(ctx, &policy.EvaluationContext{
		Tool: "db.delete_row",
		Args: map[string]any{
			"environment": "production",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, config.ActionDeny, resMatch.Action)

	resNoMatch, err := engine.Evaluate(ctx, &policy.EvaluationContext{
		Tool: "db.delete_row",
		Args: map[string]any{
			"environment": "staging",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, config.ActionAllow, resNoMatch.Action)
}

func TestEngine_InvalidCELCondition(t *testing.T) {
	yamlPolicy := `
name: "bad-cel"
rules:
  - id: "syntax-error"
    match:
      tool: "some.tool"
    condition: "args.invalid syntax here ==="
    action: DENY
`
	pol := newTestPolicy(t, yamlPolicy)
	_, err := policy.NewEngine(pol)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to compile condition")
}

func BenchmarkEngine_Evaluate(b *testing.B) {
	yamlPolicy := `
name: "bench-policy"
default_action: ALLOW
rules:
  - id: "rule-1"
    match:
      tool: "postgres.query"
    condition: "args.sql.matches('(?i)(DROP|TRUNCATE|ALTER)')"
    action: DENY
`
	p, _ := config.ParsePolicy(strings.NewReader(yamlPolicy))
	engine, _ := policy.NewEngine(p)
	ctx := context.Background()
	evalCtx := &policy.EvaluationContext{
		Tool: "postgres.query",
		Args: map[string]any{
			"sql": "SELECT id, name FROM users WHERE email = 'test@example.com'",
		},
		Timestamp: time.Now(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = engine.Evaluate(ctx, evalCtx)
	}
}
