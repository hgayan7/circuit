package onboarding

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func testGateway(t *testing.T) (Connection, *gateway.Service) {
	t.Helper()
	dir := t.TempDir()
	s, err := Create(Options{Directory: filepath.Join(dir, "setup"), Integration: "workspace", Workspace: t.TempDir(), Port: 8643})
	require.NoError(t, err)
	cfg, err := gateway.LoadConfig(s.Config)
	require.NoError(t, err)
	store, err := gateway.OpenStore(s.State)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	service, err := gateway.NewService(cfg, store, &gateway.DemoExecutor{})
	require.NoError(t, err)
	tokens, err := gateway.LoadTokens(cfg)
	require.NoError(t, err)
	handler, err := gateway.NewHTTPHandler(service, tokens)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	ca := filepath.Join(dir, "test-ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
	connection := s.Connection
	connection.URL = server.URL
	connection.CACert = ca
	return connection, service
}
func TestBridgePreservesScopedToolsArgumentsAndRetryIdentity(t *testing.T) {
	c, _ := testGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	names, err := Verify(ctx, c)
	require.NoError(t, err)
	require.Contains(t, names, "file_read")
	require.NotContains(t, names, "github_merge_pr")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- Bridge(ctx, c, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture-agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	params := &mcp.CallToolParams{Name: "file_read", Arguments: map[string]any{"workspace": "workspace", "idempotency_key": "same-key", "args": map[string]any{"path": "README.md"}}}
	var original string
	for i := 0; i < 2; i++ {
		result, err := session.CallTool(ctx, params)
		require.NoError(t, err)
		require.False(t, result.IsError)
		var action gateway.Action
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &action))
		require.Equal(t, "succeeded", action.State)
		if i == 0 {
			original = action.ID
		} else {
			require.Equal(t, original, action.ID)
		}
	}
	require.NoError(t, session.Close())
	cancel()
	<-done
}
func TestClientRejectsUnsafeURLsAndCredentials(t *testing.T) {
	c, _ := testGateway(t)
	for _, value := range []string{"http://127.0.0.1:8643", "https://user:secret@example.com", "https://example.com/path", "https://example.com?token=secret"} {
		bad := c
		bad.URL = value
		_, err := Client(bad)
		require.Error(t, err)
	}
	bad := c
	bad.TokenFile = filepath.Join(t.TempDir(), "missing")
	_, err := Client(bad)
	require.Error(t, err)
	require.NoError(t, os.Chmod(c.TokenFile, 0644))
	_, err = Client(c)
	require.Error(t, err)
}
func TestClientNeverForwardsTokenAcrossOriginsOrRedirects(t *testing.T) {
	c, _ := testGateway(t)
	token, err := os.ReadFile(c.TokenFile)
	require.NoError(t, err)
	client, err := Client(c)
	require.NoError(t, err)
	req, err := http.NewRequest("GET", "https://unrelated.example/", nil)
	require.NoError(t, err)
	_, err = client.Do(req)
	require.ErrorContains(t, err, "another origin")
	redirect := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://unrelated.example/", 302) }))
	redirect.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	redirect.StartTLS()
	defer redirect.Close()
	c.URL = redirect.URL
	require.NoError(t, os.WriteFile(c.CACert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: redirect.Certificate().Raw}), 0600))
	client, err = Client(c)
	require.NoError(t, err)
	_, err = client.Get(c.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), "redirect")
	require.NotContains(t, err.Error(), strings.TrimSpace(string(token)))
}
