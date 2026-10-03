package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func sandboxFixture(t *testing.T) SandboxOptions {
	t.Helper()
	s, err := Create(Options{Directory: filepath.Join(t.TempDir(), "operator"), Integration: "workspace", Workspace: t.TempDir(), Port: 8643})
	require.NoError(t, err)
	return SandboxOptions{Image: "fixture/agent@sha256:abc", Network: "fixture-internal", Workspace: t.TempDir(), GatewayURL: "https://gateway:8443", Connection: s.Connection, Command: []string{"agent", "--task", "two words"}}
}

func TestSandboxRestrictedPlan(t *testing.T) {
	o := sandboxFixture(t)
	args, err := SandboxArgs(o, "/private/staged")
	require.NoError(t, err)
	plan := strings.Join(args, " ")
	for _, restriction := range []string{"--interactive", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "--pids-limit 64", "--memory 512m", "--cpus 1", "--user 10001:10001", "dst=/workspace,readonly"} {
		require.Contains(t, plan, restriction)
	}
	for _, forbidden := range []string{"docker.sock", "--privileged", "--network host", "admin-token", "github-app.pem", "--env-file"} {
		require.NotContains(t, plan, forbidden)
	}
	require.Equal(t, []string{"--task", "two words"}, args[len(args)-2:])
	o.Writable = true
	args, err = SandboxArgs(o, "/private/staged")
	require.NoError(t, err)
	require.NotContains(t, strings.Join(args, " "), "dst=/workspace,readonly")
	o.Runtime = "runsc"
	args, err = SandboxArgs(o, "/private/staged")
	require.NoError(t, err)
	require.Contains(t, strings.Join(args, " "), "--runtime runsc")
}

func TestSandboxRejectsUnsafeInputs(t *testing.T) {
	for _, change := range []func(*SandboxOptions){
		func(o *SandboxOptions) { o.Image = "--privileged" },
		func(o *SandboxOptions) { o.Network = "" },
		func(o *SandboxOptions) { o.Command = []string{""} },
		func(o *SandboxOptions) { o.Runtime = "other" },
		func(o *SandboxOptions) { o.GatewayURL = "http://gateway:8443" },
		func(o *SandboxOptions) { o.GatewayURL = "https://secret@gateway:8443" },
		func(o *SandboxOptions) { o.Workspace = "/" },
		func(o *SandboxOptions) { o.Workspace = filepath.Dir(o.Connection.TokenFile) },
	} {
		o := sandboxFixture(t)
		change(&o)
		_, err := SandboxArgs(o, "/private/staged")
		require.Error(t, err)
	}
}

func TestSandboxStagesOnlyScopedConnection(t *testing.T) {
	o := sandboxFixture(t)
	dir, err := StageSandbox(o)
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, files, 3)
	for _, file := range files {
		info, err := file.Info()
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0444), info.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	require.NoError(t, err)
	token, err := os.ReadFile(o.Connection.TokenFile)
	require.NoError(t, err)
	require.NotContains(t, string(data), strings.TrimSpace(string(token)))
	require.NotContains(t, string(data), "admin-token")
	require.Contains(t, string(data), "--mounted-token")
	var config any
	require.NoError(t, json.Unmarshal(data, &config))
	_, err = mountedToken(o.Connection.TokenFile)
	require.Error(t, err, "ordinary writable host files must not qualify as read-only mounts")
}
