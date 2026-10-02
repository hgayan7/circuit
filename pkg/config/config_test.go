package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/himshikhargayan/si-shield/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePolicy_ValidYAML(t *testing.T) {
	yamlContent := `
version: "v1alpha1"
name: "test-policy"
description: "A test policy"
default_action: ALLOW

rules:
  - id: "block-drop-table"
    description: "Block DROP TABLE statements"
    match:
      tool: "postgres.query"
    condition: "args.sql.matches('(?i)DROP TABLE')"
    action: DENY
    reason: "DROP TABLE is prohibited"

  - id: "stripe-large-refund"
    description: "Require approval for large refunds"
    match:
      endpoint: "POST /v1/refunds"
    condition: "args.amount > 10000"
    action: REQUIRE_APPROVAL
    reason: "Large refund requires sign-off"
    escalation:
      channel: "slack"
      target: "#finance-approvals"

  - id: "pr-rate-limit"
    match:
      tool: "github.create_pull_request"
    budget:
      window: "1h"
      max_calls: 5
      max_amount: 0
    action: DENY
    reason: "Exceeded max PR calls per hour"
`

	policy, err := config.ParsePolicy(strings.NewReader(yamlContent))
	require.NoError(t, err)
	require.NotNil(t, policy)

	assert.Equal(t, "v1alpha1", policy.Version)
	assert.Equal(t, "test-policy", policy.Name)
	assert.Equal(t, config.ActionAllow, policy.DefaultAction)
	assert.Len(t, policy.Rules, 3)

	rule1 := policy.Rules[0]
	assert.Equal(t, "block-drop-table", rule1.ID)
	assert.Equal(t, "postgres.query", rule1.Match.Tool)
	assert.Equal(t, "args.sql.matches('(?i)DROP TABLE')", rule1.Condition)
	assert.Equal(t, config.ActionDeny, rule1.Action)
	assert.Equal(t, "DROP TABLE is prohibited", rule1.Reason)

	rule2 := policy.Rules[1]
	assert.Equal(t, "stripe-large-refund", rule2.ID)
	assert.Equal(t, "POST /v1/refunds", rule2.Match.Endpoint)
	assert.Equal(t, config.ActionRequireApproval, rule2.Action)
	require.NotNil(t, rule2.Escalation)
	assert.Equal(t, "slack", rule2.Escalation.Channel)
	assert.Equal(t, "#finance-approvals", rule2.Escalation.Target)

	rule3 := policy.Rules[2]
	assert.Equal(t, "pr-rate-limit", rule3.ID)
	require.NotNil(t, rule3.Budget)
	assert.Equal(t, time.Hour, rule3.Budget.WindowDuration)
	assert.Equal(t, int64(5), rule3.Budget.MaxCalls)
}

func TestParsePolicy_DefaultValues(t *testing.T) {
	yamlContent := `
name: "minimal-policy"
rules:
  - id: "rule-1"
    match:
      tool: "some-tool"
    action: ALLOW
`
	policy, err := config.ParsePolicy(strings.NewReader(yamlContent))
	require.NoError(t, err)
	assert.Equal(t, config.DefaultVersion, policy.Version)
	assert.Equal(t, config.ActionAllow, policy.DefaultAction)
}

func TestParsePolicy_InvalidAction(t *testing.T) {
	yamlContent := `
name: "bad-action"
rules:
  - id: "rule-1"
    match:
      tool: "some-tool"
    action: INVALID_ACTION
`
	_, err := config.ParsePolicy(strings.NewReader(yamlContent))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid action")
}

func TestParsePolicy_MissingRuleID(t *testing.T) {
	yamlContent := `
name: "missing-id"
rules:
  - match:
      tool: "some-tool"
    action: ALLOW
`
	_, err := config.ParsePolicy(strings.NewReader(yamlContent))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rule id is required")
}

func TestParsePolicy_InvalidWindowDuration(t *testing.T) {
	yamlContent := `
name: "bad-window"
rules:
  - id: "rule-1"
    match:
      tool: "some-tool"
    budget:
      window: "invalid-duration"
    action: DENY
`
	_, err := config.ParsePolicy(strings.NewReader(yamlContent))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid budget window")
}
