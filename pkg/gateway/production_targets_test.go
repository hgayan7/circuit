package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProductionGovernedTargets(t *testing.T) {
	for _, protocol := range []string{RESTForwardProtocol, MCPForwardProtocol} {
		c := forwardedConfig(t, protocol, "https://tools.example.test")
		c.Operators = []OperatorConfig{{ID: "admin", Role: "admin", TokenEnv: "ADMIN_TOKEN"}}
		require.NoError(t, c.ValidateProduction())
		c.CustomTools[0].Protocol = ""
		require.Error(t, c.ValidateProduction())
		c.CustomTools[0].Protocol = protocol
		c.Workspaces = []WorkspaceConfig{{ID: "host", Path: "/tmp"}}
		require.Error(t, c.ValidateProduction())
		c.Workspaces = nil
		c.Simulation = true
		require.Error(t, c.ValidateProduction())
	}
}

func TestLegacyCustomTransportCannotChangeDestinationOrFollowRedirect(t *testing.T) {
	calls := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/unapproved", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	target, err := NewCustomToolTarget("legacy", "", origin.URL, "POST", nil, nil, false, time.Second)
	require.NoError(t, err)
	executor := NewCustomToolExecutor(target)
	for _, key := range []string{"method", "endpoint"} {
		out := executor.Execute(context.Background(), Request{Operation: "call_custom_tool", Args: map[string]any{key: "DELETE"}})
		require.Equal(t, 400, out.Status)
	}
	require.Zero(t, calls)
	out := executor.Execute(context.Background(), Request{Operation: "call_custom_tool", Args: map[string]any{}})
	require.Equal(t, 307, out.Status)
	require.NotEmpty(t, out.Error)
	require.Equal(t, 1, calls)
}
