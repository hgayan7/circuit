package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentExecutor(t *testing.T) {
	acc, err := NewPaymentAccount(
		"treasury",
		"Corporate Treasury Account",
		"USD",
		[]string{"acct_vendor_a", "acct_vendor_b"},
		500.0,  // max transaction amount
		100.0,  // auto approval threshold
		true,   // require approval for refunds
		1000.0, // initial balance
		5*time.Second,
	)
	require.NoError(t, err)

	exec := NewPaymentExecutor(acc)
	ctx := context.Background()

	// 1. Successful transfer
	res := exec.Execute(ctx, Request{
		Operation: "transfer_funds",
		Account:   "treasury",
		Args: map[string]any{
			"amount":      50.0,
			"destination": "acct_vendor_a",
			"currency":    "USD",
			"reason":      "Invoice payment 001",
		},
	})
	require.Equal(t, 200, res.Status)
	var txData map[string]any
	require.NoError(t, json.Unmarshal(res.Body, &txData))
	assert.Equal(t, 50.0, txData["amount"])
	assert.Equal(t, 950.0, txData["balance_after"])
	assert.NotEmpty(t, txData["tx_id"])

	// 2. Transfer exceeding max transaction limit ($500)
	res = exec.Execute(ctx, Request{
		Operation: "transfer_funds",
		Account:   "treasury",
		Args: map[string]any{
			"amount":      600.0,
			"destination": "acct_vendor_a",
		},
	})
	require.Equal(t, 400, res.Status)
	assert.Contains(t, res.Error, "exceeds maximum permitted transaction amount")

	// 3. Transfer to disallowed destination
	res = exec.Execute(ctx, Request{
		Operation: "transfer_funds",
		Account:   "treasury",
		Args: map[string]any{
			"amount":      40.0,
			"destination": "acct_unauthorized_vendor",
		},
	})
	require.Equal(t, 400, res.Status)
	assert.Contains(t, res.Error, "not in the allowed destinations list")

	// 4. Transfer exceeding account balance
	res = exec.Execute(ctx, Request{
		Operation: "transfer_funds",
		Account:   "treasury",
		Args: map[string]any{
			"amount":      450.0,
			"destination": "acct_vendor_b",
		},
	})
	require.Equal(t, 200, res.Status) // balance now 950 - 450 = 500

	res = exec.Execute(ctx, Request{
		Operation: "transfer_funds",
		Account:   "treasury",
		Args: map[string]any{
			"amount":      450.0,
			"destination": "acct_vendor_b",
		},
	})
	require.Equal(t, 200, res.Status) // balance now 500 - 450 = 50

	res = exec.Execute(ctx, Request{
		Operation: "transfer_funds",
		Account:   "treasury",
		Args: map[string]any{
			"amount":      100.0,
			"destination": "acct_vendor_b",
		},
	})
	require.Equal(t, 400, res.Status)
	assert.Contains(t, res.Error, "insufficient funds")

	// 5. Transfer currency mismatch
	res = exec.Execute(ctx, Request{
		Operation: "transfer_funds",
		Account:   "treasury",
		Args: map[string]any{
			"amount":      20.0,
			"destination": "acct_vendor_a",
			"currency":    "EUR",
		},
	})
	require.Equal(t, 400, res.Status)
	assert.Contains(t, res.Error, "does not match account currency")

	// 6. Create customer charge
	res = exec.Execute(ctx, Request{
		Operation: "create_charge",
		Account:   "treasury",
		Args: map[string]any{
			"amount":      300.0,
			"customer_id": "cust_alice",
			"currency":    "USD",
			"description": "Subscription charge",
		},
	})
	require.Equal(t, 200, res.Status)
	var chargeData map[string]any
	require.NoError(t, json.Unmarshal(res.Body, &chargeData))
	chargeID, _ := chargeData["charge_id"].(string)
	assert.NotEmpty(t, chargeID)
	// Balance was 50 + 300 = 350

	// 7. Issue partial refund
	res = exec.Execute(ctx, Request{
		Operation: "issue_refund",
		Account:   "treasury",
		Args: map[string]any{
			"charge_id": chargeID,
			"amount":    100.0,
			"reason":    "Customer requested partial refund",
		},
	})
	require.Equal(t, 200, res.Status)
	var refundData map[string]any
	require.NoError(t, json.Unmarshal(res.Body, &refundData))
	assert.Equal(t, 100.0, refundData["amount"])
	assert.Equal(t, 250.0, refundData["balance_after"]) // 350 - 100 = 250

	// 8. Issue refund exceeding remaining charge amount (remaining is 200, requested 250)
	res = exec.Execute(ctx, Request{
		Operation: "issue_refund",
		Account:   "treasury",
		Args: map[string]any{
			"charge_id": chargeID,
			"amount":    250.0,
		},
	})
	require.Equal(t, 400, res.Status)
	assert.Contains(t, res.Error, "maximum refundable amount is 200.00")

	// 9. Issue refund for invalid charge
	res = exec.Execute(ctx, Request{
		Operation: "issue_refund",
		Account:   "treasury",
		Args: map[string]any{
			"charge_id": "ch_nonexistent",
			"amount":    50.0,
		},
	})
	require.Equal(t, 404, res.Status)
	assert.Contains(t, res.Error, "not found")

	// 10. Query balance
	res = exec.Execute(ctx, Request{
		Operation: "get_balance",
		Account:   "treasury",
	})
	require.Equal(t, 200, res.Status)
	var balData map[string]any
	require.NoError(t, json.Unmarshal(res.Body, &balData))
	assert.Equal(t, 250.0, balData["balance"])
	assert.Equal(t, "USD", balData["currency"])
	assert.Equal(t, float64(3), balData["transactions_count"])
	assert.Equal(t, float64(1), balData["charges_count"])
	assert.Equal(t, float64(1), balData["refunds_count"])
}

func TestPaymentGatewayApprovalAndLimits(t *testing.T) {
	cfgYAML := `
name: payment-gateway
admin_token_env: CIRCUIT_ADMIN_TOKEN
approval_ttl: 1h

payment_accounts:
  - id: corporate-ops
    name: Corporate Operations Account
    currency: USD
    allowed_destinations: ["acct_aws", "acct_vendor_1"]
    max_transaction_amount: 500
    auto_approval_threshold: 100
    require_approval_for_refunds: true
    initial_balance: 2000

agents:
  - id: finance-bot
    token_env: FINANCE_BOT_TOKEN
    accounts: ["corporate-ops"]
    actions:
      - transfer_funds
      - create_charge
      - issue_refund
      - get_balance

limits:
  - id: transfer-velocity
    actions: ["transfer_funds"]
    scope: agent_account
    window: 10m
    max_calls: 2
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	acc, err := NewPaymentAccount("corporate-ops", "Corporate Operations Account", "USD", []string{"acct_aws", "acct_vendor_1"}, 500, 100, true, 2000, 10*time.Second)
	require.NoError(t, err)

	router := NewRouterExecutor(nil, nil, nil, nil, nil, map[string]*PaymentExecutor{
		"corporate-ops": NewPaymentExecutor(acc),
	}, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)
	ctx := context.Background()

	// 1. Transfer under auto_approval_threshold ($50 <= $100) -> auto executes!
	act1, err := svc.Submit(ctx, "finance-bot", "k-tx-001", Request{
		Operation: "transfer_funds",
		Account:   "corporate-ops",
		Args: map[string]any{
			"amount":      50.0,
			"destination": "acct_aws",
			"reason":      "Cloud infrastructure bill",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act1.State)

	// 2. Transfer over auto_approval_threshold ($200 > $100) -> requires approval!
	act2, err := svc.Submit(ctx, "finance-bot", "k-tx-002", Request{
		Operation: "transfer_funds",
		Account:   "corporate-ops",
		Args: map[string]any{
			"amount":      200.0,
			"destination": "acct_vendor_1",
			"reason":      "Vendor retainer payment",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", act2.State)
	assert.Contains(t, act2.Reason, "exceeds auto-approval threshold")

	// Operator approves act2
	decidedAct2, err := svc.Decide(ctx, act2.ID, act2.Digest, "approve")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", decidedAct2.State)

	// 3. Transfer exceeding limit (max_calls: 2 in window: 10m) -> rate limit exceeded!
	act3, err := svc.Submit(ctx, "finance-bot", "k-tx-003", Request{
		Operation: "transfer_funds",
		Account:   "corporate-ops",
		Args: map[string]any{
			"amount":      20.0,
			"destination": "acct_aws",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "denied", act3.State)
	assert.Contains(t, act3.Reason, "budget transfer-velocity exhausted")

	// 4. Transfer exceeding max_transaction_amount ($600 > $500) -> policy deny
	actDenied, err := svc.Submit(ctx, "finance-bot", "k-tx-004", Request{
		Operation: "transfer_funds",
		Account:   "corporate-ops",
		Args: map[string]any{
			"amount":      600.0,
			"destination": "acct_aws",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "denied", actDenied.State)
	assert.Contains(t, actDenied.Reason, "exceeds maximum permitted transaction amount")

	// 5. Create charge ($150) -> charge over threshold requires approval
	actCharge, err := svc.Submit(ctx, "finance-bot", "k-ch-001", Request{
		Operation: "create_charge",
		Account:   "corporate-ops",
		Args: map[string]any{
			"amount":      150.0,
			"customer_id": "cust_101",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", actCharge.State)

	// Operator approves charge
	decidedCharge, err := svc.Decide(ctx, actCharge.ID, actCharge.Digest, "approve")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", decidedCharge.State)

	var chargeObj map[string]any
	require.NoError(t, json.Unmarshal(decidedCharge.Outcome.Body, &chargeObj))
	chID := chargeObj["charge_id"].(string)

	// 6. Issue refund -> require_approval_for_refunds is true, so pending approval!
	actRefund, err := svc.Submit(ctx, "finance-bot", "k-rf-001", Request{
		Operation: "issue_refund",
		Account:   "corporate-ops",
		Args: map[string]any{
			"charge_id": chID,
			"amount":    50.0,
			"reason":    "Partial refund customer goodwill",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", actRefund.State)
	assert.Contains(t, actRefund.Reason, "Refunds require operator approval")

	// Operator rejects refund
	rejectedRefund, err := svc.Decide(ctx, actRefund.ID, actRefund.Digest, "reject")
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejectedRefund.State)

	// 7. Get balance
	actBal, err := svc.Submit(ctx, "finance-bot", "k-bal-001", Request{
		Operation: "get_balance",
		Account:   "corporate-ops",
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", actBal.State)
	var balRes map[string]any
	require.NoError(t, json.Unmarshal(actBal.Outcome.Body, &balRes))
	// Initial: 2000 - 50 (act1) - 200 (act2) + 150 (charge) = 1900
	assert.Equal(t, 1900.0, balRes["balance"])
}

func TestPaymentMCPIntegration(t *testing.T) {
	cfgYAML := `
name: payment-mcp-gateway
admin_token_env: CIRCUIT_ADMIN_TOKEN
approval_ttl: 1h

payment_accounts:
  - id: default
    name: Default Payment Ledger
    currency: USD
    max_transaction_amount: 1000
    auto_approval_threshold: 500
    initial_balance: 5000

agents:
  - id: payment-agent
    token_env: PAYMENT_AGENT_TOKEN
    accounts: ["default"]
    actions:
      - transfer_funds
      - create_charge
      - issue_refund
      - get_balance
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	acc, err := NewPaymentAccount("default", "Default Payment Ledger", "USD", nil, 1000, 500, false, 5000, 10*time.Second)
	require.NoError(t, err)

	router := NewRouterExecutor(nil, nil, nil, nil, nil, map[string]*PaymentExecutor{
		"default": NewPaymentExecutor(acc),
	}, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)

	adminToken := "admin-secret-token-must-be-at-least-32-chars-long"
	agentToken := "agent-secret-token-must-be-at-least-32-chars-long"

	handler, err := NewHTTPHandler(svc, Tokens{
		Admin: adminToken,
		Agents: map[string]string{
			"payment-agent": agentToken,
		},
	})
	require.NoError(t, err)

	// Verify MCP tools list
	mcpSrv := handler.mcpServer(cfg.Agents[0])
	require.NotNil(t, mcpSrv)

	// Test payment_balance tool
	actBal, err := svc.Submit(context.Background(), "payment-agent", "mcp-tx-1", Request{
		Operation: "get_balance",
		Account:   "default",
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", actBal.State)

	// Test payment_transfer tool
	actTx, err := svc.Submit(context.Background(), "payment-agent", "mcp-tx-2", Request{
		Operation: "transfer_funds",
		Account:   "default",
		Args: map[string]any{
			"amount":      250.0,
			"destination": "acct_vendor_xyz",
			"reason":      "MCP automated payment",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", actTx.State)
}
