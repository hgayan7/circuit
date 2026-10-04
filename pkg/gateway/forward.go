package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const MCPForwardProtocol = "mcp-streamable-http-v1"
const RESTForwardProtocol = "rest-routes-v1"

// DiscoverMCP produces a reviewable manifest, not an automatic permission grant.
func DiscoverMCP(ctx context.Context, endpoint, tokenFile, caCert string) (*CustomToolConfig, error) {
	c := CustomToolConfig{ID: "upstream", Protocol: MCPForwardProtocol, Endpoint: endpoint, Method: "POST", TokenFile: tokenFile, CACert: caCert, Operations: []string{"probe"}, MCPTools: map[string]ForwardedMCPTool{"probe": {Name: "probe", InputSchema: map[string]any{"type": "object"}}}}
	target := &CustomToolTarget{}
	if err := target.configureForward(c); err != nil {
		return nil, err
	}
	ctx, cancel := isolatedForwardContext(ctx, 30*time.Second)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "circuit-discovery", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: target.httpClient}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return nil, fmt.Errorf("cannot initialize configured MCP upstream")
	}
	defer session.Close()
	tools := map[string]ForwardedMCPTool{}
	cursor := ""
	complete := false
	for pages := 0; pages < 100; pages++ {
		list, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("cannot discover upstream tools")
		}
		for _, tool := range list.Tools {
			if _, exists := tools[tool.Name]; exists {
				return nil, fmt.Errorf("upstream returned duplicate tools")
			}
			data, err := json.Marshal(tool.InputSchema)
			var schema map[string]any
			if err != nil || json.Unmarshal(data, &schema) != nil {
				return nil, fmt.Errorf("invalid upstream input schema")
			}
			if _, err := resolvedInput(schema); err != nil {
				return nil, fmt.Errorf("upstream input schema cannot be safely pinned")
			}
			tools[tool.Name] = ForwardedMCPTool{Name: tool.Name, InputSchema: schema}
			if len(tools) > 1000 {
				return nil, fmt.Errorf("upstream exceeds discovery limit")
			}
		}
		if list.NextCursor == "" {
			complete = true
			break
		}
		if list.NextCursor == cursor {
			return nil, fmt.Errorf("invalid upstream discovery cursor")
		}
		cursor = list.NextCursor
	}
	if !complete || len(tools) == 0 {
		return nil, fmt.Errorf("empty or incomplete upstream discovery")
	}
	var names []string
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	c.Operations = nil
	c.MCPTools = map[string]ForwardedMCPTool{}
	for i, name := range names {
		alias := fmt.Sprintf("tool_%03d", i+1)
		c.Operations = append(c.Operations, alias)
		c.MCPTools[alias] = tools[name]
	}
	if err := validateForwardConfig(c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (h *HTTPHandler) addForwardedTools(server *mcp.Server, agent Agent, op string) bool {
	forward, legacy := false, false
	for _, target := range h.service.cfg.CustomTools {
		if !member(agent.CustomTools, target.ID) || !member(target.Operations, op) {
			continue
		}
		if target.Protocol != MCPForwardProtocol && target.Protocol != RESTForwardProtocol {
			legacy = true
			continue
		}
		forward = true
		target := target
		var input map[string]any
		if target.Protocol == MCPForwardProtocol {
			input = target.MCPTools[op].InputSchema
		} else {
			for _, route := range target.Routes {
				if route.Operation == op {
					input = route.InputSchema
				}
			}
		}
		description := fmt.Sprintf("Configured %s operation %s.", target.ID, op)
		if target.Protocol == MCPForwardProtocol {
			description += " Upstream tool: " + target.MCPTools[op].Name + ". " + target.MCPTools[op].Description
		}
		description += " Submit through Circuit; pending requires approval. Poll circuit_action_status and reuse the original idempotency key."
		server.AddTool(&mcp.Tool{Name: "forward_" + target.ID + "_" + op, Description: description, InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"idempotency_key", "args"}, "properties": map[string]any{"idempotency_key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "args": input}}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var in struct {
				Key  string         `json:"idempotency_key"`
				Args map[string]any `json:"args"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(req.Params.Arguments)))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&in) != nil {
				return nil, fmt.Errorf("invalid forwarded request envelope")
			}
			a, err := h.service.Submit(ctx, agent.ID, in.Key, Request{Operation: op, CustomTool: target.ID, Args: in.Args})
			if err != nil {
				return nil, err
			}
			return toolResult(a), nil
		})
	}
	return forward && !legacy
}

type ForwardedMCPTool struct {
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	InputSchema map[string]any `yaml:"input_schema" json:"input_schema"`
}

type RESTRoute struct {
	Operation   string         `yaml:"operation" json:"operation"`
	Method      string         `yaml:"method" json:"method"`
	Path        string         `yaml:"path" json:"path"`
	QueryParams []string       `yaml:"query_params,omitempty" json:"query_params,omitempty"`
	InputSchema map[string]any `yaml:"input_schema" json:"input_schema"`
}

func governedCustomProtocol(p string) bool {
	return p == PluginProtocol || p == MCPForwardProtocol || p == RESTForwardProtocol
}

// Preserve cancellation without forwarding inbound MCP session/auth metadata.
func isolatedForwardContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	stop := context.AfterFunc(parent, cancel)
	if parent.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func resolvedInput(schema map[string]any) (*jsonschema.Resolved, error) {
	if schema["type"] != "object" {
		return nil, fmt.Errorf("forwarded input schema must explicitly describe an object")
	}
	data, err := json.Marshal(schema)
	if err != nil || len(data) > 256<<10 {
		return nil, fmt.Errorf("invalid or oversized input schema")
	}
	var s jsonschema.Schema
	if json.Unmarshal(data, &s) != nil {
		return nil, fmt.Errorf("invalid input schema")
	}
	return s.Resolve(nil)
}

func validateForwardConfig(c CustomToolConfig) error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return fmt.Errorf("forwarding requires fixed HTTPS upstreams; HTTP is loopback-only")
	}
	if c.Method != "POST" || len(c.Headers) != 0 || (c.TokenEnv == "") == (c.TokenFile == "") || len(c.Operations) == 0 || c.Timeout() > 30*time.Second {
		return fmt.Errorf("forwarding requires no inline headers, one gateway credential source, declared operations, and timeout <=30s")
	}
	seen := map[string]bool{}
	for _, op := range c.Operations {
		if !identifier.MatchString(op) || operations[op] || seen[op] || len(c.ID)+len(op) > 110 {
			return fmt.Errorf("forwarded operations must be unique and cannot shadow built-ins")
		}
		seen[op] = true
	}
	for _, op := range c.ReadOnlyOperations {
		if !seen[op] {
			return fmt.Errorf("read-only operations must be explicitly declared")
		}
	}
	if c.Protocol == MCPForwardProtocol {
		if len(c.Routes) != 0 || len(c.MCPTools) != len(seen) {
			return fmt.Errorf("MCP forwarding requires one pinned tool per operation and no REST routes")
		}
		for op, tool := range c.MCPTools {
			if !seen[op] || !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`).MatchString(tool.Name) {
				return fmt.Errorf("invalid MCP tool binding")
			}
			if _, err := resolvedInput(tool.InputSchema); err != nil {
				return err
			}
		}
		return nil
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("REST upstream must be an origin; paths belong in fixed routes")
	}
	if len(c.MCPTools) != 0 || len(c.Routes) != len(seen) {
		return fmt.Errorf("REST forwarding requires one fixed route per operation and no MCP tools")
	}
	routes := map[string]bool{}
	for _, route := range c.Routes {
		if !seen[route.Operation] || routes[route.Operation] {
			return fmt.Errorf("REST routes must uniquely bind declared operations")
		}
		routes[route.Operation] = true
		if !member([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}, route.Method) {
			return fmt.Errorf("unsupported fixed REST method")
		}
		if route.Path == "" || !strings.HasPrefix(route.Path, "/") || strings.HasPrefix(route.Path, "//") || path.Clean(route.Path) != route.Path || strings.ContainsAny(route.Path, "%?#\\{}\r\n") {
			return fmt.Errorf("REST route requires a literal canonical path without templates, escapes, or queries")
		}
		if member(c.ReadOnlyOperations, route.Operation) && route.Method != "GET" && route.Method != "HEAD" {
			return fmt.Errorf("read-only REST operations require GET or HEAD; method alone never establishes safety")
		}
		if _, err := resolvedInput(route.InputSchema); err != nil {
			return err
		}
		query := map[string]bool{}
		for _, key := range route.QueryParams {
			if !identifier.MatchString(key) || query[key] || (route.Method != "GET" && route.Method != "HEAD") {
				return fmt.Errorf("query parameters must be unique declarations on GET/HEAD routes")
			}
			query[key] = true
		}
	}
	return nil
}

type forwardAuth struct {
	base          http.RoundTripper
	origin, token string
}

type boundedForwardBody struct {
	io.Reader
	io.Closer
}

func (a forwardAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != a.origin {
		return nil, fmt.Errorf("upstream origin mismatch")
	}
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+a.token)
	// Prevent HTTP transport replay of buffered POSTs on stale pooled connections.
	copy.GetBody = nil
	response, err := a.base.RoundTrip(copy)
	if err == nil {
		response.Body = boundedForwardBody{Reader: io.LimitReader(response.Body, (4<<20)+1), Closer: response.Body}
	}
	return response, err
}

func (t *CustomToolTarget) configureForward(c CustomToolConfig) error {
	if err := validateForwardConfig(c); err != nil {
		return err
	}
	token, err := credential(c.TokenEnv, c.TokenFile)
	if err != nil || len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("upstream requires a protected single-line credential of at least 32 characters")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if c.CACert != "" {
		data, err := os.ReadFile(c.CACert)
		pool := x509.NewCertPool()
		if err != nil || !pool.AppendCertsFromPEM(data) {
			return fmt.Errorf("invalid upstream CA file")
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	u, _ := url.Parse(c.Endpoint)
	t.httpClient = &http.Client{Timeout: c.Timeout(), Transport: forwardAuth{base: transport, origin: u.Scheme + "://" + u.Host, token: token}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Own the binding snapshot just as the service owns approved requests.
	data, _ := json.Marshal(c)
	var owned CustomToolConfig
	if err := json.Unmarshal(data, &owned); err != nil {
		return err
	}
	owned.timeout = c.Timeout()
	t.plugin = &owned
	t.pluginToken = token
	return nil
}

// Durable outcomes must be valid JSON, including text/empty REST responses.
// Redact the gateway-only upstream credential even if an upstream echoes it.
func forwardBody(data []byte, secret string) []byte {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if !json.Valid(data) || decoder.Decode(&value) != nil {
		value = map[string]any{"text": string(data)}
	}
	var redact func(any) any
	redact = func(v any) any {
		switch v := v.(type) {
		case string:
			return strings.ReplaceAll(v, secret, "[REDACTED]")
		case []any:
			for i := range v {
				v[i] = redact(v[i])
			}
			return v
		case map[string]any:
			out := map[string]any{}
			for k, val := range v {
				out[strings.ReplaceAll(k, secret, "[REDACTED]")] = redact(val)
			}
			return out
		default:
			return v
		}
	}
	encoded, _ := json.Marshal(redact(value))
	return encoded
}

func (e *CustomToolExecutor) executeForward(ctx context.Context, r Request) Outcome {
	c := e.target.plugin
	id, _ := ctx.Value(executionIDKey{}).(string)
	if id == "" || r.CustomTool != c.ID || !member(c.Operations, r.Operation) {
		return Outcome{Error: "Forwarding lacks approved action context or declared scope"}
	}
	ctx, cancel := isolatedForwardContext(ctx, c.Timeout())
	defer cancel()
	mutating := !member(c.ReadOnlyOperations, r.Operation)
	if c.Protocol == MCPForwardProtocol {
		return e.forwardMCP(ctx, r, mutating, id)
	}
	var route RESTRoute
	for _, candidate := range c.Routes {
		if candidate.Operation == r.Operation {
			route = candidate
			break
		}
	}
	schema, err := resolvedInput(route.InputSchema)
	if err != nil || schema.Validate(r.Args) != nil {
		return Outcome{Error: "REST arguments do not match the configured input schema"}
	}
	u, _ := url.Parse(c.Endpoint)
	u.Path = route.Path
	var body io.Reader
	if route.Method == "GET" || route.Method == "HEAD" {
		query := url.Values{}
		for k, v := range r.Args {
			value, ok := v.(string)
			if !member(route.QueryParams, k) || !ok {
				return Outcome{Error: "REST query parameters must be declared strings"}
			}
			query.Set(k, value)
		}
		u.RawQuery = query.Encode()
	} else {
		data, err := json.Marshal(r.Args)
		if err != nil {
			return Outcome{Error: "Cannot encode REST payload"}
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, route.Method, u.String(), body)
	if err != nil {
		return Outcome{Error: "Invalid fixed REST route"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", id)
	response, err := e.target.httpClient.Do(req)
	if err != nil {
		return Outcome{Error: "REST outcome unavailable", Uncertain: mutating}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || response.StatusCode < 200 || response.StatusCode >= 300 {
		return Outcome{Status: response.StatusCode, Error: "REST response cannot establish successful completion", Uncertain: mutating}
	}
	return Outcome{Status: response.StatusCode, Body: forwardBody(data, e.target.pluginToken)}
}

func (e *CustomToolExecutor) forwardMCP(ctx context.Context, r Request, mutating bool, id string) Outcome {
	c := e.target.plugin
	binding := c.MCPTools[r.Operation]
	schema, err := resolvedInput(binding.InputSchema)
	if err != nil || schema.Validate(r.Args) != nil {
		return Outcome{Error: "MCP arguments do not match the pinned input schema"}
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "circuit-governed-forwarder", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: c.Endpoint, HTTPClient: e.target.httpClient}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return Outcome{Error: "Cannot initialize configured MCP upstream"}
	}
	defer session.Close()
	cursor := ""
	found := false
	complete := false
	for pages := 0; pages < 100; pages++ {
		list, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return Outcome{Error: "Cannot verify configured MCP tool"}
		}
		for _, tool := range list.Tools {
			if tool.Name != binding.Name {
				continue
			}
			if found {
				return Outcome{Error: "Upstream returned duplicate tool bindings"}
			}
			actual, err := json.Marshal(tool.InputSchema)
			pinned, _ := json.Marshal(binding.InputSchema)
			var normalized any
			if err != nil || json.Unmarshal(actual, &normalized) != nil {
				return Outcome{Error: "Cannot verify upstream schema"}
			}
			actual, _ = json.Marshal(normalized)
			if !bytes.Equal(actual, pinned) {
				return Outcome{Error: "Upstream MCP schema changed; operator must review and reconfigure"}
			}
			found = true
		}
		if list.NextCursor == "" {
			complete = true
			break
		}
		if list.NextCursor == cursor {
			return Outcome{Error: "Invalid upstream discovery cursor"}
		}
		cursor = list.NextCursor
	}
	if !found || !complete {
		return Outcome{Error: "Configured MCP tool is absent or discovery exceeded its limit"}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: binding.Name, Arguments: r.Args, Meta: mcp.Meta{"circuit/action_id": id}})
	if err != nil {
		return Outcome{Error: "MCP outcome unavailable", Uncertain: mutating}
	}
	if result.IsError {
		return Outcome{Error: "MCP tool reported an error; inspect upstream before replaying a write", Uncertain: mutating}
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > 4<<20 {
		return Outcome{Error: "MCP response exceeds the durable outcome limit", Uncertain: mutating}
	}
	var envelope struct {
		Type string `json:"resultType"`
	}
	if json.Unmarshal(data, &envelope) != nil || (envelope.Type != "" && envelope.Type != "complete") || len(result.InputRequests) > 0 {
		return Outcome{Error: "MCP tool did not return a terminal outcome", Uncertain: mutating}
	}
	return Outcome{Status: 200, Body: forwardBody(data, e.target.pluginToken)}
}
