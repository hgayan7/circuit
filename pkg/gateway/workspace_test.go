package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceResolvePathSandboxing(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := NewWorkspace("test-ws", tmpDir, false, 30*time.Second)
	require.NoError(t, err)

	// Valid relative paths
	p, err := ws.ResolvePath("file.txt")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(ws.Root, "file.txt"), p)

	p, err = ws.ResolvePath("sub/dir/test.go")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(ws.Root, "sub/dir/test.go"), p)

	p, err = ws.ResolvePath(".")
	require.NoError(t, err)
	require.Equal(t, ws.Root, p)

	// Path traversal outside root
	_, err = ws.ResolvePath("../outside.txt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "forbidden")

	_, err = ws.ResolvePath("sub/../../outside.txt")
	require.Error(t, err)

	// Absolute paths
	_, err = ws.ResolvePath("/etc/passwd")
	require.Error(t, err)
	require.Contains(t, err.Error(), "relative")

	// Symlink escape outside root
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0600)

	symlinkPath := filepath.Join(ws.Root, "escape_link")
	err = os.Symlink(outsideFile, symlinkPath)
	require.NoError(t, err)

	_, err = ws.ResolvePath("escape_link")
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace root")
}

func TestDestructiveCommandDetection(t *testing.T) {
	safeCmds := []string{
		"echo hello",
		"ls -la",
		"cat README.md",
		"go test ./...",
		"git status",
		"python3 script.py",
		"grep -rn 'TODO' .",
	}
	for _, cmd := range safeCmds {
		isDestructive, reason := IsDestructiveCommand(cmd)
		require.False(t, isDestructive, "expected %q to be safe, got: %s", cmd, reason)
	}

	destructiveCmds := []struct {
		cmd    string
		substr string
	}{
		{"rm file.txt", "rm"},
		{"rm -rf node_modules", "rm"},
		{"git reset --hard HEAD~1", "reset"},
		{"git clean -fd", "clean"},
		{"git push origin main", "push"},
		{"chmod 777 run.sh", "chmod"},
		{"sudo apt update", "sudo"},
		{"kill -9 1234", "kill"},
	}
	for _, tc := range destructiveCmds {
		isDestructive, reason := IsDestructiveCommand(tc.cmd)
		require.True(t, isDestructive, "expected %q to be destructive", tc.cmd)
		require.Contains(t, reason, tc.substr)
	}
}

func TestShellExecutorFileOperations(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := NewWorkspace("default", tmpDir, false, 10*time.Second)
	require.NoError(t, err)
	exec := NewShellExecutor(ws)

	// 1. write_file plain text
	out := exec.Execute(context.Background(), Request{
		Operation: "write_file",
		Workspace: "default",
		Args: map[string]any{
			"path":    "hello.txt",
			"content": "Hello Circuit!",
		},
	})
	require.Equal(t, 200, out.Status)
	require.Empty(t, out.Error)

	// 2. read_file
	out = exec.Execute(context.Background(), Request{
		Operation: "read_file",
		Workspace: "default",
		Args: map[string]any{
			"path": "hello.txt",
		},
	})
	require.Equal(t, 200, out.Status)
	var readRes map[string]any
	require.NoError(t, json.Unmarshal(out.Body, &readRes))
	require.Equal(t, "Hello Circuit!", readRes["content"])

	// 3. write_file with base64 encoding and nested directory creation
	b64Content := base64.StdEncoding.EncodeToString([]byte("binary content 12345"))
	out = exec.Execute(context.Background(), Request{
		Operation: "write_file",
		Workspace: "default",
		Args: map[string]any{
			"path":     "nested/dir/bin.dat",
			"content":  b64Content,
			"encoding": "base64",
		},
	})
	require.Equal(t, 200, out.Status)

	// 4. list_dir
	out = exec.Execute(context.Background(), Request{
		Operation: "list_dir",
		Workspace: "default",
		Args: map[string]any{
			"path": ".",
		},
	})
	require.Equal(t, 200, out.Status)
	var listRes struct {
		Entries []DirEntryInfo `json:"entries"`
		Count   int            `json:"count"`
	}
	require.NoError(t, json.Unmarshal(out.Body, &listRes))
	require.GreaterOrEqual(t, listRes.Count, 2)

	// 5. delete_file
	out = exec.Execute(context.Background(), Request{
		Operation: "delete_file",
		Workspace: "default",
		Args: map[string]any{
			"path": "hello.txt",
		},
	})
	require.Equal(t, 200, out.Status)

	// Verify file is gone
	out = exec.Execute(context.Background(), Request{
		Operation: "read_file",
		Workspace: "default",
		Args: map[string]any{
			"path": "hello.txt",
		},
	})
	require.Equal(t, 404, out.Status)

	// 6. ReadOnly workspace rejects writes and deletes
	roWs, err := NewWorkspace("ro", tmpDir, true, 10*time.Second)
	require.NoError(t, err)
	roExec := NewShellExecutor(roWs)

	out = roExec.Execute(context.Background(), Request{
		Operation: "write_file",
		Workspace: "ro",
		Args:      map[string]any{"path": "denied.txt", "content": "test"},
	})
	require.Equal(t, 403, out.Status)
	require.Contains(t, out.Error, "read-only")

	out = roExec.Execute(context.Background(), Request{
		Operation: "delete_file",
		Workspace: "ro",
		Args:      map[string]any{"path": "nested/dir/bin.dat"},
	})
	require.Equal(t, 403, out.Status)
}

func TestShellExecutorCommandExecutionAndTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := NewWorkspace("cmd-ws", tmpDir, false, 2*time.Second)
	require.NoError(t, err)
	exec := NewShellExecutor(ws)

	// 1. Successful execution
	out := exec.Execute(context.Background(), Request{
		Operation: "exec_cmd",
		Workspace: "cmd-ws",
		Args: map[string]any{
			"command": "echo 'running inside workspace'",
		},
	})
	require.Equal(t, 200, out.Status)
	var cmdRes struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	require.NoError(t, json.Unmarshal(out.Body, &cmdRes))
	require.Equal(t, 0, cmdRes.ExitCode)
	require.Contains(t, cmdRes.Stdout, "running inside workspace")

	// 2. Command with non-zero exit code
	out = exec.Execute(context.Background(), Request{
		Operation: "exec_cmd",
		Workspace: "cmd-ws",
		Args: map[string]any{
			"command": "exit 37",
		},
	})
	require.Equal(t, 400, out.Status)
	require.NoError(t, json.Unmarshal(out.Body, &cmdRes))
	require.Equal(t, 37, cmdRes.ExitCode)

	// 3. Command timeout enforcement
	out = exec.Execute(context.Background(), Request{
		Operation: "exec_cmd",
		Workspace: "cmd-ws",
		Args: map[string]any{
			"command":     "sleep 10",
			"timeout_sec": float64(1),
		},
	})
	require.Equal(t, 504, out.Status)
	require.Contains(t, out.Error, "timed out")
}

func TestWorkspaceGatewayApprovalAndLimits(t *testing.T) {
	tmpDir := t.TempDir()
	yamlCfg := fmt.Sprintf(`name: workspace-gateway
workspaces:
- id: local
  path: %s
  max_timeout: 10s
agents:
- id: ws-agent
  token_env: AGENT_TOKEN
  workspaces: [local]
  actions: [exec_cmd, write_file, delete_file, read_file, list_dir]
limits:
- id: cmd-quota
  actions: [exec_cmd]
  scope: agent_workspace
  window: 1h
  max_calls: 2
`, tmpDir)

	cfg, err := ParseConfig(strings.NewReader(yamlCfg))
	require.NoError(t, err)

	ws, err := NewWorkspace("local", tmpDir, false, 10*time.Second)
	require.NoError(t, err)
	router := NewRouterExecutor(nil, map[string]*ShellExecutor{"local": NewShellExecutor(ws)}, nil, nil, nil, nil, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)

	// 1. Shell commands reserve quota and wait for exact approval.
	a, err := svc.Submit(context.Background(), "ws-agent", "key-1", Request{
		Operation: "exec_cmd",
		Workspace: "local",
		Args:      map[string]any{"command": "echo 'safe'"},
	})
	require.NoError(t, err)
	require.Equal(t, "pending", a.State)
	a, err = svc.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "succeeded", a.State, "denial reason: %s", a.Reason)

	// 2. Destructive command requires approval
	aDestructive, err := svc.Submit(context.Background(), "ws-agent", "key-2", Request{
		Operation: "exec_cmd",
		Workspace: "local",
		Args:      map[string]any{"command": "rm -rf some_dir"},
	})
	require.NoError(t, err)
	require.Equal(t, "pending", aDestructive.State)
	require.Contains(t, aDestructive.Reason, "Arbitrary shell execution requires operator approval")

	// Rejecting the pending destructive action releases its reserved budget quota
	rejected, err := svc.Decide(context.Background(), aDestructive.ID, aDestructive.Digest, "reject")
	require.NoError(t, err)
	require.Equal(t, "rejected", rejected.State)

	// 3. Deleting file requires operator approval
	aDel, err := svc.Submit(context.Background(), "ws-agent", "key-3", Request{
		Operation: "delete_file",
		Workspace: "local",
		Args:      map[string]any{"path": "target.txt"},
	})
	require.NoError(t, err)
	require.Equal(t, "pending", aDel.State)
	require.Equal(t, "Deleting files requires operator approval", aDel.Reason)

	// 4. Overwriting file requires operator approval
	aOverwrite, err := svc.Submit(context.Background(), "ws-agent", "key-4", Request{
		Operation: "write_file",
		Workspace: "local",
		Args:      map[string]any{"path": "target.txt", "content": "new", "overwrite": true},
	})
	require.NoError(t, err)
	require.Equal(t, "pending", aOverwrite.State)
	require.Equal(t, "Overwriting existing files requires operator approval", aOverwrite.Reason)

	// 5. Normal write executes immediately
	aWrite, err := svc.Submit(context.Background(), "ws-agent", "key-5", Request{
		Operation: "write_file",
		Workspace: "local",
		Args:      map[string]any{"path": "new_file.txt", "content": "fresh content"},
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", aWrite.State)

	// 6. Second safe command consumes remaining exec_cmd quota (2/2)
	a, err = svc.Submit(context.Background(), "ws-agent", "key-6", Request{
		Operation: "exec_cmd",
		Workspace: "local",
		Args:      map[string]any{"command": "echo 'safe 2'"},
	})
	require.NoError(t, err)
	require.Equal(t, "pending", a.State)
	a, err = svc.Decide(context.Background(), a.ID, a.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "succeeded", a.State)

	// 7. Third safe command exceeds quota -> denied!
	a, err = svc.Submit(context.Background(), "ws-agent", "key-7", Request{
		Operation: "exec_cmd",
		Workspace: "local",
		Args:      map[string]any{"command": "echo 'safe 3'"},
	})
	require.NoError(t, err)
	require.Equal(t, "denied", a.State)
	require.Contains(t, a.Reason, "Action budget cmd-quota exhausted")
}

func TestWorkspaceMCPIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	yamlCfg := fmt.Sprintf(`name: mcp-ws-gateway
workspaces:
- id: default
  path: %s
agents:
- id: mcp-agent
  token_env: AGENT_TOKEN
  workspaces: [default]
  actions: [exec_cmd, write_file, read_file]
`, tmpDir)

	cfg, err := ParseConfig(strings.NewReader(yamlCfg))
	require.NoError(t, err)

	ws, err := NewWorkspace("default", tmpDir, false, 10*time.Second)
	require.NoError(t, err)
	router := NewRouterExecutor(nil, map[string]*ShellExecutor{"default": NewShellExecutor(ws)}, nil, nil, nil, nil, nil)

	storeFile := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(storeFile)
	require.NoError(t, err)
	defer store.Close()

	svc, err := NewService(cfg, store, router)
	require.NoError(t, err)

	tokens := Tokens{
		Admin:  adminToken,
		Agents: map[string]string{"mcp-agent": agentToken},
	}
	handler, err := NewHTTPHandler(svc, tokens)
	require.NoError(t, err)

	server := httptest.NewServer(handler)
	defer server.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "workspace-test-client", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: authTransport{token: agentToken}},
	}, nil)
	require.NoError(t, err)
	defer session.Close()

	// Verify tools list includes shell and file tools
	toolList, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	toolMap := map[string]bool{}
	for _, tool := range toolList.Tools {
		toolMap[tool.Name] = true
	}
	require.True(t, toolMap["shell_exec_cmd"])
	require.True(t, toolMap["file_write"])
	require.True(t, toolMap["file_read"])

	// Call file_write via MCP
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "file_write",
		Arguments: map[string]any{
			"idempotency_key": "mcp-file-1",
			"args": map[string]any{
				"path":    "mcp_created.txt",
				"content": "Created via official MCP SDK",
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)

	// Call shell_exec_cmd via MCP
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "shell_exec_cmd",
		Arguments: map[string]any{
			"idempotency_key": "mcp-cmd-1",
			"args": map[string]any{
				"command": "cat mcp_created.txt",
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	var pending Action
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &pending))
	require.Equal(t, "pending", pending.State)
	approved, err := svc.Decide(context.Background(), pending.ID, pending.Digest, "approve")
	require.NoError(t, err)
	require.Equal(t, "succeeded", approved.State)
	require.Contains(t, string(approved.Outcome.Body), "Created via official MCP SDK")
}
