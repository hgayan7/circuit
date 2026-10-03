package onboarding

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hgayan7/circuit/pkg/gateway"
	"github.com/stretchr/testify/require"
)

func TestPresetsUseCoreScopeAndExactApprovals(t *testing.T) {
	for _, preset := range []string{"read-only", "pr-author", "pr-author-with-merge"} {
		t.Run(preset, func(t *testing.T) {
			cfg, err := BuildConfig(Options{Integration: "github", Preset: preset, Repository: "fixture/repo", AppID: 1, InstallationID: 1, KeyFile: "fixture.pem", Port: 8643}, t.TempDir())
			require.NoError(t, err)
			require.NoError(t, cfg.ValidateProduction())
			store, err := gateway.OpenStore(filepath.Join(t.TempDir(), "state.db"))
			require.NoError(t, err)
			defer store.Close()
			service, err := gateway.NewService(cfg, store, &gateway.DemoExecutor{})
			require.NoError(t, err)
			request := gateway.Request{Operation: "create_pr", Repository: "fixture/repo", Args: map[string]any{"title": "Fix", "head": "circuit/fix", "base": "main"}}
			action, err := service.Submit(context.Background(), "agent", "pr", request)
			require.NoError(t, err)
			if preset == "read-only" {
				require.Equal(t, "denied", action.State)
			} else {
				require.Equal(t, "pending", action.State)
			}
			request.Operation = "get_pr"
			request.Args = map[string]any{"number": 1}
			request.Repository = "other/repo"
			action, err = service.Submit(context.Background(), "agent", "outside", request)
			require.NoError(t, err)
			require.Equal(t, "denied", action.State)
		})
	}
}
func TestIntegrationPresetsAreExplicit(t *testing.T) {
	t.Setenv("FIXTURE_DSN", "fixture-secret-dsn")
	dir := t.TempDir()
	workspace := t.TempDir()
	for _, integration := range []string{"postgres", "workspace", "plugin"} {
		o := Options{Integration: integration, Preset: "read-only", Port: 8643, DSNEnv: "FIXTURE_DSN", Workspace: workspace, Endpoint: "http://127.0.0.1:9000/actions", PluginTokenFile: "fixture-token", PluginOperations: []string{"lookup", "modify"}, PluginReadOnly: []string{"lookup"}}
		cfg, err := BuildConfig(o, dir)
		require.NoError(t, err)
		require.Error(t, cfg.ValidateProduction())
		require.NotContains(t, cfg.Agents[0].Actions, "exec_cmd")
		if integration == "plugin" {
			require.Equal(t, []string{"lookup"}, cfg.Agents[0].Actions)
			require.Equal(t, gateway.PluginProtocol, cfg.CustomTools[0].Protocol)
		}
	}
	_, err := BuildConfig(Options{Integration: "cloud", Port: 8643}, dir)
	require.Error(t, err)
}
func TestCreatePrivateFilesAndNoProviderSecretsInExports(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "setup")
	o := Options{Directory: dir, Integration: "workspace", Workspace: t.TempDir(), Preset: "read-only", Port: 8643, Executable: "/fixture/circuit"}
	s, err := Create(o)
	require.NoError(t, err)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	tokens := map[string]bool{}
	for _, role := range []string{"admin", "reviewer", "observer", "agent"} {
		path := filepath.Join(dir, "secrets", role+"-token")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
		require.False(t, tokens[string(data)])
		tokens[string(data)] = true
	}
	for _, name := range []string{"mcp.json", "agent-connection.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		require.NotContains(t, string(data), "admin-token")
		require.NotContains(t, string(data), "reviewer-token")
		require.NotContains(t, string(data), "tls.key")
		for token := range tokens {
			require.NotContains(t, string(data), strings.TrimSpace(token))
		}
		var value any
		require.NoError(t, json.Unmarshal(data, &value))
	}
	_, err = Create(o)
	require.Error(t, err)
	loaded, err := Load(dir)
	require.NoError(t, err)
	require.Equal(t, *s, *loaded)
	_, err = Client(s.Connection)
	require.NoError(t, err)
	link := filepath.Join(parent, "linked-token")
	require.NoError(t, os.Symlink(s.Connection.TokenFile, link))
	_, err = privateFile(link)
	require.Error(t, err)
}
func TestCredentialsDoNotSilentlySimulate(t *testing.T) {
	t.Setenv("FIXTURE_DSN", "")
	dir := filepath.Join(t.TempDir(), "setup")
	_, err := Create(Options{Directory: dir, Integration: "postgres", DSNEnv: "FIXTURE_DSN", Port: 8643})
	require.Error(t, err)
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err))
}

func TestWorkspaceCannotExposeSetupSecretsThroughAliases(t *testing.T) {
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(workspace, alias))
	for _, path := range []string{filepath.Join(workspace, "setup"), filepath.Join(alias, "setup"), workspace} {
		_, err := BuildConfig(Options{Integration: "workspace", Workspace: workspace, Port: 8643}, path)
		require.ErrorContains(t, err, "outside the agent workspace")
	}
}
