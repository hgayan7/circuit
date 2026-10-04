package onboarding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestMiddlewareSetupImportsOnlyReviewedTargets(t *testing.T) {
	for _, protocol := range []string{gateway.MCPForwardProtocol, gateway.RESTForwardProtocol} {
		parent := t.TempDir()
		secret := "fixture-upstream-token-at-least-32-characters"
		require.NoError(t, os.WriteFile(filepath.Join(parent, "upstream-token"), []byte(secret), 0600))
		schema := map[string]any{"type": "object", "additionalProperties": false}
		target := gateway.CustomToolConfig{ID: "upstream", Protocol: protocol, Endpoint: "http://127.0.0.1:9000", TokenFile: "upstream-token", Operations: []string{"lookup", "change"}}
		if protocol == gateway.MCPForwardProtocol {
			target.MCPTools = map[string]gateway.ForwardedMCPTool{"lookup": {Name: "lookup", InputSchema: schema}, "change": {Name: "change", InputSchema: schema}}
		} else {
			target.Routes = []gateway.RESTRoute{{Operation: "lookup", Method: "GET", Path: "/items", InputSchema: schema}, {Operation: "change", Method: "POST", Path: "/items", InputSchema: schema}}
		}
		data, err := yaml.Marshal(map[string]any{"custom_tools": []gateway.CustomToolConfig{target}})
		require.NoError(t, err)
		manifest := filepath.Join(parent, "manifest.yaml")
		require.NoError(t, os.WriteFile(manifest, data, 0600))
		o := Options{Directory: filepath.Join(parent, "operator"), Integration: "middleware", Preset: "read-only", UpstreamManifest: manifest, Port: 8643}
		_, err = Create(o)
		require.ErrorContains(t, err, "no explicitly classified read-only")
		o.Preset = "review-writes"
		s, err := Create(o)
		require.NoError(t, err)
		cfg, err := gateway.LoadConfig(s.Config)
		require.NoError(t, err)
		require.Equal(t, []string{"lookup", "change"}, cfg.Agents[0].Actions)
		require.Equal(t, filepath.Join(parent, "upstream-token"), cfg.CustomTools[0].TokenFile)
		require.Empty(t, cfg.CustomTools[0].ReadOnlyOperations)
		require.Equal(t, []string{"lookup", "change"}, cfg.Rules[0].Actions)
		require.NoError(t, CheckCredentials(cfg))
		require.NoError(t, CheckTokens(cfg))
		require.NoError(t, cfg.ValidateProduction())
		for _, name := range []string{"mcp.json", "agent-connection.json", "gateway.yaml"} {
			data, err := os.ReadFile(filepath.Join(o.Directory, name))
			require.NoError(t, err)
			require.NotContains(t, string(data), secret)
			if name != "gateway.yaml" {
				require.NotContains(t, string(data), "upstream-token")
			}
		}
	}
}

func TestUpstreamManifestCannotGrantAgentPermissions(t *testing.T) {
	for _, contents := range []string{
		"custom_tools: []\n",
		"custom_tools: []\nagents: [{id: attacker}]\n",
		"custom_tools:\n- id: x\n  protocol: rest-routes-v1\n  token_file: token\n---\ncustom_tools: []\n",
		"custom_tools:\n- id: x\n  protocol: circuit-plugin-v1\n  token_file: token\n",
		"custom_tools:\n- id: x\n  protocol: rest-routes-v1\n  token_env: UNTRUSTED_TOKEN\n",
	} {
		path := filepath.Join(t.TempDir(), "manifest.yaml")
		require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
		_, err := loadUpstreamManifest(path)
		require.Error(t, err)
	}
	_, err := loadUpstreamManifest(strings.Repeat("x", 50))
	require.Error(t, err)
}
