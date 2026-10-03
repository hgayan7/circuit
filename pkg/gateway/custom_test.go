package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomToolExecutor(t *testing.T) {
	ctx := context.Background()

	// 1. Built-in simulator mode
	simTarget, err := NewCustomToolTarget("local-sim", "Local Simulator", "mock:local", "POST", nil, nil, false, 5*time.Second)
	require.NoError(t, err)

	simExec := NewCustomToolExecutor(simTarget)
	res := simExec.Execute(ctx, Request{
		Operation:  "call_custom_tool",
		CustomTool: "local-sim",
		Args: map[string]any{
			"tool":    "local-sim",
			"payload": map[string]any{"key": "value123"},
		},
	})
	require.Equal(t, 200, res.Status)
	var simData map[string]any
	require.NoError(t, json.Unmarshal(res.Body, &simData))
	assert.Equal(t, "executed", simData["status"])
	assert.Equal(t, "local-sim", simData["tool"])
	assert.NotEmpty(t, simData["call_id"])

	// 2. Custom mock handler
	mockTarget, err := NewCustomToolTarget("mock-service", "Mock Service", "mock:service", "POST", nil, nil, false, 5*time.Second)
	require.NoError(t, err)
	mockTarget.SetMockHandler(func(req Request) (int, any, error) {
		return 201, map[string]any{"received_op": req.Operation, "custom_status": "ok"}, nil
	})

	mockExec := NewCustomToolExecutor(mockTarget)
	res = mockExec.Execute(ctx, Request{
		Operation:  "custom_action",
		CustomTool: "mock-service",
		Args: map[string]any{
			"foo": "bar",
		},
	})
	require.Equal(t, 201, res.Status)
	var mockData map[string]any
	require.NoError(t, json.Unmarshal(res.Body, &mockData))
	assert.Equal(t, "custom_action", mockData["received_op"])
	assert.Equal(t, "ok", mockData["custom_status"])

	// 3. Live HTTP server
	receivedHeaders := http.Header{}
	var receivedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream":"success","processed":true}`))
	}))
	defer ts.Close()

	httpTarget, err := NewCustomToolTarget(
		"remote-api",
		"Remote Internal API",
		ts.URL+"/v1/endpoint",
		"POST",
		map[string]string{"X-Api-Key": "secret-12345", "X-Custom-Env": "staging"},
		nil,
		false,
		10*time.Second,
	)
	require.NoError(t, err)

	httpExec := NewCustomToolExecutor(httpTarget)
	res = httpExec.Execute(ctx, Request{
		Operation:  "call_custom_tool",
		CustomTool: "remote-api",
		Args: map[string]any{
			"tool":    "remote-api",
			"payload": map[string]any{"customer_id": "cust_456"},
		},
	})
	require.Equal(t, 200, res.Status)
	assert.Equal(t, "secret-12345", receivedHeaders.Get("X-Api-Key"))
	assert.Equal(t, "staging", receivedHeaders.Get("X-Custom-Env"))
	assert.Contains(t, string(receivedBody), "cust_456")
	assert.Contains(t, string(res.Body), `"upstream":"success"`)
}

func TestCustomToolGatewayApprovalAndLimits(t *testing.T) {
	cfgYAML := `
name: custom-tools-gateway
admin_token_env: CIRCUIT_ADMIN_TOKEN
approval_ttl: 1h

custom_tools:
  - id: crm-sync
    name: CRM Synchronization
    endpoint: mock:crm
    operations: ["sync_customer"]
    require_approval: false

  - id: prod-pipeline
    name: Production CI/CD Webhook
    endpoint: mock:pipeline
    operations: ["trigger_pipeline"]
    require_approval: true

agents:
  - id: integration-bot
    token_env: INTEGRATION_BOT_TOKEN
    custom_tools: ["crm-sync", "prod-pipeline"]
    actions:
      - call_custom_tool
      - sync_customer
      - trigger_pipeline

limits:
  - id: crm-sync-limit
    actions: ["sync_customer"]
    scope: custom_tool
    window: 5m
    max_calls: 2
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	crmTarget, err := NewCustomToolTarget("crm-sync", "CRM Synchronization", "mock:crm", "POST", nil, []string{"sync_customer"}, false, 5*time.Second)
	require.NoError(t, err)

	pipelineTarget, err := NewCustomToolTarget("prod-pipeline", "Production CI/CD Webhook", "mock:pipeline", "POST", nil, []string{"trigger_pipeline"}, true, 5*time.Second)
	require.NoError(t, err)

	router := NewRouterExecutor(nil, nil, nil, nil, nil, nil, map[string]*CustomToolExecutor{
		"crm-sync":      NewCustomToolExecutor(crmTarget),
		"prod-pipeline": NewCustomToolExecutor(pipelineTarget),
	})

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)
	ctx := context.Background()

	// 1. Non-approval custom action (sync_customer) executes immediately
	act1, err := svc.Submit(ctx, "integration-bot", "k-crm-01", Request{
		Operation:  "sync_customer",
		CustomTool: "crm-sync",
		Args: map[string]any{
			"customer_id": "cust_123",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act1.State)

	// 2. Second call to sync_customer within budget
	act2, err := svc.Submit(ctx, "integration-bot", "k-crm-02", Request{
		Operation:  "sync_customer",
		CustomTool: "crm-sync",
		Args: map[string]any{
			"customer_id": "cust_456",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", act2.State)

	// 3. Third call exceeds budget limit (max_calls: 2) -> denied
	act3, err := svc.Submit(ctx, "integration-bot", "k-crm-03", Request{
		Operation:  "sync_customer",
		CustomTool: "crm-sync",
		Args: map[string]any{
			"customer_id": "cust_789",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "denied", act3.State)
	assert.Contains(t, act3.Reason, "budget crm-sync-limit exhausted")

	// 4. Action requiring approval (trigger_pipeline on prod-pipeline) -> pending
	actPipeline, err := svc.Submit(ctx, "integration-bot", "k-pipe-01", Request{
		Operation:  "trigger_pipeline",
		CustomTool: "prod-pipeline",
		Args: map[string]any{
			"branch": "release/v2.0",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", actPipeline.State)
	assert.Contains(t, actPipeline.Reason, "require operator approval")

	// Operator approves
	approvedPipeline, err := svc.Decide(ctx, actPipeline.ID, actPipeline.Digest, "approve")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", approvedPipeline.State)

	// 5. Generic call_custom_tool on prod-pipeline -> also pending
	actGeneric, err := svc.Submit(ctx, "integration-bot", "k-pipe-02", Request{
		Operation:  "call_custom_tool",
		CustomTool: "prod-pipeline",
		Args: map[string]any{
			"tool":    "prod-pipeline",
			"payload": map[string]any{"action": "deploy_canary"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", actGeneric.State)

	// Operator rejects
	rejectedGeneric, err := svc.Decide(ctx, actGeneric.ID, actGeneric.Digest, "reject")
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejectedGeneric.State)

	// 6. Accessing an unallowed custom tool -> denied
	actUnauthorized, err := svc.Submit(ctx, "integration-bot", "k-unauth", Request{
		Operation:  "call_custom_tool",
		CustomTool: "secret-vault-api",
		Args: map[string]any{
			"tool": "secret-vault-api",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "denied", actUnauthorized.State)
	assert.Contains(t, actUnauthorized.Reason, "not permitted to access custom tool")
}

func TestCustomToolMCPIntegration(t *testing.T) {
	cfgYAML := `
name: custom-tools-mcp-gateway
admin_token_env: CIRCUIT_ADMIN_TOKEN
approval_ttl: 1h

custom_tools:
  - id: inventory-api
    name: Inventory API
    endpoint: mock:inventory
    operations: ["check_stock"]

agents:
  - id: warehouse-bot
    token_env: WAREHOUSE_BOT_TOKEN
    custom_tools: ["inventory-api"]
    actions:
      - call_custom_tool
      - check_stock
`
	cfg, err := ParseConfig(strings.NewReader(cfgYAML))
	require.NoError(t, err)

	invTarget, err := NewCustomToolTarget("inventory-api", "Inventory API", "mock:inventory", "POST", nil, []string{"check_stock"}, false, 5*time.Second)
	require.NoError(t, err)

	router := NewRouterExecutor(nil, nil, nil, nil, nil, nil, map[string]*CustomToolExecutor{
		"inventory-api": NewCustomToolExecutor(invTarget),
	})

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
			"warehouse-bot": agentToken,
		},
	})
	require.NoError(t, err)

	mcpSrv := handler.mcpServer(cfg.Agents[0])
	require.NotNil(t, mcpSrv)

	// Execute custom check_stock action
	actStock, err := svc.Submit(context.Background(), "warehouse-bot", "mcp-inv-1", Request{
		Operation:  "check_stock",
		CustomTool: "inventory-api",
		Args: map[string]any{
			"sku": "ITEM-9988",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", actStock.State)

	// Execute call_custom_tool
	actCall, err := svc.Submit(context.Background(), "warehouse-bot", "mcp-inv-2", Request{
		Operation:  "call_custom_tool",
		CustomTool: "inventory-api",
		Args: map[string]any{
			"tool":    "inventory-api",
			"payload": map[string]any{"sku": "ITEM-1122"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", actCall.State)
}
