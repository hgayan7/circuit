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

## Validation and status

```sh
go test -race ./...
go vet ./...
```

Gateway tests cover the full simulated GitHub workflow, official SDK MCP connections, concurrent retries, persistent budgets, approval expiry, stale policies and commit SHAs, and uncertain-outcome recovery.

This is an initial pilot implementation. A [live GitHub pilot](docs/github-pilot.md) passed 43 checks across private and protected public fixture repositories, including real merges, stale-SHA rejection, enforced branch protection, and restart persistence. It used an existing CLI OAuth credential; repository-scoped token and GitHub App permissions still need validation. A single private bbolt database provides durability; distributed operation and individual reviewer SSO are not implemented. Circuit prevents automatic replay of claimed actions, rather than promising exactly-once delivery across the GitHub network boundary.

## License

MIT. See [LICENSE](LICENSE).
