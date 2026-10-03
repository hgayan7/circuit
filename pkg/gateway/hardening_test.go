package gateway

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExplicitSimulationAndMissingCredentials(t *testing.T) {
	base := "name: test\nagents:\n  - id: agent\n    token_env: TOKEN\n    repositories: [acme/app]\n    actions: [get_pr]\n"
	for _, section := range []string{
		"environments:\n  - id: staging\n",
		"communications:\n  - id: chat\n",
		"payment_accounts:\n  - id: billing\n",
	} {
		_, err := ParseConfig(strings.NewReader(base + section))
		require.ErrorContains(t, err, "simulation-only")
		_, err = ParseConfig(strings.NewReader("simulation: true\n" + base + section))
		require.NoError(t, err)
	}
	_, err := NewDatabaseTarget("db", "postgres", "", false, 10, time.Second, nil, nil)
	require.Error(t, err)
	_, err = NewCustomToolTarget("tool", "", "", "POST", nil, nil, false, time.Second)
	require.Error(t, err)
}

func TestWorkspaceOverwriteAndMissingSymlinkParent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	ws, err := NewWorkspace("local", root, false, time.Second)
	require.NoError(t, err)
	defer ws.Close()
	ex := NewShellExecutor(ws)
	require.NoError(t, os.WriteFile(filepath.Join(root, "existing"), []byte("original"), 0600))
	out := ex.Execute(context.Background(), Request{Operation: "write_file", Args: map[string]any{"path": "existing", "content": "replacement"}})
	require.Equal(t, 409, out.Status)
	data, err := os.ReadFile(filepath.Join(root, "existing"))
	require.NoError(t, err)
	require.Equal(t, "original", string(data))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	out = ex.Execute(context.Background(), Request{Operation: "write_file", Args: map[string]any{"path": "escape/new/secret", "content": "escaped"}})
	require.NotEqual(t, 200, out.Status)
	_, err = os.Stat(filepath.Join(outside, "new"))
	require.True(t, os.IsNotExist(err))
}

func TestWorkspaceSymlinkSwapCannotEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "inside"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "sentinel"), []byte("outside"), 0600))
	ws, err := NewWorkspace("local", root, false, time.Second)
	require.NoError(t, err)
	defer ws.Close()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			default:
			}
			os.Remove(filepath.Join(root, "swap"))
			os.Symlink("inside", filepath.Join(root, "swap"))
			os.Remove(filepath.Join(root, "swap"))
			os.Symlink(outside, filepath.Join(root, "swap"))
		}
	}()
	defer func() { close(done); <-stopped }()
	ex := NewShellExecutor(ws)
	for i := 0; i < 100; i++ {
		ex.Execute(context.Background(), Request{Operation: "write_file", Args: map[string]any{"path": "swap/new", "content": "test", "overwrite": true}})
		out := ex.Execute(context.Background(), Request{Operation: "read_file", Args: map[string]any{"path": "swap/sentinel"}})
		require.NotEqual(t, 200, out.Status)
	}
	_, err = os.Stat(filepath.Join(outside, "new"))
	require.True(t, os.IsNotExist(err))
}

func TestShellCredentialIsolationAndReadOnly(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "must-not-inherit")
	t.Setenv("CIRCUIT_ADMIN_TOKEN", "must-not-inherit")
	ws, err := NewWorkspace("local", t.TempDir(), false, time.Second)
	require.NoError(t, err)
	defer ws.Close()
	out := NewShellExecutor(ws).Execute(context.Background(), Request{Operation: "exec_cmd", Args: map[string]any{"command": "env"}})
	require.Equal(t, 200, out.Status)
	require.NotContains(t, string(out.Body), "must-not-inherit")
	ws.ReadOnly = true
	out = NewShellExecutor(ws).Execute(context.Background(), Request{Operation: "exec_cmd", Args: map[string]any{"command": "touch unsafe"}})
	require.Equal(t, 403, out.Status)
}

func TestShellApprovalBypassesRemainPending(t *testing.T) {
	cfg, err := ParseConfig(strings.NewReader("name: shell\nworkspaces:\n - id: local\n   path: /tmp\nagents:\n - id: agent\n   token_env: TOKEN\n   workspaces: [local]\n   actions: [exec_cmd]\n"))
	require.NoError(t, err)
	svc, _ := testService(t, cfg, &countingExecutor{})
	for i, cmd := range []string{"env rm target", "sh -c 'rm target'", "python3 -c 'import os; os.remove(\"target\")'", "echo x > target", "x=rm; $x target", "git -C . reset --hard", "cat /etc/passwd"} {
		a, err := svc.Submit(context.Background(), "agent", fmtKey(i), Request{Operation: "exec_cmd", Workspace: "local", Args: map[string]any{"command": cmd}})
		require.NoError(t, err)
		require.Equal(t, "pending", a.State)
	}
}

func fmtKey(i int) string { return "key-" + time.Duration(i).String() }

func TestSQLNestedMutationsAndFunctions(t *testing.T) {
	for _, query := range []string{"WITH x AS (DELETE FROM users RETURNING *) SELECT * FROM x", "SELECT pg_advisory_lock(1)", "SELECT nextval('sequence')", "SELECT * FROM (SELECT * FROM users FOR UPDATE) x"} {
		a, err := AnalyzeSQL(query)
		require.NoError(t, err)
		require.False(t, a.IsReadOnly, query)
	}
	analysis, err := AnalyzeSQL("SELECT * FROM private.users")
	require.NoError(t, err)
	require.Contains(t, analysis.Tables, "private.users")
	require.Error(t, CheckTableAccess(analysis.Tables, []string{"users"}, nil))
	require.Error(t, CheckTableAccess(analysis.Tables, []string{"private.users"}, []string{"users"}))
}
