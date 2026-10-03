package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

const agentToken = "agent-token-for-test-only-at-least-32-characters"
const adminToken = "operator-token-for-test-only-at-least-32-characters"

func testHTTP(t *testing.T) (*Service, *countingExecutor, *httptest.Server) {
	t.Helper()
	e := &countingExecutor{}
	s, _ := testService(t, testConfig(t, ""), e)
	handler, err := NewHTTPHandler(s, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return s, e, server
}
func callHTTP(t *testing.T, url, method, token, key string, value any) (int, []byte) {
	t.Helper()
	var b []byte
	if value != nil {
		var err error
		b, err = json.Marshal(value)
		require.NoError(t, err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(b))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, data
}
func TestHTTPAuthenticationAndExactApprovalWorkflow(t *testing.T) {
	_, e, server := testHTTP(t)
	status, _ := callHTTP(t, server.URL+"/v1/actions", "POST", "wrong", "one", mergeRequest())
	require.Equal(t, 401, status)
	status, _ = callHTTP(t, server.URL+"/admin/actions", "GET", agentToken, "", nil)
	require.Equal(t, 403, status)
	status, _ = callHTTP(t, server.URL+"/v1/actions", "POST", adminToken, "one", mergeRequest())
	require.Equal(t, 403, status)
	status, data := callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, "one", mergeRequest())
	require.Equal(t, 202, status)
	var a Action
	require.NoError(t, json.Unmarshal(data, &a))
	require.Equal(t, "pending", a.State)
	require.Zero(t, e.calls.Load())
	status, _ = callHTTP(t, server.URL+"/admin/actions/"+a.ID+"/decision", "POST", agentToken, "", map[string]string{"digest": a.Digest, "decision": "approve"})
	require.Equal(t, 403, status)
	status, data = callHTTP(t, server.URL+"/admin/actions/"+a.ID+"/decision", "POST", adminToken, "", map[string]string{"digest": a.Digest, "decision": "approve"})
	require.Equal(t, 200, status)
	require.NoError(t, json.Unmarshal(data, &a))
	require.Equal(t, "succeeded", a.State)
	status, _ = callHTTP(t, server.URL+"/v1/actions", "POST", agentToken, "one", mergeRequest())
	require.Equal(t, 200, status)
	require.EqualValues(t, 1, e.calls.Load())
	require.NotContains(t, string(data), adminToken)
	require.NotContains(t, string(data), agentToken)
}

type authTransport struct{ token string }

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	req.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(req)
}
func TestMCPClientToolScopeAndDurableAction(t *testing.T) {
	_, e, server := testHTTP(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: authTransport{agentToken}}}, nil)
	require.NoError(t, err)
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(tools.Tools), 8)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "github_create_pr", Arguments: map[string]any{"repository": "acme/app", "idempotency_key": "mcp-one", "args": prRequest().Args}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	var a Action
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &a))
	require.Equal(t, "succeeded", a.State)
	_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "github_create_pr", Arguments: map[string]any{"repository": "acme/app", "idempotency_key": "mcp-one", "args": prRequest().Args}})
	require.NoError(t, err)
	require.EqualValues(t, 1, e.calls.Load())
}
func TestAgentCannotReadOtherAgentsHistory(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Agents = append(cfg.Agents, Agent{ID: "other", TokenEnv: "OTHER", Repositories: []string{"acme/app"}, Actions: []string{"get_pr"}, BranchPrefix: "circuit/"})
	e := &countingExecutor{}
	s, _ := testService(t, cfg, e)
	handler, err := NewHTTPHandler(s, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken, "other": strings.Repeat("o", 40)}})
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()
	a, err := s.Submit(context.Background(), "agent", "private", prRequest())
	require.NoError(t, err)
	status, _ := callHTTP(t, server.URL+"/v1/actions/"+a.ID, "GET", strings.Repeat("o", 40), "", nil)
	require.Equal(t, 404, status)
	status, data := callHTTP(t, server.URL+"/v1/actions", "GET", strings.Repeat("o", 40), "", nil)
	require.Equal(t, 200, status)
	require.JSONEq(t, "[]", string(data))
}
