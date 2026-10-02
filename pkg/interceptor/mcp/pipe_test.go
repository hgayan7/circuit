package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/himshikhargayan/circuit/pkg/approval"
	"github.com/himshikhargayan/circuit/pkg/audit"
	"github.com/himshikhargayan/circuit/pkg/config"
	"github.com/himshikhargayan/circuit/pkg/interceptor/mcp"
	"github.com/himshikhargayan/circuit/pkg/policy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestEngine(t *testing.T) *policy.Engine {
	yamlPolicy := `
name: "mcp-test-policy"
default_action: ALLOW
rules:
  - id: "deny-destructive-sql"
    match:
      tool: "postgres.query"
    condition: "args.sql.matches('(?i)(DROP|TRUNCATE|DELETE FROM)')"
    action: DENY
    reason: "Destructive SQL statements are blocked"

  - id: "require-approval-restart"
    match:
      tool: "system.restart"
    action: REQUIRE_APPROVAL
    reason: "System restart requires operator approval"
`
	pol, err := config.ParsePolicy(strings.NewReader(yamlPolicy))
	require.NoError(t, err)

	engine, err := policy.NewEngine(pol)
	require.NoError(t, err)
	return engine
}

func TestMCPPipe_PassThrough_NonToolCall(t *testing.T) {
	engine := setupTestEngine(t)
	var auditBuf bytes.Buffer
	rec := audit.NewJSONRecorder(&auditBuf)

	initMsg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}` + "\n"

	clientIn := strings.NewReader(initMsg)
	var clientOut bytes.Buffer
	var downstreamIn bytes.Buffer
	downstreamOut := strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}` + "\n")

	pipe := mcp.NewPipe(engine,
		mcp.WithAuditRecorder(rec),
		mcp.WithToolPrefix("postgres"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := pipe.Run(ctx, clientIn, &clientOut, downstreamOut, &downstreamIn)
	require.NoError(t, err)

	// Non-tool call was forwarded directly to downstream
	assert.Contains(t, downstreamIn.String(), `"method":"initialize"`)
	// Downstream response forwarded to client
	assert.Contains(t, clientOut.String(), `"result":{"protocolVersion"`)
}

func TestMCPPipe_ToolsCall_Allowed(t *testing.T) {
	engine := setupTestEngine(t)
	var auditBuf bytes.Buffer
	rec := audit.NewJSONRecorder(&auditBuf)

	safeCall := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"query","arguments":{"sql":"SELECT * FROM users;"}}}` + "\n"

	clientIn := strings.NewReader(safeCall)
	var clientOut bytes.Buffer
	var downstreamIn bytes.Buffer
	downstreamOut := strings.NewReader(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"OK"}]}}` + "\n")

	pipe := mcp.NewPipe(engine,
		mcp.WithAuditRecorder(rec),
		mcp.WithToolPrefix("postgres"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := pipe.Run(ctx, clientIn, &clientOut, downstreamOut, &downstreamIn)
	require.NoError(t, err)

	// Call passed through to downstream
	assert.Contains(t, downstreamIn.String(), `"name":"query"`)
	assert.Contains(t, clientOut.String(), `"text":"OK"`)

	// Audited as ALLOW
	assert.Contains(t, auditBuf.String(), `"decision":"ALLOW"`)
}

func TestMCPPipe_ToolsCall_Denied(t *testing.T) {
	engine := setupTestEngine(t)
	var auditBuf bytes.Buffer
	rec := audit.NewJSONRecorder(&auditBuf)

	destructiveCall := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query","arguments":{"sql":"DROP TABLE production_users;"}}}` + "\n"

	clientIn := strings.NewReader(destructiveCall)
	var clientOut bytes.Buffer
	var downstreamIn bytes.Buffer
	downstreamOut := strings.NewReader("")

	pipe := mcp.NewPipe(engine,
		mcp.WithAuditRecorder(rec),
		mcp.WithToolPrefix("postgres"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := pipe.Run(ctx, clientIn, &clientOut, downstreamOut, &downstreamIn)
	require.NoError(t, err)

	// Downstream received NOTHING
	assert.Empty(t, downstreamIn.String())

	// Client received JSON-RPC error frame
	var rpcErr struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	err = json.Unmarshal(clientOut.Bytes(), &rpcErr)
	require.NoError(t, err)

	assert.Equal(t, 3, rpcErr.ID)
	assert.Equal(t, -32000, rpcErr.Error.Code)
	assert.Contains(t, rpcErr.Error.Message, "Destructive SQL statements are blocked")

	// Audited as DENY
	assert.Contains(t, auditBuf.String(), `"decision":"DENY"`)
}

func TestMCPPipe_ToolsCall_RequireApproval_Approved(t *testing.T) {
	engine := setupTestEngine(t)
	mockApprover := approval.NewMockProvider(true, "admin@corp", "Maintenance approved")

	reqMsg := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"restart","arguments":{"server":"app-1"}}}` + "\n"

	clientIn := strings.NewReader(reqMsg)
	var clientOut bytes.Buffer
	var downstreamIn bytes.Buffer
	downstreamOut := strings.NewReader(`{"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"restarted"}]}}` + "\n")

	pipe := mcp.NewPipe(engine,
		mcp.WithApprovalProvider(mockApprover),
		mcp.WithToolPrefix("system"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := pipe.Run(ctx, clientIn, &clientOut, downstreamOut, &downstreamIn)
	require.NoError(t, err)

	// Approved -> forwarded downstream
	assert.Contains(t, downstreamIn.String(), `"name":"restart"`)
	assert.Contains(t, clientOut.String(), `"restarted"`)
}

func TestMCPPipe_ToolsCall_RequireApproval_Rejected(t *testing.T) {
	engine := setupTestEngine(t)
	mockApprover := approval.NewMockProvider(false, "admin@corp", "Too risky now")

	reqMsg := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"restart","arguments":{"server":"app-1"}}}` + "\n"

	clientIn := strings.NewReader(reqMsg)
	var clientOut bytes.Buffer
	var downstreamIn bytes.Buffer
	downstreamOut := strings.NewReader("")

	pipe := mcp.NewPipe(engine,
		mcp.WithApprovalProvider(mockApprover),
		mcp.WithToolPrefix("system"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := pipe.Run(ctx, clientIn, &clientOut, downstreamOut, &downstreamIn)
	require.NoError(t, err)

	// Blocked
	assert.Empty(t, downstreamIn.String())
	assert.Contains(t, clientOut.String(), "Rejected by human approver: Too risky now")
}
