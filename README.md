<p align="center"><img src="assets/logo.jpg" alt="Circuit" width="120" /></p>

# Circuit

**A governed execution boundary for AI agents.**

Circuit sits between an AI agent and the services it uses. The agent requests an action; Circuit checks whether it is allowed, asks for approval when required, and executes it using credentials held by the gateway. A built-in web interface shows requests, decisions, and execution history.

For example, let an engineering agent read two repositories and open up to five PRs per hour. Restrict writes to approved branches, forbid workflow-file changes, and require approval before merging an exact commit. The agent can keep working without receiving an unrestricted GitHub token.

**Start here:** [Watch the overview](#watch-circuit) | [Try the approval demo](#try-the-demo) | [Understand the flow](#how-it-works) | [Integrate your app](#integrate-your-app) | [Check support and limits](#supported-integrations)

## Watch Circuit

A 22-second cinematic introduction to scoped agent access, gateway-held credentials, configured Docker isolation, and recorded execution.

https://github.com/user-attachments/assets/fb2ec505-0782-4ce7-bae7-3df1291683c1

The 3D scenes explain the boundary; the recorded interface results are from the local simulation below. Docker isolation requires configuration; installing the SDK alone does not provide isolation.

## Install With Homebrew

```sh
brew install hgayan7/circuit/circuit
# Existing installations:
brew update
brew upgrade circuit
```

The [tap](https://github.com/hgayan7/homebrew-circuit) updates hourly after stable releases pass CI and artifact validation. GitHub scheduling may delay an update. Linux/macOS amd64/arm64 binaries are verified against release checksums.

## Try The Demo

Watch an agent propose a GitHub file change, wait for your approval, and continue to a separately approved merge. The review interface shows the requested payload, your decision, and the execution history.

This local simulation never contacts GitHub and needs no real credentials. Requires Go 1.26.9 or later and Python 3. For a reproducible release checkout, use the `v0.3.0` tag.

1. From the source checkout, build and start the demo:

   ```sh
   go build -o bin/circuit ./cmd/circuit
   bin/circuit gateway demo
   ```

2. Open [http://127.0.0.1:8080](http://127.0.0.1:8080) and enter the public demo operator token printed by the command.

3. In another terminal, run the sample agent:

   ```sh
   python3 examples/github-agent.py --demo
   ```

4. Review and approve the pending file-write request. The agent continues to open a simulated PR, then waits for your approval to merge its exact head commit.

5. Approve the merge and inspect the completed actions and approval records in the interface. The agent prints `Workflow completed through Circuit.`

For a real GitHub workflow, configure your own credentials and repository scopes using the [GitHub guide](docs/github-gateway.md). See [Getting Started](docs/getting-started.md) for installation and next steps.

## What Is Available

The **[v0.3.0 release](https://github.com/hgayan7/circuit/releases/tag/v0.3.0)** is a self-hosted BYOK gateway with tested Docker/runc isolation and maintained Python, TypeScript, and Go clients. Check [repository releases](https://github.com/hgayan7/circuit/releases) for validated artifacts. Downloadable binaries target Linux and macOS, amd64 and arm64, with SHA-256 checksums.

| Version | What you get |
| --- | --- |
| `v0.3.0` | Explicit rules authorize autonomous actions, including REST writes; trusted-consent movie booking example; Go security fixes. Includes the v0.2.0 deployment and client features. |
| Earlier `v0.2.0` | Guided setup, action gateway, review UI, agent-only MCP connector, isolated gateway-only agent deployment, GitHub and governed MCP/REST/plugin production targets, and generated TypeScript/Python/Go clients. |
| Earlier prerelease `v0.2.0-rc.2` | GitHub production-oriented profile, guided setup, action gateway, review UI, and agent-only MCP connector. Does not include the isolation and generated-client additions in v0.2.0. |

The release workflow verifies exact downloadable binary and SDK archives before publication. Gateway v0.3.0 retains the OpenAPI 1.0.0 contract and compatible SDK 0.2.0 archives. See the [v0.3.0 notes](docs/releases/v0.3.0.md), especially the changed `ALLOW` semantics. Use the matching source tag for Docker builds and rehearsal scripts; changes on `main` may be newer than the release. No npm/PyPI or container-registry publishing is performed. The [Homebrew tap](https://github.com/hgayan7/homebrew-circuit) tracks validated stable releases automatically. See [local artifact testing](docs/local-release-testing.md) to build and validate without publishing.

The owner waived the uninterrupted 72-hour fixture soak on 2026-10-05; it has not passed. See the [release checklist](docs/release-checklist.md) for scope and exceptions. Neither a stable version, a `--production` flag, nor a passing fixture test is production certification.

## How It Works

```text
Your agent/app inside the isolated environment
          | requests an action through an SDK or MCP
          v
Circuit gateway --- Human review when required
  permissions -> policies -> budgets -> approval -> recorded execution
          | gateway-owned service credentials
          v
Declared REST services / MCP servers / plugins / GitHub
          | result returned through Circuit
          v
Your agent/app
```

Circuit runs as a persistent middleware service. The agent decides what to request; Circuit decides whether to execute it. Operators review requests in the web interface.

**What prevents bypass?** In the tested Docker deployment, the agent can reach only Circuit, and it does not receive service credentials. It cannot skip Circuit and contact a service directly through that network. Installing an SDK alone on an unrestricted host does not provide this protection.

Circuit supplies Docker orchestration and a separate trusted network firewall. It is not a transparent model router or a custom OS-sandbox engine. Other infrastructure needs equivalent isolation enforced by its operator.

## Integrate Your App

### 1. Declare Services And Rules

Create an operator-owned manifest listing the services and operations your agent may use. Keep service credentials outside the agent workspace. The [integration guide](docs/isolated-agents.md) includes a complete manifest example, prerequisites, and image builds.

From the current source checkout, build the CLI and trusted images:

```sh
go build -o bin/circuit ./cmd/circuit
docker build --target gateway -f deploy/docker/Dockerfile -t circuit-gateway:local .
docker build --target boundary -f deploy/docker/Dockerfile -t circuit-boundary:local .
```

Requires Go 1.26.9 or later, Docker with Linux containers, Docker Compose v2 supporting `--wait`, and an agent image containing your app and dependencies.

Generate the configuration, scoped credentials, and local TLS files:

```sh
bin/circuit setup --integration middleware --upstream-manifest /private/upstreams.yaml \
  --out "$HOME/.circuit-operator"
```

Replace `/private/upstreams.yaml` with your manifest path. Setup is guided; the integration guide also provides noninteractive commands.

### 2. Start Circuit

```sh
bin/circuit up --dir "$HOME/.circuit-operator"
bin/circuit doctor --dir "$HOME/.circuit-operator"
```

Use the printed review URL and an operator token to review actions. Agent tokens cannot approve requests.

### 3. Connect And Run Your App

Configure your app's tool/model callbacks to use a Circuit SDK or MCP, then launch it:

```sh
bin/circuit agent run --dir "$HOME/.circuit-operator" --image YOUR_AGENT_IMAGE \
  --workspace /path/to/clean/workspace -- YOUR_COMMAND
```

Replace the image, workspace, and command with your app's values. Use the same setup directory throughout: `--out` creates it; `--dir` selects it. The runner supplies the gateway URL, agent token, and public CA, and mounts the reviewed workspace read-only.

**All network access must go through Circuit**, including model calls. The isolated runner permits only gateway TCP/8443. Model access must be a declared route/tool; Circuit does not automatically intercept an existing model SDK. In v0.3.0, an explicit gateway `ALLOW` rule can authorize REST POST requests, including inference, automatically. Unmatched writes default to approval. See [model access](docs/isolated-agents.md#model-access) for autonomous inference options and cost-accounting limits.

### Choose A Client

Circuit's API is language-agnostic. The enforcement rules stay in the gateway, not in each client.

| Client | Integration path | Validation |
| --- | --- | --- |
| Node.js / TypeScript | Generated SDK with a safety-aware facade | Tested against the real gateway over TLS. |
| Python | Generated SDK with a safety-aware facade | Tested against the real gateway over TLS. |
| Go | Generated SDK with a safety-aware facade | Tested against the real gateway over TLS. |
| Java, Kotlin, C#, Rust, and other languages | REST API or bindings generated from OpenAPI | Other generated clients are not yet runtime-qualified. |
| MCP-compatible agents | Circuit's agent-only MCP connector | Scoped MCP connection and forwarding rehearsed in CI. |

The maintained SDKs are available in this checkout and as packed repository release assets, **not on npm/PyPI**. See [local/archive installation](docs/local-release-testing.md) and [code examples](docs/isolated-agents.md#generated-clients). Generate another language with:

```sh
sh scripts/generate-client.sh kotlin /path/to/new-client-directory
```

The [OpenAPI contract](api/openapi.yaml) defines the shared REST API. Generated bindings handle the wire format; your app still needs to connect its callbacks and handle action states. The maintained facades submit once, poll for a result, and stop on denial, unresolved approval, or an uncertain outcome without automatically redispatching.

## What Circuit Enforces

| Control | Behavior |
| --- | --- |
| Scoped access | Explicit repository and operation allowlists, separate agent identities, and gateway-owned service credentials. |
| Combined policies | DENY overrides ALLOW; approval requirements and all matching budgets apply together. |
| Durable budgets | Atomic reservations persist through restarts and are rechecked before execution. |
| Exact approvals | Decisions bind to stored payloads and digests, with expiry and policy-version checks. GitHub merges also require the approved head SHA. |
| Retry protection | Stable idempotency keys return the original action. Claimed actions are not automatically replayed after ambiguous failures or restarts. |
| Review and audit | A web review queue and persisted history record requests, decisions, operator attribution, and service outcomes. |

## Supported Integrations

Use existing Streamable HTTP MCP servers or fixed REST routes through the [middleware contract](docs/middleware.md). Discover and review a service manifest, import it with `setup --integration middleware`, and deploy the isolated agent. Registered operations do not require a provider-specific adapter, but your app must call the Circuit contract.

For custom behavior, provider plugins run as separate services using the versioned [plugin contract](docs/plugin-contract.md). Policy, approvals, budgets, durable claims, and audit remain in the trusted core. Registering a plugin does not automatically make its provider production-supported.

v0.2.0 accepts governed MCP/REST transports and plugins under `--production`; rc.2 downloads do not contain these additions.

| Area | Tested scope and boundary |
| --- | --- |
| GitHub | Live fixture pilot: branches, files, PRs, merges, issues, App token refresh, approvals, budgets, isolated-agent execution, and signed webhook recovery. |
| PostgreSQL | Real local/CI queries and mutations, row limits, transactional rollback, permission failures, and timeouts. Requires least-privilege roles and query-specific policies. The native executor remains outside `--production`. |
| Workspace/files | Local root-scoped access, atomic writes, symlink-race protection, and bounded shell execution. Shell commands default to approval, can be explicitly authorized by gateway rules, and require an external OS sandbox. Native workspace execution remains outside `--production`. |
| Custom tools/plugins | Local HTTP/MCP integration and sidecar conformance tests. Each provider needs its own scope, credential-isolation, and recovery validation. |
| Operational email/storage | Firing/resolved SMTP messages and authenticated S3-compatible upload/readback/recovery fixtures. Actual BYOK destinations require operator acceptance. |
| Cloud, communication, payments | Simulation-only action adapters; no native provider execution. Operational alert email is separate from the simulated communication adapter. |

Simulations are explicit and return `simulated: true`. Missing credentials do not silently enable simulation. See [validation status](docs/validation-status.md) for detailed coverage and reproduction.

## Other Connection Modes

### Host-Side Guided Setup

The rc.2 binaries and current source include a guided BYOK flow and agent-only stdio MCP connector:

```sh
bin/circuit setup
bin/circuit start
# In another terminal:
bin/circuit doctor
```

Choose GitHub, PostgreSQL, read-only workspace access, or a provider plugin; register the generated `mcp.json` entry in your agent client. This host-side flow is cooperative, not sandbox enforcement. For current-source GitHub/MCP/REST/plugins, prefer [setup -> up -> isolated agent](docs/isolated-agents.md). PostgreSQL/native workspace executors remain bounded pilot integrations outside `--production`.

To rely on enforcement, agents must not have independent provider credentials or unrestricted alternative execution paths. Routing one tool through Circuit does not protect calls that bypass it.

### Proxy And Inspection Tools

The earlier tools remain available with a separate `circuit.yaml` configuration:

```sh
circuit run --policy examples/policies/expanded_safety.yaml -- python agent.py
circuit mcp wrap --policy circuit.yaml -- your-mcp-server
circuit inspect --kind prompt
circuit inspect --kind shell
circuit inspect --kind sql
```

These cover proxy-aware traffic, heuristic prompt-injection signals, and conservative parser-based shell/SQL restrictions. They do not replace sandboxing or the action gateway, and use older first-match policy semantics. Read the [safety guide](docs/safety.md) before relying on them.

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

## Verification And Limits

```sh
go test -race ./...
go vet ./...
```

[CI](https://github.com/hgayan7/circuit/actions/workflows/ci.yml) also checks generated-client drift, all three maintained SDKs against a real TLS gateway, isolated-agent bypass attempts, live PostgreSQL, reachable Go vulnerabilities, restricted Docker builds, real disk-full recovery, authenticated S3 recovery, alert behavior, and Linux/macOS cross-platform builds. Release publishing requires green CI for the exact tagged commit.

Recorded evidence covers [GitHub and local workflows](docs/validation-status.md), [Docker deployment](docs/production-validation-results.json), [email and backups](docs/operations-validation-results.json), [S3 storage](docs/archive-validation-results.json), and [specific-image upgrade/rollback](docs/upgrade-validation-results.json). A short test or running soak is not a completed multi-day validation.

| Limit | What it means |
| --- | --- |
| Single-process storage | The bbolt store has one owning process, not distributed replicas. |
| Bearer-role identity | Named roles are not SSO/MFA; team identity and reviewer quorum remain future work. |
| Ambiguous delivery | Circuit prevents automatic replay of claimed actions, not exactly-once delivery across network boundaries. |
| Qualified isolation | The enforced deployment is tested with Docker/Linux containers and `runc`. Other runtimes and infrastructure need separate qualification. Host/kernel administrators and malicious trusted images are outside the threat model. |
| Qualification exceptions | The uninterrupted long soak is owner-waived, not passed; independent security review was deferred, not completed. Additional providers need their own validation. |

## Documentation

| Need | Guide |
| --- | --- |
| Integrate an isolated agent and install SDKs | [Isolated agents and generated clients](docs/isolated-agents.md) |
| Install or explore the demo | [Getting started](docs/getting-started.md) |
| Register REST routes or MCP tools | [Middleware](docs/middleware.md) |
| Run a support-agent inventory workflow end to end | [Inventory replacement example](examples/inventory-support/README.md) |
| Run an autonomous booking chatbot with customer consent | [Movie booking example](examples/movie-booking/README.md) |
| Build a provider plugin | [Plugin contract](docs/plugin-contract.md) |
| Operate, back up, and restore Circuit | [Deployment](docs/production-deployment.md) and [operations](docs/operations.md) |
| Configure autonomous execution and approval rules | [Gateway policy](docs/gateway-policy.md) |
| Check tested scope and release gates | [Validation status](docs/validation-status.md) and [release checklist](docs/release-checklist.md) |
| Check compatibility or rehearse upgrades | [Compatibility policy](docs/compatibility.md) and [local artifact testing](docs/local-release-testing.md) |

## License

MIT. See [LICENSE](LICENSE).
