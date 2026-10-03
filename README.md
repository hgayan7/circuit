<p align="center"><img src="assets/logo.jpg" alt="Circuit" width="120" /></p>

# Circuit

**Approval and action limits for AI agents.**

Circuit is a self-hosted action gateway between an agent and the tools it uses. Agents receive scoped Circuit credentials; the gateway holds upstream credentials, evaluates policies and budgets, and asks a human to approve consequential actions. A built-in web interface shows proposals, decisions, and execution history.

For example, let an engineering agent read two repositories and open up to five PRs per hour. Restrict writes to approved branches, forbid workflow-file changes, and require approval before merging an exact commit. The agent can keep working without receiving an unrestricted GitHub token.

## Release Status

[**v0.2.0-rc.1**](https://github.com/hgayan7/circuit/releases/tag/v0.2.0-rc.1) is available as a **developer preview / release candidate**, not a production-certified release. Downloadable binaries target Linux and macOS, amd64 and arm64, with SHA-256 checksums. The stable Homebrew tap does not install this candidate.

The GitHub-only production-oriented profile, operational extensions, recovery tests, and cross-platform builds are implemented. The full uninterrupted 72-hour fixture soak remains pending. Operators must also validate their intended workload, email delivery, and separate-host recovery with their own credentials. See the [release checklist](docs/release-checklist.md) for acceptance criteria and supported scope.

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

Review file-write and merge proposals in the interface. This simulation never contacts GitHub and needs no real credentials. For a reproducible candidate checkout, use the `v0.2.0-rc.1` tag. See [Getting Started](docs/getting-started.md) for installation and next steps.

## How It Fits

```text
Agent in an existing sandbox
          | Circuit agent credential
          v
Circuit action gateway --- Operator review interface
  scopes | policies | budgets | approvals | durable state
          | scoped provider credential
          v
        GitHub
```

Run Circuit as a persistent middleware service. Connect agent tools through authenticated REST or Streamable HTTP MCP; operators use the web interface. The CLI supplies setup, validation, and recovery commands. Circuit is not a model router or an OS sandbox.

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

| Area | Tested Scope |
| --- | --- |
| GitHub | Live fixture pilot: branches, files, PRs, merges, issues, App token refresh, approvals, budgets, isolated-agent execution, and signed webhook recovery. The `--production` profile supports GitHub only. |
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
