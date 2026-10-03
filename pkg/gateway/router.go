package gateway

import (
	"context"
	"fmt"
)

// RouterExecutor routes action requests to the appropriate backend executor (GitHub or Workspace).
type RouterExecutor struct {
	github     Executor
	workspaces map[string]*ShellExecutor
}

// NewRouterExecutor creates a composite router executor.
func NewRouterExecutor(github Executor, workspaces map[string]*ShellExecutor) *RouterExecutor {
	return &RouterExecutor{
		github:     github,
		workspaces: workspaces,
	}
}

func isWorkspaceOperation(r Request) bool {
	switch r.Operation {
	case "exec_cmd", "write_file", "delete_file", "list_dir":
		return true
	case "read_file":
		return r.Workspace != "" || r.Repository == ""
	default:
		return false
	}
}

func (r *RouterExecutor) Execute(ctx context.Context, req Request) Outcome {
	if isWorkspaceOperation(req) {
		wsKey := req.Workspace
		if wsKey == "" {
			wsKey = "default"
		}
		executor, ok := r.workspaces[wsKey]
		if !ok {
			return Outcome{Error: fmt.Sprintf("workspace %q is not configured on this gateway", wsKey)}
		}
		return executor.Execute(ctx, req)
	}
	if r.github != nil {
		return r.github.Execute(ctx, req)
	}
	return Outcome{Error: fmt.Sprintf("no backend executor configured for operation %q", req.Operation)}
}
