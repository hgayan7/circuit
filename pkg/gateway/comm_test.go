package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommExecutor(t *testing.T) {
	ctx := context.Background()
	target, err := NewCommTarget("slack-dev", "chat", []string{"#general", "#alerts"}, []string{"acme.org"}, 5, true)
	require.NoError(t, err)
	exec := NewCommExecutor(target)

	// 1. Send message to allowed channel
	msgRes := exec.Execute(ctx, Request{
		Operation: "send_message",
		Channel:   "slack-dev",
		Args: map[string]any{
			"channel": "#general",
			"message": "Deployment completed successfully.",
		},
	})
	assert.Equal(t, 200, msgRes.Status)
	var msgData map[string]any
	require.NoError(t, json.Unmarshal(msgRes.Body, &msgData))
	assert.Equal(t, "#general", msgData["channel"])
	assert.Equal(t, "sent", msgData["status"])

	// 2. Reject message to unallowed channel
	unallowedRes := exec.Execute(ctx, Request{
		Operation: "send_message",
		Channel:   "slack-dev",
		Args: map[string]any{
			"channel": "#random-unapproved",
			"message": "Hello world",
		},
	})
	assert.Equal(t, 403, unallowedRes.Status)
	assert.Contains(t, unallowedRes.Error, "not in the allowed channels list")

	// 3. Send email to internal recipients
	emailRes := exec.Execute(ctx, Request{
		Operation: "send_email",
		Channel:   "slack-dev",
		Args: map[string]any{
			"to":      "alice@acme.org, bob@acme.org",
			"subject": "System Status",
			"body":    "All services operating normally.",
		},
	})
	assert.Equal(t, 200, emailRes.Status)
	var emailData map[string]any
	require.NoError(t, json.Unmarshal(emailRes.Body, &emailData))
	assert.Equal(t, float64(2), emailData["recipients_count"])

	// 4. Create and update ticket
	ticketRes := exec.Execute(ctx, Request{
		Operation: "create_ticket",
		Channel:   "slack-dev",
		Args: map[string]any{
			"project":     "OPS",
			"title":       "Database migration verification",
			"description": "Verify indexes after migration",
		},
	})
	assert.Equal(t, 200, ticketRes.Status)
	var ticketData map[string]any
	require.NoError(t, json.Unmarshal(ticketRes.Body, &ticketData))
	assert.Equal(t, "OPS-1", ticketData["key"])

	updateRes := exec.Execute(ctx, Request{
		Operation: "update_ticket",
		Channel:   "slack-dev",
		Args: map[string]any{
			"key":     "OPS-1",
			"status":  "resolved",
			"comment": "Verified and all checks passed.",
		},
	})
	assert.Equal(t, 200, updateRes.Status)

	// 5. Publish document
	docRes := exec.Execute(ctx, Request{
		Operation: "publish_document",
		Channel:   "slack-dev",
		Args: map[string]any{
			"title":   "Incident Report 2026-10",
			"content": "Root cause analysis...",
		},
	})
	assert.Equal(t, 200, docRes.Status)
}

func TestCommGatewayApprovalAndLimits(t *testing.T) {
	ctx := context.Background()
	cfgYAML := `
name: comm-gateway
admin_token_env: ADMIN_TOKEN
approval_ttl: 15m
communications:
  - id: corporate-comm
    kind: all
    allowed_channels: ["#engineering", "#announcements"]
    internal_domains: ["mycompany.com"]
    require_approval_for_external: true
agents:
  - id: bot
    token_env: BOT_TOKEN
    channels: ["corporate-comm"]
    actions:
      - send_message
      - send_email
      - create_ticket
      - update_ticket
      - publish_document
limits:
  - id: message-rate
    actions: ["send_message"]
    scope: channel
    window: 1m
    max_calls: 2
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	target, err := NewCommTarget("corporate-comm", "all", []string{"#engineering", "#announcements"}, []string{"mycompany.com"}, 10, true)
	require.NoError(t, err)
	router := NewRouterExecutor(nil, nil, nil, nil, map[string]*CommExecutor{
		"corporate-comm": NewCommExecutor(target),
	}, nil, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)

	// 1. Regular chat message succeeds automatically
	act1, err := svc.Submit(ctx, "bot", "msg-1", Request{
		Operation: "send_message",
		Channel:   "corporate-comm",
		Args: map[string]any{
			"channel": "#engineering",
			"message": "Routine build completed.",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act1.State)

	// 2. Chat message with broadcast mention (@channel) requires operator approval
	act2, err := svc.Submit(ctx, "bot", "msg-broadcast", Request{
		Operation: "send_message",
		Channel:   "corporate-comm",
		Args: map[string]any{
			"channel": "#engineering",
			"message": "Attention @channel: scheduled maintenance tonight.",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", act2.State)
	assert.Contains(t, act2.Reason, "Broadcast channel mentions")

	// Operator approves broadcast message
	approvedMsg, err := svc.Decide(ctx, act2.ID, act2.Digest, "approve")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", approvedMsg.State)

	// 3. Email to external domain requires operator approval
	act3, err := svc.Submit(ctx, "bot", "email-ext", Request{
		Operation: "send_email",
		Channel:   "corporate-comm",
		Args: map[string]any{
			"to":      "client@externalpartner.com",
			"subject": "Confidential Report",
			"body":    "Here is the weekly update.",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", act3.State)
	assert.Contains(t, act3.Reason, "Sending email to external domains requires operator approval")

	// Operator rejects external email
	rejectedEmail, err := svc.Decide(ctx, act3.ID, act3.Digest, "reject")
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejectedEmail.State)

	// 4. Publishing document requires operator approval
	act4, err := svc.Submit(ctx, "bot", "doc-1", Request{
		Operation: "publish_document",
		Channel:   "corporate-comm",
		Args: map[string]any{
			"title":   "Q3 Earnings Memo",
			"content": "Financial summary...",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", act4.State)
	assert.Contains(t, act4.Reason, "Publishing documents broadly requires operator approval")

	// 5. Test Rate / Velocity limits on channel scope (max_calls: 2 for send_message)
	// act1 and act2 each consumed 1 slot (total 2).
	// Next message must be denied by budget limit
	act5, err := svc.Submit(ctx, "bot", "msg-excess", Request{
		Operation: "send_message",
		Channel:   "corporate-comm",
		Args: map[string]any{
			"channel": "#engineering",
			"message": "Another update",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "denied", act5.State)
	assert.Contains(t, act5.Reason, "budget message-rate exhausted")
}

func TestCommMCPIntegration(t *testing.T) {
	ctx := context.Background()
	cfgYAML := `
name: comm-mcp-gateway
admin_token_env: ADMIN_TOKEN
approval_ttl: 15m
communications:
  - id: slack
    kind: chat
    allowed_channels: ["#engineering"]
agents:
  - id: notifier
    token_env: NOTIFIER_TOKEN
    channels: ["slack"]
    actions:
      - send_message
      - send_email
      - create_ticket
      - update_ticket
      - publish_document
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	target, err := NewCommTarget("slack", "chat", []string{"#engineering"}, nil, 10, false)
	require.NoError(t, err)
	router := NewRouterExecutor(nil, nil, nil, nil, map[string]*CommExecutor{
		"slack": NewCommExecutor(target),
	}, nil, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)

	tokens := Tokens{
		Admin:  "admin-secret-token-32-chars-long-circuit",
		Agents: map[string]string{"notifier": "notifier-secret-token-32-chars-long"},
	}
	handler, err := NewHTTPHandler(svc, tokens)
	require.NoError(t, err)

	server := handler.mcpServer(cfg.Agents[0])
	require.NotNil(t, server)

	// Call comm_send_message
	act, err := svc.Submit(ctx, "notifier", "mcp-comm-msg-1", Request{
		Operation: "send_message",
		Channel:   "slack",
		Args: map[string]any{
			"channel": "#engineering",
			"message": "Build passed all tests",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act.State)
	assert.NotNil(t, act.Outcome)
	assert.Equal(t, 200, act.Outcome.Status)
}
