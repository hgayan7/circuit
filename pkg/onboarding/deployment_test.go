package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func deploymentFixture(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	token := filepath.Join(parent, "provider-token")
	require.NoError(t, os.WriteFile(token, []byte("provider-token-distinct-and-over-32-characters"), 0600))
	manifest := filepath.Join(parent, "upstream.json")
	require.NoError(t, os.WriteFile(manifest, []byte(`{"custom_tools":[{"id":"inventory","protocol":"rest-routes-v1","endpoint":"https://inventory.example.test","token_file":"provider-token","operations":["reserve"],"routes":[{"operation":"reserve","method":"POST","path":"/reserve","input_schema":{"type":"object"}}]}]}`), 0600))
	dir := filepath.Join(parent, "operator")
	_, err := Create(Options{Directory: dir, Integration: "middleware", Preset: "review-writes", UpstreamManifest: manifest, Port: 8643})
	require.NoError(t, err)
	return dir
}

func TestDeploymentSecretSeparationAndReproduciblePlan(t *testing.T) {
	dir := deploymentFixture(t)
	d, err := PrepareDeployment(dir, "circuit-gateway:fixture")
	require.NoError(t, err)
	same, err := PrepareDeployment(dir, "circuit-gateway:fixture")
	require.NoError(t, err)
	require.Equal(t, d, same)
	_, err = PrepareDeployment(dir, "different:fixture")
	require.ErrorContains(t, err, "configuration changed")
	data, err := os.ReadFile(d.Compose)
	require.NoError(t, err)
	require.NotContains(t, string(data), "provider-token-distinct")
	var compose map[string]any
	require.NoError(t, yaml.Unmarshal(data, &compose))
	serialized, _ := json.Marshal(compose)
	require.Contains(t, string(serialized), `"internal":true`)
	require.Contains(t, string(serialized), `"--production"`)
	require.Contains(t, string(serialized), `"read_only":true`)
	info, err := os.Stat(filepath.Join(dir, "deployment"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	data, err = os.ReadFile(filepath.Join(dir, "deployment", "gateway.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "/run/circuit/upstream-0.token")
	require.NotContains(t, string(data), "provider-token-distinct")
}

func TestDeploymentDoesNotDiscardHostActionHistory(t *testing.T) {
	dir := deploymentFixture(t)
	s, err := Load(dir)
	require.NoError(t, err)
	store, err := gateway.OpenStore(s.State)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	_, err = PrepareDeployment(dir, "gateway:fixture")
	require.ErrorContains(t, err, "refusing to start a second empty action store")
}

func TestBoundaryDoesNotGiveAgentNetworkAdministration(t *testing.T) {
	d := &Deployment{Project: "fixture", Network: "fixture_agents"}
	args, err := BoundaryArgs(d, "circuit-boundary:fixture", "fixture-boundary", "172.28.0.2")
	require.NoError(t, err)
	require.Contains(t, args, "NET_ADMIN")
	for _, ip := range []string{"not-ip", "::1", "172.28.0.2;evil"} {
		_, err := BoundaryArgs(d, "boundary:fixture", "fixture", ip)
		require.Error(t, err)
	}
	workspace := t.TempDir()
	secret := filepath.Join(t.TempDir(), "agent")
	require.NoError(t, os.WriteFile(secret, []byte("fixture"), 0600))
	o := SandboxOptions{Image: "agent:fixture", Network: d.Network, Workspace: workspace, GatewayURL: "https://gateway:8443", Command: []string{"agent"}, Connection: Connection{TokenFile: secret, CACert: secret}}
	args, err = NamespacedArgs(o, "/private/staged", "fixture-boundary")
	require.NoError(t, err)
	require.Contains(t, args, "container:fixture-boundary")
	require.Contains(t, args, "runc")
	require.NotContains(t, strings.Join(args, " "), "NET_ADMIN")
	require.Contains(t, strings.Join(args, " "), "dst=/etc/hosts,readonly")
	o.Writable = true
	_, err = NamespacedArgs(o, "/private/staged", "fixture-boundary")
	require.Error(t, err)
	o.Writable = false
	o.Runtime = "runsc"
	_, err = NamespacedArgs(o, "/private/staged", "fixture-boundary")
	require.Error(t, err)
}

func TestMiddlewareMultipleTargetsKeepScopesAndApproval(t *testing.T) {
	dir := deploymentFixture(t)
	cfg, err := gateway.LoadConfig(filepath.Join(dir, "gateway.yaml"))
	require.NoError(t, err)
	model := cfg.CustomTools[0]
	model.ID = "model"
	model.Protocol = gateway.MCPForwardProtocol
	model.Operations = []string{"model_complete"}
	model.ReadOnlyOperations = []string{"model_complete"}
	model.Routes = nil
	model.MCPTools = map[string]gateway.ForwardedMCPTool{"model_complete": {Name: "complete", InputSchema: map[string]any{"type": "object"}}}
	manifest := filepath.Join(t.TempDir(), "targets.yaml")
	data, err := yaml.Marshal(map[string]any{"custom_tools": []gateway.CustomToolConfig{cfg.CustomTools[0], model}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifest, data, 0600))
	o := Options{Integration: "middleware", Preset: "review-writes", UpstreamManifest: manifest, Port: 8643}
	cfg, err = BuildConfig(o, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, []string{"inventory", "model"}, cfg.Agents[0].CustomTools)
	require.ElementsMatch(t, []string{"reserve", "model_complete"}, cfg.Agents[0].Actions)
	require.Equal(t, []string{"reserve"}, cfg.Rules[0].Actions)
	o.Preset = "read-only"
	cfg, err = BuildConfig(o, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, []string{"model"}, cfg.Agents[0].CustomTools)
	require.Equal(t, []string{"model_complete"}, cfg.Agents[0].Actions)
}

func TestIsolatedWorkspaceRejectsKnownCredentials(t *testing.T) {
	dir := deploymentFixture(t)
	s, err := Load(dir)
	require.NoError(t, err)
	require.NoError(t, CheckWorkspaceIsolation(s, t.TempDir()))
	require.Error(t, CheckWorkspaceIsolation(s, dir))
	c, err := gateway.LoadConfig(s.Config)
	require.NoError(t, err)
	require.Error(t, CheckWorkspaceIsolation(s, filepath.Dir(c.CustomTools[0].TokenFile)))
}
