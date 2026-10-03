package onboarding

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type authenticated struct {
	base          http.RoundTripper
	origin, token string
}

func (a authenticated) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != a.origin {
		return nil, fmt.Errorf("refusing to send agent credential to another origin")
	}
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(copy)
}
func Client(c Connection) (*http.Client, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("gateway URL must be an HTTPS origin without credentials, query, or path")
	}
	data, err := privateFile(c.TokenFile)
	if c.MountedToken {
		data, err = mountedToken(c.TokenFile)
	}
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("agent token must have at least 32 characters and no newlines")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if c.CACert != "" {
		data, err := os.ReadFile(c.CACert)
		if err != nil {
			return nil, fmt.Errorf("trusted CA file unavailable")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("trusted CA file is invalid")
		}
		tlsConfig.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: authenticated{base: transport, origin: u.Scheme + "://" + u.Host, token: token}, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("gateway redirects are not allowed") }}, nil
}
func Connect(ctx context.Context, c Connection) (*mcp.ClientSession, error) {
	client, err := Client(c)
	if err != nil {
		return nil, err
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "circuit-agent-connector", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: strings.TrimRight(c.URL, "/") + "/mcp", HTTPClient: client}, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to scoped MCP endpoint; check that Circuit is running, the CA is trusted, and the agent token is current")
	}
	return session, nil
}

// Bridge forwards schemas and arguments without creating a second policy engine.
func Bridge(ctx context.Context, c Connection, transport mcp.Transport) error {
	upstream, err := Connect(ctx, c)
	if err != nil {
		return err
	}
	defer upstream.Close()
	server := mcp.NewServer(&mcp.Implementation{Name: "circuit-connector", Version: "1"}, nil)
	cursor := ""
	for {
		tools, err := upstream.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("cannot discover permitted tools")
		}
		for _, tool := range tools.Tools {
			tool := tool
			server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				result, err := upstream.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: req.Params.Arguments})
				if err != nil {
					return nil, fmt.Errorf("gateway tool call unavailable; inspect action status before retrying and reuse the original idempotency key")
				}
				return result, nil
			})
		}
		cursor = tools.NextCursor
		if cursor == "" {
			break
		}
	}
	return server.Run(ctx, transport)
}
func Verify(ctx context.Context, c Connection) ([]string, error) {
	client, err := Client(c)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.URL, "/")+"/readyz", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway unreachable or TLS verification failed; run circuit start and check the trusted CA")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("gateway is not ready; inspect storage, drain, or restore reconciliation status")
	}
	session, err := Connect(ctx, c)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot discover permitted MCP tools")
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	return names, nil
}
