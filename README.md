<p align="center"><img src="assets/logo.jpg" alt="Circuit" width="120" /></p>

# Circuit

**A bounded allowance for autonomous agents to act.**

Circuit is a self-hosted GitHub action gateway for unattended engineering agents. Give an agent permission to work in selected repositories, enforce cumulative action limits, and require approval of consequential changes.

For example: let an agent open five PRs per hour in two repositories, restrict its writes to `circuit/` branches, forbid workflow-file changes, and require approval before merging an exact commit.

## Try it

```sh
go build -o bin/circuit ./cmd/circuit
bin/circuit gateway demo
```

Open http://127.0.0.1:8080 and enter the public demo operator token printed by the command. In another terminal:

```sh
python3 examples/github-agent.py --demo
```

Review the file-write and merge proposals in the interface. This simulation never contacts GitHub and needs no real credentials.

## What the gateway does

- **Scoped agent access:** explicit repository and operation allowlists, separate Circuit bearer identities, and upstream credentials held by the gateway.
- **Combined enforcement:** DENY overrides ALLOW; approval requirements and all matching budgets apply together.
- **Durable limits:** atomic reservations persist through restarts and are rechecked before execution.
- **Exact-action approvals:** approve a stored payload and digest, with expiry and policy-version checks; merges also use GitHub's head-SHA precondition.
- **Retry protection:** stable idempotency keys return the original action. Claimed actions are never automatically replayed after ambiguous failures or restarts.
- **Accountable execution:** a web review queue and persisted history show proposals, decisions, operator attribution, and upstream outcomes.
- **REST and MCP:** structured GitHub actions over authenticated REST or Streamable HTTP MCP using the official Go SDK.

```text
Agent in an existing sandbox
          │ Circuit agent credential
          ▼
Circuit action gateway ─── Operator review interface
  scopes · limits · approvals · durable state
          │ scoped GitHub credential
          ▼
        GitHub
```

Circuit integrates with existing sandboxes and model gateways. It does not provide model routing or OS isolation. To rely on its controls, agents must lack independent GitHub credentials and unrestricted alternative execution paths.

## Use a real repository

```sh
bin/circuit gateway init --repo your-org/your-repo
bin/circuit gateway check gateway.yaml
export CIRCUIT_ADMIN_TOKEN="$(bin/circuit gateway token)"
export CIRCUIT_AGENT_TOKEN="$(bin/circuit gateway token)"
# Set a scoped GITHUB_TOKEN securely on the gateway only.
bin/circuit gateway serve --config gateway.yaml --data .circuit/gateway.db
```

See the [GitHub gateway guide](docs/github-gateway.md) for configuration, agent/MCP connections, approvals, retry semantics, and deployment limits.

## Supporting inspection tools

The earlier generic inspection tools remain available:

```sh
circuit run --policy examples/policies/expanded_safety.yaml -- python agent.py
circuit mcp wrap --policy circuit.yaml -- your-mcp-server
circuit inspect --kind prompt
circuit inspect --kind shell
circuit inspect --kind sql
```

These provide HTTPS inspection for proxy-aware clients, heuristic prompt-injection checks, and conservative parser-based shell/SQL restrictions. They use `circuit.yaml`, separately from the action gateway's `gateway.yaml`.

Read [the safety guide](docs/safety.md) for coverage and limitations. Those tools do not replace a sandbox, and their older first-match CEL policy semantics differ from the gateway's combined enforcement.

## Roadmap

**The full product vision is bounded autonomy across tools:** one place to define where agents can act, what they can do, how much they can do, and when they need human approval. GitHub is the first supported action adapter.

The capabilities below describe planned support, not features available today. Each adapter will expose specific reviewed operations with its own permissions, approval rules, limits, and recovery behavior.

| Area | Intended support |
| --- | --- |
| GitHub | **Supported:** branch, file, PR, merge, and issue actions with approval and budgets; GitHub App installations with short-lived repository-scoped tokens and automated webhook reconciliation. |
| Cloud and deployments | Bounded deployment and infrastructure operations, scoped to projects and environments, with approval for production changes and destructive actions. |
| Databases | Scoped queries and controlled writes, with limits on affected rows, execution time, and accessible data; review of consequential changes. |
| Shell and files | **Supported:** structured commands (`exec_cmd`), file reads (`read_file`), atomic writes (`write_file`), directory listings (`list_dir`), and deletes (`delete_file`) inside workspace boundaries with execution timeouts, output buffers, and approval for destructive operations. |
| Communication and work tools | Email, messaging, documents, and ticket actions with recipient/resource restrictions, volume limits, and approval before consequential sends or publication. |
| Business and payment APIs | Explicit supported operations with transaction and cumulative spending limits, destination restrictions, and approval for financial commitments. |
| Custom tools | A documented adapter interface and REST/MCP connections so teams can bring their own APIs under the same action controls. |

Across these integrations, the roadmap includes:

- **Agent and workflow allowances:** shared agent limits plus separate workflow budgets, expiring credentials, immediate revocation, and constrained delegation to other agents.
- **Credential isolation:** secret-manager integration, short-lived provider tokens, rotation, and credentials kept outside agent environments.
- **Team approvals:** individual reviewer accounts, SSO, roles, multiple-reviewer requirements, and notifications for pending decisions.
- **Reliable execution:** provider-aware retry handling, webhook/status reconciliation, cancellation before dispatch, and clear recovery for uncertain outcomes. Compensation will be offered only where the provider supports it.
- **Security and isolation:** integration with existing sandboxes and egress controls, plus prompt-injection signals and sensitive-data checks alongside deterministic action policies.
- **Operations and evidence:** searchable audit history, policy simulation, usage dashboards, retention controls, protected backups, and support for multiple gateway instances.

The governing principle stays the same: agents receive bounded permission to act through Circuit. Enforcement depends on isolating credentials and preventing alternative execution paths; detection alone cannot guarantee safe behavior.

## Validation and status

```sh
go test -race ./...
go vet ./...
```

Gateway tests cover the full simulated GitHub workflow, official SDK MCP connections, concurrent retries, persistent budgets, approval expiry, stale policies and commit SHAs, uncertain-outcome recovery, RSA key parsing, RS256 JWT minting, GitHub App short-lived token auto-refresh, HMAC-SHA256 webhook verification, and automated event reconciliation.

This is an initial pilot implementation. A [live GitHub pilot](docs/github-pilot.md) passed 43 checks across private and protected public fixture repositories, including real merges, stale-SHA rejection, enforced branch protection, and restart persistence. GitHub App installation tokens, repository-scoped permissions, and webhook reconciliation are now integrated into the gateway engine. A single private bbolt database provides durability; distributed operation and individual reviewer SSO are not implemented. Circuit prevents automatic replay of claimed actions, rather than promising exactly-once delivery across the GitHub network boundary.

## License

MIT. See [LICENSE](LICENSE).
