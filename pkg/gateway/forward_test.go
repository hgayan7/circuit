package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const upstreamSecret = "fixture-upstream-credential-at-least-32-characters"

func forwardedSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"sku"}, "properties": map[string]any{"sku": map[string]any{"type": "string"}}}
}
func forwardedConfig(t *testing.T, protocol, endpoint string) *Config {
	t.Helper()
	t.Setenv("FORWARD_TOKEN", upstreamSecret)
	c := Config{Name: "forward-test", CustomTools: []CustomToolConfig{{ID: "inventory", Protocol: protocol, Endpoint: endpoint, TokenEnv: "FORWARD_TOKEN", Operations: []string{"inspect_item", "reserve_item"}, ReadOnlyOperations: []string{"inspect_item"}}}, Agents: []Agent{{ID: "agent", TokenEnv: "AGENT_TOKEN", CustomTools: []string{"inventory"}, Actions: []string{"inspect_item", "reserve_item"}}}}
	if protocol == MCPForwardProtocol {
		c.CustomTools[0].MCPTools = map[string]ForwardedMCPTool{"inspect_item": {Name: "lookup", InputSchema: forwardedSchema()}, "reserve_item": {Name: "reserve", InputSchema: forwardedSchema()}}
	} else {
		c.CustomTools[0].Routes = []RESTRoute{{Operation: "inspect_item", Method: "GET", Path: "/items", QueryParams: []string{"sku"}, InputSchema: forwardedSchema()}, {Operation: "reserve_item", Method: "POST", Path: "/reserve", InputSchema: forwardedSchema()}}
	}
	data, err := yaml.Marshal(c)
	require.NoError(t, err)
	parsed, err := ParseConfig(strings.NewReader(string(data)))
	require.NoError(t, err)
	return parsed
}

func TestRESTForwardingUsesCoreApprovalScopeAndBudget(t *testing.T) {
	var reads, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+upstreamSecret, r.Header.Get("Authorization"))
		require.NotEmpty(t, r.Header.Get("Idempotency-Key"))
		switch r.URL.Path {
		case "/items":
			require.Equal(t, "GET", r.Method)
			require.Equal(t, "fixture", r.URL.Query().Get("sku"))
			reads.Add(1)
		case "/reserve":
			require.Equal(t, "POST", r.Method)
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, map[string]any{"sku": "fixture"}, body)
			writes.Add(1)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()
	cfg := forwardedConfig(t, RESTForwardProtocol, server.URL)
	cfg.Limits = []Limit{{ID: "writes", Actions: []string{"reserve_item"}, MaxCalls: 1, Window: "1h", Scope: "agent_custom_tool"}}
	data, _ := yaml.Marshal(cfg)
	cfg, err := ParseConfig(strings.NewReader(string(data)))
	require.NoError(t, err)
	s, _ := testService(t, cfg, pluginExecutor(t, cfg))
	ctx := context.Background()
	r := Request{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}}
	a, err := s.Submit(ctx, "agent", "write", r)
	require.NoError(t, err)
	require.Equal(t, "pending", a.State)
	require.Zero(t, writes.Load())
	a, err = s.Decide(ctx, a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "succeeded", a.State)
	require.EqualValues(t, 1, writes.Load())
	again, err := s.Submit(ctx, "agent", "write", r)
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID)
	require.EqualValues(t, 1, writes.Load())
	r.Args["sku"] = "other"
	limited, err := s.Submit(ctx, "agent", "limited", r)
	require.NoError(t, err)
	require.Equal(t, "denied", limited.State)
	require.EqualValues(t, 1, writes.Load())
	read, err := s.Submit(ctx, "agent", "read", Request{Operation: "inspect_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", read.State)
	require.EqualValues(t, 1, reads.Load())
	for _, r := range []Request{
		{Operation: "inspect_item", Args: map[string]any{"sku": "fixture"}},
		{Operation: "inspect_item", CustomTool: "other", Args: map[string]any{"sku": "fixture"}},
		{Operation: "unknown", CustomTool: "inventory", Args: map[string]any{}},
	} {
		denied, err := s.Submit(ctx, "agent", fmt.Sprintf("denied-%s-%s", r.Operation, r.CustomTool), r)
		require.NoError(t, err)
		require.Equal(t, "denied", denied.State)
	}
	bad, err := s.Submit(ctx, "agent", "override", Request{Operation: "inspect_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture", "endpoint": "https://attacker.invalid", "method": "DELETE"}})
	require.NoError(t, err)
	require.Equal(t, "failed", bad.State)
	require.EqualValues(t, 1, reads.Load())
}

func TestRESTUnknownOutcomeAndRedirectAreNotReplayed(t *testing.T) {
	var calls, leaked atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer attacker.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Redirect(w, r, attacker.URL, 307) }))
	defer server.Close()
	cfg := forwardedConfig(t, RESTForwardProtocol, server.URL)
	s, _ := testService(t, cfg, pluginExecutor(t, cfg))
	r := Request{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}}
	a, err := s.Submit(context.Background(), "agent", "write", r)
	require.NoError(t, err)
	a, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "uncertain", a.State)
	again, err := s.Submit(context.Background(), "agent", "different-key", r)
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID)
	require.EqualValues(t, 1, calls.Load())
	require.Zero(t, leaked.Load())
	encoded, _ := json.Marshal(a)
	require.NotContains(t, string(encoded), upstreamSecret)
}

func upstreamMCP(t *testing.T, fail bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	for _, name := range []string{"lookup", "reserve", "unregistered"} {
		s.AddTool(&mcp.Tool{Name: name, InputSchema: forwardedSchema(), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			require.NotEmpty(t, r.Params.Meta["circuit/action_id"])
			calls.Add(1)
			return &mcp.CallToolResult{IsError: fail, Content: []mcp.Content{&mcp.TextContent{Text: "fixture-result"}}}, nil
		})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+upstreamSecret {
			w.WriteHeader(401)
			return
		}
		h.ServeHTTP(w, r)
	}))
	return server, &calls
}

type lostMCPReply struct{ base http.RoundTripper }

func (l lostMCPReply) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body == nil {
		return l.base.RoundTrip(r)
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(strings.NewReader(string(data)))
	response, err := l.base.RoundTrip(r)
	if err == nil && strings.Contains(string(data), `"method":"tools/call"`) {
		response.Body.Close()
		return nil, io.ErrUnexpectedEOF
	}
	return response, err
}

func TestMCPReplyLossRemainsUncertainAcrossRestart(t *testing.T) {
	server, calls := upstreamMCP(t, false)
	defer server.Close()
	cfg := forwardedConfig(t, MCPForwardProtocol, server.URL)
	executor := pluginExecutor(t, cfg)
	client := executor.customTools["inventory"].target.httpClient
	client.Transport = lostMCPReply{base: client.Transport}
	s, file := testService(t, cfg, executor)
	r := Request{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}}
	a, err := s.Submit(context.Background(), "agent", "write", r)
	require.NoError(t, err)
	a, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "uncertain", a.State)
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, s.store.Close())
	store, err := OpenStore(file)
	require.NoError(t, err)
	defer store.Close()
	restarted, err := NewService(cfg, store, pluginExecutor(t, cfg))
	require.NoError(t, err)
	again, err := restarted.Submit(context.Background(), "agent", "different-key", r)
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID)
	require.Equal(t, "uncertain", again.State)
	require.EqualValues(t, 1, calls.Load())
}

func TestForwardOutcomesAreBoundedJSONAndRedactCredentials(t *testing.T) {
	for _, body := range []string{"plain text " + upstreamSecret, `{"text":"` + upstreamSecret + `","large":9007199254740993}`, strings.Repeat("x", (4<<20)+2)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		cfg := forwardedConfig(t, RESTForwardProtocol, server.URL)
		s, _ := testService(t, cfg, pluginExecutor(t, cfg))
		r := Request{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}}
		a, err := s.Submit(context.Background(), "agent", "write", r)
		require.NoError(t, err)
		a, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
		require.NoError(t, err)
		encoded, err := json.Marshal(a)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), upstreamSecret)
		if len(body) > 4<<20 {
			require.Equal(t, "uncertain", a.State)
		} else {
			require.Equal(t, "succeeded", a.State)
			require.True(t, json.Valid(a.Outcome.Body))
			if strings.HasPrefix(body, "{") {
				require.Contains(t, string(a.Outcome.Body), "9007199254740993")
			}
		}
		server.Close()
	}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), executionIDKey{}, "private-inbound-metadata"))
	child, stop := isolatedForwardContext(parent, time.Second)
	defer stop()
	require.Nil(t, child.Value(executionIDKey{}))
	cancel()
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation was lost")
	}
}

func TestMCPForwardingDiscoveryAndExactApprovals(t *testing.T) {
	server, calls := upstreamMCP(t, false)
	defer server.Close()
	cfg := forwardedConfig(t, MCPForwardProtocol, server.URL)
	s, _ := testService(t, cfg, pluginExecutor(t, cfg))
	read, err := s.Submit(context.Background(), "agent", "read", Request{Operation: "inspect_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}})
	require.NoError(t, err)
	require.Equal(t, "succeeded", read.State)
	r := Request{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}}
	a, err := s.Submit(context.Background(), "agent", "write", r)
	require.NoError(t, err)
	require.Equal(t, "pending", a.State)
	require.EqualValues(t, 1, calls.Load(), "untrusted read-only annotation must not waive approval")
	a, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "succeeded", a.State)
	require.EqualValues(t, 2, calls.Load())
	again, err := s.Submit(context.Background(), "agent", "write", r)
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID)
	require.EqualValues(t, 2, calls.Load())
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte(upstreamSecret), 0600))
	manifest, err := DiscoverMCP(context.Background(), server.URL, path, "")
	require.NoError(t, err)
	require.Len(t, manifest.MCPTools, 3)
	require.Empty(t, manifest.ReadOnlyOperations)
	require.EqualValues(t, 2, calls.Load(), "discovery must not execute tools")
	h, err := NewHTTPHandler(s, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.NoError(t, err)
	gateway := httptest.NewServer(h)
	defer gateway.Close()
	client := &http.Client{Transport: bearerRoundTripper{token: agentToken}}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "agent-fixture", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: gateway.URL + "/mcp", HTTPClient: client}, nil)
	require.NoError(t, err)
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	require.Contains(t, names, "forward_inventory_inspect_item")
	require.NotContains(t, names, "unregistered")
	require.NotContains(t, names, "custom_inspect_item")
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "forward_inventory_inspect_item", Arguments: map[string]any{"idempotency_key": "mcp-read", "args": map[string]any{"sku": "fixture"}}})
	require.NoError(t, err)
	encodedResult, _ := json.Marshal(result)
	require.False(t, result.IsError, string(encodedResult))
	require.EqualValues(t, 3, calls.Load())
}

type bearerRoundTripper struct{ token string }

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPRejectsSchemaDriftAndAmbiguousErrors(t *testing.T) {
	for _, failure := range []bool{false, true} {
		server, calls := upstreamMCP(t, failure)
		cfg := forwardedConfig(t, MCPForwardProtocol, server.URL)
		if !failure {
			cfg.CustomTools[0].MCPTools["reserve_item"].InputSchema["maxProperties"] = 1
		}
		s, _ := testService(t, cfg, pluginExecutor(t, cfg))
		r := Request{Operation: "reserve_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}}
		a, err := s.Submit(context.Background(), "agent", "write", r)
		require.NoError(t, err)
		a, err = s.Decide(context.Background(), a.ID, a.Digest, "approve")
		require.NoError(t, err)
		if failure {
			require.Equal(t, "uncertain", a.State)
			again, err := s.Submit(context.Background(), "agent", "new-key", r)
			require.NoError(t, err)
			require.Equal(t, a.ID, again.ID)
			require.EqualValues(t, 1, calls.Load())
		} else {
			require.Equal(t, "failed", a.State)
			require.Zero(t, calls.Load())
		}
		server.Close()
	}
}

func TestForwardingRejectsUnsafeConfigurationAndCredentialReuse(t *testing.T) {
	cfg := forwardedConfig(t, RESTForwardProtocol, "https://upstream.example")
	for _, change := range []func(*CustomToolConfig){
		func(c *CustomToolConfig) { c.Endpoint = "http://remote.example" },
		func(c *CustomToolConfig) { c.Endpoint = "https://upstream.example/path" },
		func(c *CustomToolConfig) { c.Headers = map[string]string{"Authorization": "secret"} },
		func(c *CustomToolConfig) { c.Routes[0].Path = "//attacker.invalid/path" },
		func(c *CustomToolConfig) { c.Routes[0].Path = "/x/../secret" },
		func(c *CustomToolConfig) { c.Routes[0].Path = "/%2e%2e/secret" },
		func(c *CustomToolConfig) { c.Routes[0].Method = "POST" },
		func(c *CustomToolConfig) { c.ReadOnlyOperations = []string{"unknown"} },
		func(c *CustomToolConfig) {
			c.Routes[0].InputSchema = map[string]any{"type": "object", "$ref": "https://attacker.invalid/schema"}
		},
	} {
		data, _ := json.Marshal(cfg.CustomTools[0])
		var c CustomToolConfig
		require.NoError(t, json.Unmarshal(data, &c))
		change(&c)
		require.Error(t, validateForwardConfig(c))
	}
	t.Setenv("FORWARD_TOKEN", agentToken)
	s, _ := testService(t, cfg, pluginExecutor(t, cfg))
	_, err := NewHTTPHandler(s, Tokens{Admin: adminToken, Agents: map[string]string{"agent": agentToken}})
	require.ErrorContains(t, err, "distinct gateway-only credential")
}

func TestForwardedTLSVerifiesTheConfiguredCA(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"ok":true}`) }))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	defer server.Close()
	cfg := forwardedConfig(t, RESTForwardProtocol, server.URL)
	s, _ := testService(t, cfg, pluginExecutor(t, cfg))
	r := Request{Operation: "inspect_item", CustomTool: "inventory", Args: map[string]any{"sku": "fixture"}}
	a, err := s.Submit(context.Background(), "agent", "untrusted", r)
	require.NoError(t, err)
	require.Equal(t, "failed", a.State)
	require.Zero(t, calls.Load())
	ca := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	cfg.CustomTools[0].CACert = ca
	s, _ = testService(t, cfg, pluginExecutor(t, cfg))
	a, err = s.Submit(context.Background(), "agent", "trusted", r)
	require.NoError(t, err)
	require.Equal(t, "succeeded", a.State)
	require.EqualValues(t, 1, calls.Load())
}
