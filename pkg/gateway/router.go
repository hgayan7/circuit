package gateway

import (
	"context"
	"fmt"
)

// RouterExecutor routes action requests to the appropriate backend executor (GitHub, Workspace, Database, or Cloud).
type RouterExecutor struct {
	github       Executor
	workspaces   map[string]*ShellExecutor
	databases    map[string]*DatabaseExecutor
	environments map[string]*CloudExecutor
}

// NewRouterExecutor creates a composite router executor.
func NewRouterExecutor(github Executor, workspaces map[string]*ShellExecutor, databases map[string]*DatabaseExecutor, environments map[string]*CloudExecutor) *RouterExecutor {
	return &RouterExecutor{
		github:       github,
		workspaces:   workspaces,
		databases:    databases,
		environments: environments,
	}
}

func isCloudOperation(r Request) bool {
	switch r.Operation {
	case "deploy_service", "rollback_deployment", "restart_service", "get_deployment_status", "scale_service":
		return true
	default:
		return false
	}
}

func isDatabaseOperation(r Request) bool {
	switch r.Operation {
	case "query_sql", "exec_sql", "list_tables", "describe_table":
		return true
	default:
		return false
	}
}

func isWorkspaceOperation(r Request) bool {
	switch r.Operation {
	case "exec_cmd", "write_file", "delete_file", "list_dir":
		return true
	case "read_file":
		return r.Workspace != "" || (r.Repository == "" && r.Database == "" && r.Environment == "")
	default:
		return false
	}
}

func (r *RouterExecutor) Execute(ctx context.Context, req Request) Outcome {
	if isCloudOperation(req) {
		envKey := req.Environment
		if envKey == "" {
			envKey = "default"
		}
		executor, ok := r.environments[envKey]
		if !ok {
			return Outcome{Error: fmt.Sprintf("cloud environment %q is not configured on this gateway", envKey)}
		}
		return executor.Execute(ctx, req)
	}
	if isDatabaseOperation(req) {
		dbKey := req.Database
		if dbKey == "" {
			dbKey = "default"
		}
		executor, ok := r.databases[dbKey]
		if !ok {
			return Outcome{Error: fmt.Sprintf("database %q is not configured on this gateway", dbKey)}
		}
		return executor.Execute(ctx, req)
	}
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
