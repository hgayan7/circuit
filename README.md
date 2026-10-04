<p align="center"><img src="assets/logo.jpg" alt="Circuit" width="120" /></p>

# Circuit

**A governed execution boundary for AI agents.**

Circuit is a self-hosted action gateway between an agent and the tools it uses. Agents receive scoped Circuit credentials; the gateway holds upstream credentials, evaluates policies and budgets, and asks a human to approve consequential actions. A built-in web interface shows proposals, decisions, and execution history.

For example, let an engineering agent read two repositories and open up to five PRs per hour. Restrict writes to approved branches, forbid workflow-file changes, and require approval before merging an exact commit. The agent can keep working without receiving an unrestricted GitHub token.

## Release Status

[**v0.2.0-rc.2**](https://github.com/hgayan7/circuit/releases/tag/v0.2.0-rc.2) is available as a **developer preview / release candidate**, not a production-certified release. Downloadable binaries target Linux and macOS, amd64 and arm64, with SHA-256 checksums. The stable Homebrew tap does not install this candidate.

The released candidate contains the GitHub production-oriented profile. **Current main adds an isolated gateway-only agent deployment, governed MCP/REST/plugin production targets, and generated TypeScript/Python/Go clients.** These newer features are source-only until the next release. The full uninterrupted 72-hour fixture soak remains pending. See the [release checklist](docs/release-checklist.md) for acceptance criteria and supported scope.

## Integrate Your Agent

Follow the [isolated-agent quickstart](docs/isolated-agents.md). Register reviewed upstream routes/tools and keep their credentials in the gateway, then:

```sh
circuit setup --integration middleware --upstream-manifest /private/upstreams.yaml \
  --out "$HOME/.circuit-operator"
circuit up --dir "$HOME/.circuit-operator"
circuit agent run --dir "$HOME/.circuit-operator" --image YOUR_AGENT_IMAGE \
  --workspace /path/to/clean/workspace -- YOUR_COMMAND
```

Use the same setup directory for all three commands (`--out` on setup, `--dir` on up/run). The guide includes exact noninteractive commands and image builds. Connect via MCP or the generated REST clients. The isolated runner only permits gateway TCP/8443; model access must also be a declared route. Installing an SDK alone on an unrestricted host does not prevent bypass.

## Try It Locally

Requires Go 1.26.7 or later and Python 3 for the sample agent. From a source checkout:

```sh
go build -o bin/circuit ./cmd/circuit
bin/circuit gateway demo
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080) and enter the public demo operator token printed by the command. In another terminal:

```sh
python3 examples/github-agent.py --demo
```

Review file-write and merge proposals in the interface. This simulation never contacts GitHub and needs no real credentials. For a reproducible candidate checkout, use the `v0.2.0-rc.2` tag. See [Getting Started](docs/getting-started.md) for installation and next steps.

## How It Fits

```text
Agent in a restricted network namespace
          | Circuit agent credential
          v
Circuit action gateway --- Operator review interface
  scopes | policies | budgets | approvals | durable state
          | scoped provider credential
          v
Declared MCP / REST / plugins / GitHub
```

Run Circuit as a persistent middleware service. Operators use the web interface. Current source supplies Docker orchestration and a separate trusted namespace firewall; you can also enforce equivalent isolation in your own infrastructure. Circuit is not a transparent model router or a custom OS-sandbox engine.

### Guided Setup

The rc.2 binaries and current source include a guided BYOK flow and agent-only stdio MCP connector:

```sh
bin/circuit setup
bin/circuit start
# In another terminal:
bin/circuit doctor
```

Choose GitHub, PostgreSQL, read-only workspace access, or a provider plugin; register the generated `mcp.json` entry in your agent client. This host-side flow is cooperative, not sandbox enforcement. For current-source GitHub/MCP/REST/plugins, prefer [setup -> up -> isolated agent](docs/isolated-agents.md). PostgreSQL/native workspace executors remain bounded pilot integrations outside `--production`.

To rely on enforcement, agents must not have independent provider credentials or unrestricted alternative execution paths. Routing one tool through Circuit does not protect calls that bypass it.

## What Is Enforced

- **Scoped access:** explicit repository and operation allowlists, separate agent identities, and gateway-owned upstream credentials.
- **Combined policies:** DENY overrides ALLOW; approval requirements and all matching budgets apply together.
- **Durable budgets:** atomic reservations persist through restarts and are rechecked before execution.
- **Exact approvals:** decisions bind to stored payloads and digests, with expiry and policy-version checks. GitHub merges also require the approved head SHA.
- **Retry protection:** stable idempotency keys return the original action. Claimed actions are not automatically replayed after ambiguous failures or restarts.
- **Review and audit:** a web review queue and persisted history record proposals, decisions, operator attribution, and upstream outcomes.

## BYOK Deployment

You own the deployment and bring your own credentials. No provider keys, SMTP passwords, or backup decryption identities are bundled.

The [GitHub deployment guide](docs/production-deployment.md) covers the single-process `--production` profile: GitHub App authentication, TLS 1.3, named observer/reviewer/admin roles, mounted secrets, restricted Docker containers, health checks, and authenticated metrics. Agents receive Circuit credentials, not the App private key.

Optional [operational extensions](docs/operations.md) provide:

- Prometheus monitoring and Alertmanager notifications through your SMTP provider.
- Scheduled age-encrypted backups and local retention, with the decryption identity kept offline.
- A separate storage BYOK worker that uploads immutable archives and verifies full remote readback.
- Disk-capacity, active TLS-expiry, backup, and storage-transfer alerts.
- Restore with a mandatory reconciliation barrier, plus upgrade/rollback and soak rehearsal tools.

Docker deployment builds and rehearsal scripts require the source checkout. Local SMTP and authenticated S3-compatible fixtures validate the operational pipelines; they do not prove delivery or disaster recovery for your chosen providers.

## Extensible Integrations

Provider plugins run as separate services using the versioned [plugin contract](docs/plugin-contract.md). Policy, approvals, budgets, durable claims, and audit remain in the trusted core. Registering a plugin does not automatically make its provider production-supported.

**New on main, after rc.2:** [MCP and REST middleware](docs/middleware.md) reuses existing Streamable HTTP MCP servers and fixed REST routes through the same enforcement core. Discover and review an upstream manifest, import it with `setup --integration middleware`, then deploy the isolated agent. No provider-specific adapter is required for registered operations. Current `--production` accepts these governed transports and plugins; rc.2 downloads do not contain this work. The [OpenAPI contract](api/openapi.yaml) generates clients for other languages without a separate policy engine.

| Area | Tested Scope |
| --- | --- |
| GitHub | Live fixture pilot: branches, files, PRs, merges, issues, App token refresh, approvals, budgets, isolated-agent execution, and signed webhook recovery. |
| PostgreSQL | Real local/CI database queries and mutations, row limits, transactional rollback, permission failures, and timeouts. Requires least-privilege roles and query-specific policies; outside the GitHub-only production profile. |
| Workspace/files | Local root-scoped file access, atomic writes, symlink-race protection, and bounded shell execution. Shell commands require approval and an external OS sandbox. |
| Custom tools/plugins | Local HTTP/MCP integration and sidecar conformance tests. Each provider needs its own scope, credential-isolation, and recovery validation. |
| Operational email/storage | Firing/resolved SMTP messages and authenticated S3-compatible upload/readback/recovery fixtures. Actual BYOK destinations require operator acceptance. |
| Cloud, communication, payments | Simulation-only action adapters; no native provider execution. Operational alert email is separate from the simulated communication adapter. |

Simulations are explicit and return `simulated: true`. Missing real credentials do not silently enable simulation. See [validation status](docs/validation-status.md) for detailed coverage and reproduction.

## Tests And Limits

```sh
go test -race ./...
go vet ./...
```

[CI](https://github.com/hgayan7/circuit/actions/workflows/ci.yml) also exercises live PostgreSQL, reachable Go vulnerability checks, restricted Docker builds, real disk-full recovery, authenticated S3 recovery, alert behavior, and Linux/macOS cross-platform builds. Release publishing requires green CI for the exact tagged commit.

Recorded evidence covers [GitHub and local workflows](docs/validation-status.md), [Docker deployment](docs/production-validation-results.json), [email and backups](docs/operations-validation-results.json), [S3 storage](docs/archive-validation-results.json), and [specific-image upgrade/rollback](docs/upgrade-validation-results.json). A short test or running soak is not a completed multi-day validation.

The bbolt store has one owning process, not distributed replicas. Named bearer roles are not SSO/MFA. Circuit prevents automatic replay of claimed actions; it does not guarantee exactly-once delivery across network boundaries. Independent security review was deferred, not completed. Team identity, reviewer quorum, distributed operation, and additional validated providers remain future work.

## Supporting Inspection Tools

The earlier proxy and inspection tools remain available with a separate `circuit.yaml` configuration:

```sh
circuit run --policy examples/policies/expanded_safety.yaml -- python agent.py
circuit mcp wrap --policy circuit.yaml -- your-mcp-server
circuit inspect --kind prompt
circuit inspect --kind shell
circuit inspect --kind sql
```

These cover proxy-aware traffic, heuristic prompt-injection signals, and conservative parser-based shell/SQL restrictions. They do not replace sandboxing or the action gateway, and use older first-match policy semantics. Read the [safety guide](docs/safety.md) before relying on them.

## License

MIT. See [LICENSE](LICENSE).
