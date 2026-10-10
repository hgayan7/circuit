package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hgayan7/circuit/pkg/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestExplicitRulesOverrideApprovalDefaults(t *testing.T) {
	requests := []Request{
		mergeRequest(),
		{Operation: "delete_file", Workspace: "workspace", Args: map[string]any{"path": "ticket.txt"}},
		{Operation: "write_file", Workspace: "workspace", Args: map[string]any{"path": "ticket.txt", "overwrite": true}},
		{Operation: "exec_cmd", Workspace: "workspace", Args: map[string]any{"command": "echo ticket"}},
		{Operation: "exec_sql", Database: "database", Args: map[string]any{"query": "TRUNCATE tickets"}},
		{Operation: "rollback_deployment", Environment: "prod", Args: map[string]any{}},
		{Operation: "publish_document", Channel: "channel", Args: map[string]any{}},
		{Operation: "issue_refund", Account: "account", Args: map[string]any{"amount": 10.0}},
		{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{}},
	}
	for _, req := range requests {
		t.Run(req.Operation, func(t *testing.T) {
			c := &Config{
				Agents:          []Agent{{ID: "agent", Actions: []string{req.Operation}, Repositories: []string{"acme/app"}, Workspaces: []string{"workspace"}, Databases: []string{"database"}, Environments: []string{"prod"}, Channels: []string{"channel"}, Accounts: []string{"account"}, CustomTools: []string{"inventory"}}},
				Environments:    []CloudEnvironmentConfig{{ID: "prod", Production: true}},
				PaymentAccounts: []PaymentAccountConfig{{ID: "account", RequireApprovalForRefunds: true, MaxTransactionAmount: 20}},
				CustomTools:     []CustomToolConfig{{ID: "inventory", Protocol: PluginProtocol, Operations: []string{"reserve_item"}, RequireApproval: true}},
			}
			verdict, _, err := c.evaluate("agent", req)
			require.NoError(t, err)
			require.Equal(t, config.ActionRequireApproval, verdict, "no matching rule retains the approval default")
			allow := Rule{ID: "autonomous", Actions: []string{req.Operation}, Action: config.ActionAllow}
			review := Rule{ID: "review", Actions: []string{req.Operation}, Action: config.ActionRequireApproval}
			deny := Rule{ID: "deny", Actions: []string{req.Operation}, Action: config.ActionDeny}
			for _, tc := range []struct {
				rules []Rule
				want  config.ActionType
			}{
				{[]Rule{allow}, config.ActionAllow},
				{[]Rule{allow, review}, config.ActionRequireApproval},
				{[]Rule{review, allow}, config.ActionRequireApproval},
				{[]Rule{allow, review, deny}, config.ActionDeny},
				{[]Rule{deny, review, allow}, config.ActionDeny},
			} {
				c.Rules = tc.rules
				verdict, _, err = c.evaluate("agent", req)
				require.NoError(t, err)
				require.Equal(t, tc.want, verdict)
			}
			c.Rules = []Rule{allow}
			verdict, _, err = c.evaluate("unknown-agent", req)
			require.NoError(t, err)
			require.Equal(t, config.ActionDeny, verdict)
			if req.Operation == "issue_refund" {
				req.Args["amount"] = 30.0
				verdict, _, err = c.evaluate("agent", req)
				require.NoError(t, err)
				require.Equal(t, config.ActionDeny, verdict, "ALLOW cannot override a transaction cap")
			}
		})
	}
}

func parseAutonomyConfig(t *testing.T, c *Config) *Config {
	t.Helper()
	data, err := yaml.Marshal(c)
	require.NoError(t, err)
	parsed, err := ParseConfig(strings.NewReader(string(data)))
	require.NoError(t, err)
	return parsed
}

func TestAutonomousAllowCannotBypassPolicyErrorsOrScope(t *testing.T) {
	for _, rules := range []string{
		`- id: allow
  actions: [merge_pr]
  action: ALLOW
- id: invalid-runtime-input
  actions: [merge_pr]
  condition: args.missing_confirmation == true
  action: REQUIRE_APPROVAL
`,
		`- id: invalid-runtime-input
  actions: [merge_pr]
  condition: args.missing_confirmation == true
  action: REQUIRE_APPROVAL
- id: allow
  actions: [merge_pr]
  action: ALLOW
`,
	} {
		c := testConfig(t, "rules:\n"+rules)
		e := &countingExecutor{}
		s, _ := testService(t, c, e)
		a, err := s.Submit(context.Background(), "agent", "missing-input", mergeRequest())
		require.NoError(t, err)
		require.Equal(t, "denied", a.State)
		require.Equal(t, "Policy evaluation failed", a.Reason)
		req := mergeRequest()
		req.Repository = "other/app"
		a, err = s.Submit(context.Background(), "agent", "scope", req)
		require.NoError(t, err)
		require.Equal(t, "denied", a.State)
		require.Zero(t, e.calls.Load())
	}
}

func TestAutonomousRESTWritesKeepScopeBudgetAndIdempotency(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/reserve", r.URL.Path)
		require.Equal(t, "Bearer "+upstreamSecret, r.Header.Get("Authorization"))
		require.NotEmpty(t, r.Header.Get("Idempotency-Key"))
		calls.Add(1)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	c := forwardedConfig(t, RESTForwardProtocol, upstream.URL)
	c.Rules = []Rule{
		{ID: "reserve", Actions: []string{"reserve_item"}, Action: config.ActionAllow, Condition: `custom_tool == "inventory" && args.sku.startsWith("auto-")`},
		{ID: "review", Actions: []string{"reserve_item"}, Action: config.ActionRequireApproval, Condition: `args.sku == "auto-review"`},
		{ID: "deny", Actions: []string{"reserve_item"}, Action: config.ActionDeny, Condition: `args.sku == "auto-deny"`},
	}
	c.Limits = []Limit{{ID: "reservations", Actions: []string{"reserve_item"}, Scope: "agent_custom_tool", Window: "1h", MaxCalls: 1}}
	c = parseAutonomyConfig(t, c)
	s, _ := testService(t, c, pluginExecutor(t, c))
	h, err := NewHTTPHandler(s, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.NoError(t, err)
	server := httptest.NewServer(h)
	defer server.Close()
	submit := func(key, sku, target string) Action {
		t.Helper()
		_, data := callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, key, Request{Operation: "reserve_item", CustomTool: target, Args: map[string]any{"sku": sku}})
		var a Action
		require.NoError(t, json.Unmarshal(data, &a))
		return a
	}
	require.Equal(t, "denied", submit("scope", "auto-ticket", "other").State)
	require.Equal(t, "denied", submit("deny", "auto-deny", "inventory").State)
	for _, sku := range []string{"auto-review", "no-matching-rule"} {
		a := submit(sku, sku, "inventory")
		require.Equal(t, "pending", a.State)
		require.Zero(t, calls.Load())
		_, err := s.Decide(context.Background(), a.ID, a.Digest, "reject")
		require.NoError(t, err)
	}
	a := submit("purchase", "auto-ticket", "inventory")
	require.Equal(t, "succeeded", a.State)
	require.Equal(t, "policy", a.ApprovedBy)
	require.Equal(t, a.ID, submit("purchase", "auto-ticket", "inventory").ID)
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, "denied", submit("over-budget", "auto-second-ticket", "inventory").State)
	require.EqualValues(t, 1, calls.Load())
}

func TestAutonomousRESTUncertainWriteIsNotReplayedAfterRestart(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		conn.Close() // The provider accepted the write but its reply was lost.
	}))
	defer upstream.Close()
	c := forwardedConfig(t, RESTForwardProtocol, upstream.URL)
	c.Rules = []Rule{{ID: "autonomous", Actions: []string{"reserve_item"}, Action: config.ActionAllow}}
	c = parseAutonomyConfig(t, c)
	s, path := testService(t, c, pluginExecutor(t, c))
	r := Request{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{"sku": "ticket"}}
	a, err := s.Submit(context.Background(), "agent", "purchase", r)
	require.NoError(t, err)
	require.Equal(t, "uncertain", a.State)
	require.Equal(t, "policy", a.ApprovedBy)
	require.NoError(t, s.store.Close())
	store, err := OpenStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	s, err = NewService(c, store, pluginExecutor(t, c))
	require.NoError(t, err)
	for _, key := range []string{"purchase", "new-key"} {
		again, err := s.Submit(context.Background(), "agent", key, r)
		require.NoError(t, err)
		require.Equal(t, a.ID, again.ID)
		require.Equal(t, "uncertain", again.State)
	}
	require.EqualValues(t, 1, calls.Load())
}
