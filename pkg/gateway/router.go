package gateway

import (
	"context"
	"encoding/json"
	"fmt"
)

func simulationOutcome(out Outcome) Outcome {
	if len(out.Body) > 0 {
		var body map[string]any
		if json.Unmarshal(out.Body, &body) == nil {
			body["simulated"] = true
			out.Body, _ = json.Marshal(body)
		}
	}
	return out
}

// RouterExecutor routes action requests to the appropriate backend executor (GitHub, Workspace, Database, Cloud, Communication, Payment, or Custom Tools).
type RouterExecutor struct {
	github       Executor
	workspaces   map[string]*ShellExecutor
	databases    map[string]*DatabaseExecutor
	environments map[string]*CloudExecutor
	comms        map[string]*CommExecutor
	payments     map[string]*PaymentExecutor
	customTools  map[string]*CustomToolExecutor
}

// NewRouterExecutor creates a composite router executor.
func NewRouterExecutor(github Executor, workspaces map[string]*ShellExecutor, databases map[string]*DatabaseExecutor, environments map[string]*CloudExecutor, comms map[string]*CommExecutor, payments map[string]*PaymentExecutor, customTools map[string]*CustomToolExecutor) *RouterExecutor {
	return &RouterExecutor{
		github:       github,
		workspaces:   workspaces,
		databases:    databases,
		environments: environments,
		comms:        comms,
		payments:     payments,
		customTools:  customTools,
	}
}

func isCommOperation(r Request) bool {
	switch r.Operation {
	case "send_message", "send_email", "create_ticket", "update_ticket", "publish_document":
		return true
	default:
		return false
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

func isPaymentOperation(r Request) bool {
	switch r.Operation {
	case "transfer_funds", "create_charge", "issue_refund", "get_balance":
		return true
	default:
		return false
	}
}

func isCustomOperation(r Request) bool {
	return r.Operation == "call_custom_tool" || r.CustomTool != "" || !operations[r.Operation]
}

func isWorkspaceOperation(r Request) bool {
	switch r.Operation {
	case "exec_cmd", "write_file", "delete_file", "list_dir":
		return true
	case "read_file":
		return r.Workspace != "" || (r.Repository == "" && r.Database == "" && r.Environment == "" && r.Channel == "" && r.Account == "" && r.CustomTool == "")
	default:
		return false
	}
}

func (r *RouterExecutor) isCustomOp(req Request) bool {
	if isCustomOperation(req) {
		return true
	}
	for _, tool := range r.customTools {
		if member(tool.target.Operations, req.Operation) {
			return true
		}
	}
	return false
}

func (r *RouterExecutor) Execute(ctx context.Context, req Request) Outcome {
	if r.isCustomOp(req) {
		toolKey := req.CustomTool
		if toolKey == "" {
			toolKey = text(req.Args, "tool")
		}
		if toolKey == "" {
			for id, tool := range r.customTools {
				if member(tool.target.Operations, req.Operation) {
					toolKey = id
					break
				}
			}
		}
		if toolKey == "" {
			toolKey = "default"
		}
		executor, ok := r.customTools[toolKey]
		if !ok {
			return Outcome{Error: fmt.Sprintf("custom tool %q is not configured on this gateway", toolKey)}
		}
		return executor.Execute(ctx, req)
	}
	if isPaymentOperation(req) {
		accKey := req.Account
		if accKey == "" {
			accKey = "default"
		}
		executor, ok := r.payments[accKey]
		if !ok {
			return Outcome{Error: fmt.Sprintf("payment account %q is not configured on this gateway", accKey)}
		}
		return executor.Execute(ctx, req)
	}
	if isCommOperation(req) {
		commKey := req.Channel
		if commKey == "" {
			commKey = "default"
		}
		executor, ok := r.comms[commKey]
		if !ok {
			return Outcome{Error: fmt.Sprintf("communication target %q is not configured on this gateway", commKey)}
		}
		return executor.Execute(ctx, req)
	}
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
