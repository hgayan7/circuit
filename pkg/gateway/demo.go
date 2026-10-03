package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// These public credentials work only for the explicit, simulated demo command.
const DemoAdminToken = "circuit-demo-operator-local-only-token"
const DemoAgentToken = "circuit-demo-engineering-local-only-token"
const DemoSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type DemoExecutor struct {
	mu     sync.Mutex
	nextPR int
}

func (d *DemoExecutor) Execute(_ context.Context, r Request) Outcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	var result any
	switch r.Operation {
	case "create_pr":
		d.nextPR++
		result = map[string]any{"number": d.nextPR, "html_url": "https://github.com/" + r.Repository + "/pull/" + fmt.Sprint(d.nextPR), "head": map[string]any{"sha": DemoSHA}, "simulated": true}
	case "merge_pr":
		result = map[string]any{"merged": true, "sha": text(r.Args, "sha"), "simulated": true}
	case "get_pr":
		result = map[string]any{"number": number(r.Args, "number"), "head": map[string]any{"sha": DemoSHA}, "simulated": true}
	default:
		result = map[string]any{"operation": r.Operation, "simulated": true}
	}
	data, _ := json.Marshal(result)
	return Outcome{Status: 200, Body: data}
}
func ExampleConfig(repo string) string {
	return fmt.Sprintf(`name: engineering-actions
admin_token_env: CIRCUIT_ADMIN_TOKEN
github_token_env: GITHUB_TOKEN
approval_ttl: 1h
agents:
  - id: engineering-agent
    token_env: CIRCUIT_AGENT_TOKEN
    repositories: [%q]
    branch_prefix: circuit/
    actions: [read_file, get_pr, create_branch, put_file, create_pr, merge_pr, create_issue, update_issue]
rules:
  - id: review-file-writes
    actions: [put_file]
    action: REQUIRE_APPROVAL
    reason: Review the exact file content before writing
limits:
  - id: agent-pr-hour
    actions: [create_pr]
    scope: agent
    window: 1h
    max_calls: 5
  - id: repository-pr-hour
    actions: [create_pr]
    scope: repository
    window: 1h
    max_calls: 10
  - id: total-write-hour
    actions: [create_branch, put_file, create_pr, merge_pr, create_issue, update_issue]
    scope: agent_repository
    window: 1h
    max_calls: 30
safety:
  prompt_injection: true
`, repo)
}
