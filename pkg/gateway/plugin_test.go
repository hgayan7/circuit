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

	"github.com/stretchr/testify/require"
)

func pluginConfig(t *testing.T, endpoint string) *Config {
	t.Helper()
	c, err := ParseConfig(strings.NewReader(fmt.Sprintf(`name: plugin-test
custom_tools:
- id: inventory
  protocol: circuit-plugin-v1
  endpoint: %s
  token_env: PLUGIN_TOKEN
  operations: [inspect_item, reserve_item]
  read_only_operations: [inspect_item]
agents:
- id: agent
  token_env: AGENT_TOKEN
  custom_tools: [inventory]
  actions: [inspect_item, reserve_item]
`, endpoint)))
	require.NoError(t, err)
	return c
}
func pluginExecutor(t *testing.T, c *Config) *RouterExecutor {
	t.Helper()
	tool := c.CustomTools[0]
	target, err := NewCustomToolTarget(tool.ID, tool.Name, tool.Endpoint, tool.Method, nil, tool.Operations, false, tool.Timeout())
	require.NoError(t, err)
	require.NoError(t, target.ConfigurePlugin(tool))
	return NewRouterExecutor(nil, nil, nil, nil, nil, nil, map[string]*CustomToolExecutor{tool.ID: NewCustomToolExecutor(target)})
}
func TestPluginCannotBypassApprovalOrRedirectTransport(t *testing.T) {
	t.Setenv("PLUGIN_TOKEN", "plugin-fixture-secret-at-least-32-characters")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/execute", r.URL.Path)
		require.Equal(t, "Bearer "+"plugin-fixture-secret-at-least-32-characters", r.Header.Get("Authorization"))
		var call PluginCall
		require.NoError(t, json.NewDecoder(r.Body).Decode(&call))
		require.Equal(t, PluginProtocol, call.Protocol)
		require.NotEmpty(t, call.ActionID)
		require.Equal(t, call.ActionID, r.Header.Get("Idempotency-Key"))
		json.NewEncoder(w).Encode(PluginResult{Protocol: PluginProtocol, ActionID: call.ActionID, Outcome: Outcome{Status: 200, Body: json.RawMessage(`{"ok":true}`)}})
	}))
	defer server.Close()
	cfg := pluginConfig(t, server.URL+"/execute")
	s, _ := testService(t, cfg, pluginExecutor(t, cfg))
	request := Request{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture", "endpoint": "https://attacker.invalid", "method": "DELETE"}}
	a, err := s.Submit(context.Background(), "agent", "reserve", request)
	require.NoError(t, err)
	require.Equal(t, "pending", a.State)
	require.Zero(t, calls.Load())
	a, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "succeeded", a.State)
	require.Equal(t, int32(1), calls.Load())
	again, err := s.Submit(context.Background(), "agent", "reserve", request)
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID)
	require.Equal(t, int32(1), calls.Load())
	read, err := s.Submit(context.Background(), "agent", "read", Request{Operation: "inspect_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", read.State)
	denied, err := s.Submit(context.Background(), "agent", "implicit", Request{Operation: "inspect_item", Args: map[string]any{"sku": "fixture"}})
	require.NoError(t, err)
	require.Equal(t, "denied", denied.State)
}
func TestPluginUnknownWriteOutcomeIsNeverRetried(t *testing.T) {
	t.Setenv("PLUGIN_TOKEN", "plugin-fixture-secret-at-least-32-characters")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "https://attacker.invalid", 307)
	}))
	defer server.Close()
	cfg := pluginConfig(t, server.URL)
	s, _ := testService(t, cfg, pluginExecutor(t, cfg))
	request := Request{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}}
	a, err := s.Submit(context.Background(), "agent", "unknown", request)
	require.NoError(t, err)
	a, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "uncertain", a.State)
	again, err := s.Submit(context.Background(), "agent", "different-key", request)
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID)
	require.Equal(t, int32(1), calls.Load())
}
func TestPluginRejectsUnsafeConfiguration(t *testing.T) {
	valid := CustomToolConfig{ID: "fixture", Protocol: PluginProtocol, Method: "POST", Endpoint: "https://plugins.example/execute", TokenEnv: "PLUGIN_TOKEN", Operations: []string{"inspect_item"}}
	require.NoError(t, validatePluginConfig(valid))
	for _, change := range []func(*CustomToolConfig){
		func(c *CustomToolConfig) { c.Protocol = "v0" },
		func(c *CustomToolConfig) { c.Operations = []string{"merge_pr"} },
		func(c *CustomToolConfig) { c.ReadOnlyOperations = []string{"unknown"} },
		func(c *CustomToolConfig) { c.Endpoint = "http://remote.invalid/execute" },
		func(c *CustomToolConfig) { c.Headers = map[string]string{"Authorization": "secret"} },
		func(c *CustomToolConfig) { c.TokenFile = "other" },
	} {
		c := valid
		change(&c)
		require.Error(t, validatePluginConfig(c))
	}
}
