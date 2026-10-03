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

## Support and roadmap

The product vision is bounded autonomy across tools. Current support distinguishes real execution from simulations; policy tests alone do not establish provider support.

| Area | Current status |
| --- | --- |
| GitHub | **Live pilot validated:** branches, files, PRs, merges, issues, exact approvals, budgets, repository-scoped GitHub App tokens, and an agent container with upstream egress blocked. Signed repository webhook delivery validates exact-head merge recovery. |
| PostgreSQL | **Local integration validated:** real queries and mutations, row limits, transactional rollback, permission failures, and timeouts. PostgreSQL is the only real database backend tested. |
| Workspace/files | **Local integration validated:** root-scoped file access, atomic writes without implicit replacement, symlink-race protection, and bounded shell execution. All shell commands require approval and an external OS sandbox. |
| Cloud | **Simulation only:** deployment, rollback, restart, status, and scale policies; no cloud provider backend. |
| Communication/work tools | **Simulation only:** messages, email, tickets, and documents; no Slack, SMTP, Jira, Notion, or other native delivery backend. |
| Payments | **Simulation only:** transfers, charges, refunds, and balance; no payment provider backend. |
| Custom tools | **Local HTTP integration validated:** generic HTTP dispatch and MCP exposure, approvals, budgets, and uncertain-response recovery. Individual providers require their own validation. |

Cloud, communication, and payment configurations require explicit `simulation: true`; successful simulator results contain `simulated: true`. Database simulation requires `driver: mock`, and custom-tool simulation requires a `mock:` or `sim:` endpoint. Missing database credentials and empty custom endpoints fail instead of silently simulating.

See [the validation report](docs/validation-status.md) for evidence, reproduction, compatibility changes, and remaining release limits.

Across these integrations, the roadmap includes:

- **Agent and workflow allowances:** shared agent limits plus separate workflow budgets, expiring credentials, immediate revocation, and constrained delegation to other agents.
- **Credential isolation:** secret-manager integration, short-lived provider tokens, rotation, and credentials kept outside agent environments.
- **Team approvals:** SSO/MFA, verified reviewer accounts, multiple-reviewer requirements, and notifications for pending decisions. Named bearer identities and roles are available today.
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

The follow-up GitHub App pilot passed 44 checks with an isolated agent. Real signed repository webhook recovery passed 12 checks; the local CLI/REST/browser workflow passed 14 checks. Real PostgreSQL integration and official SDK MCP tests pass, with a dedicated PostgreSQL CI job now configured. See [validation status](docs/validation-status.md) for evidence, reproduction, compatibility changes, and remaining release gates.

This remains a bounded pilot. A [production-oriented GitHub deployment](docs/production-deployment.md) now includes TLS, named operator roles, restricted Docker containers, health/metrics, and verified backup/paused restore. A short Docker soak is not a multi-day production validation. The single-process bbolt store does not provide distributed operation; named tokens are not SSO/MFA. Shell execution needs an external OS sandbox, SQL needs least-privilege database roles, and cloud/communication/payment adapters are simulation-only. Circuit prevents automatic replay of claimed actions, not exactly-once delivery across network boundaries.

## License

MIT. See [LICENSE](LICENSE).
