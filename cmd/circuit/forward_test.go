package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDiscoveryCLIExportsReviewManifestOnStdoutOnly(t *testing.T) {
	secret := "fixture-upstream-secret-at-least-32-characters"
	var calls atomic.Int32
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "lookup", InputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+secret, r.Header.Get("Authorization"))
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(token, []byte(secret), 0600))
	parent := &cobra.Command{Use: "gateway"}
	addGatewayForwarding(parent)
	var out, warnings bytes.Buffer
	parent.SetOut(&out)
	parent.SetErr(&warnings)
	parent.SetArgs([]string{"discover-mcp", "--endpoint", server.URL, "--token-file", token})
	require.NoError(t, parent.Execute())
	var manifest struct {
		CustomTools []gateway.CustomToolConfig `yaml:"custom_tools"`
	}
	require.NoError(t, yaml.Unmarshal(out.Bytes(), &manifest))
	require.Len(t, manifest.CustomTools, 1)
	require.Equal(t, gateway.MCPForwardProtocol, manifest.CustomTools[0].Protocol)
	require.Empty(t, manifest.CustomTools[0].ReadOnlyOperations)
	require.True(t, strings.Contains(warnings.String(), "No agent permissions granted"))
	require.NotContains(t, out.String()+warnings.String(), secret)
	require.Zero(t, calls.Load())
}
