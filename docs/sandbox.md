# Optional Agent Sandbox

**Current-source recommended flow:** [isolated agents](isolated-agents.md) adds `circuit up` and `agent run --dir`, automatically wiring a dedicated gateway-only firewall namespace. That enforced mode is newer than rc.2. The older invocation below remains available but checks only an internal Docker network, not per-agent egress firewall rules.

Circuit's action gateway works with your existing sandbox. The optional `circuit agent run` command in v0.2.0-rc.2 and on `main` provides a restricted Docker invocation; it is not an OS isolation implementation or a replacement for the action gateway.

```text
Agent container -- scoped Circuit token --> Action gateway --> Provider/plugin
                                               |
                                         Operator approval

Optional model gateway: separately governed endpoint, never provider keys in agent
Optional operations: SMTP alerts, backups, archive storage, monitoring
```

## Run An Agent

First deploy the HTTPS gateway on an existing **internal** Docker network. The [deployment template](production-deployment.md) creates an `agent_only` network with the gateway attached. The runner checks Docker's `Internal` setting and rejects ordinary bridge/host networks. Do not put other services or unrestricted proxies on that network. The gateway reaches providers through its separate upstream network.

Prepare a private agent token file and public CA outside the workspace. The image must contain your agent and, for the generated stdio MCP entry, the current `circuit` executable on `PATH`. Pin a trusted image digest in real deployments. An agent image alone does not make its tools use Circuit.

```sh
circuit agent run \
  --image your-agent-image@sha256:YOUR_DIGEST \
  --network YOUR_PROJECT_agent_only \
  --url https://127.0.0.1:8443 \
  --gateway-url https://gateway:8443 \
  --token-file /private/agent/agent-token \
  --ca-cert /private/agent/ca.crt \
  --workspace /absolute/path/to/workspace \
  -- your-agent-command
```

`--url` is reachable from the host for verified agent-identity/readiness/discovery preflight. `--gateway-url` is the origin reachable inside Docker; its hostname must match the certificate. `--dry-run` performs preflight and prints the restricted plan without starting a container. It does not verify container isolation or provider access.

The container receives only:

- `/run/circuit/agent-token`: the scoped Circuit credential, not a provider/operator key.
- `/run/circuit/ca.crt`: the public gateway CA.
- `/run/circuit/mcp.json`: a stdio MCP entry referencing those two files.
- `CIRCUIT_GATEWAY_URL`, `CIRCUIT_TOKEN_FILE`, `CIRCUIT_CA_CERT`, and `CIRCUIT_MCP_CONFIG`: connection references, not raw secrets.

Configure the agent to register that MCP entry or call authenticated Circuit REST/Streamable HTTP directly. The runner does not configure every agent framework automatically. The generated connector uses `--mounted-token`: Linux verifies that the token is a regular file on a read-only filesystem mount. Ordinary host connections still require a private credential file.

Host staging has a private directory; its individual files are readable by the non-root container UID and mounted read-only. The scoped token is intentionally available to the agent. The complete operator directory, provider credentials, host environment, Docker socket, and TLS private key are not passed by the runner.

## Restrictions And Limits

The fixed plan uses UID/GID 10001, all capabilities dropped, no new privileges, a read-only root filesystem and workspace, a 128 MiB no-exec `/tmp`, 512 MiB memory, one CPU, and 64 processes. It does not accept arbitrary Docker flags. Container stdin is forwarded for MCP, and a uniquely named owned container is removed after normal exit or interruption. Cleanup failures are surfaced explicitly.

`--writable` explicitly permits direct workspace writes. Those local writes do **not** pass through gateway approval. Mount only a disposable working copy, never operator files or host sockets. Untrusted images may contain their own secrets, privileged helpers, or undesirable tooling; review what you supply. Docker daemon access on the host is itself privileged.

An internal network blocks ordinary public egress but does not prove safety against every Docker/host configuration, network peer, proxy, DNS path, or kernel attack. Docker shares a kernel on Linux; it is not a VM-grade boundary. For higher-risk workloads, `--runtime runsc` requests an already installed/configured gVisor runtime. Circuit neither installs it nor claims it has been validated here. A managed sandbox can also use the same gateway interfaces without this runner.

Agents needing models must use a separately configured, credential-isolating model broker attached to an explicitly governed network path. There is no implicit internet exception or bundled model key. Circuit does not hand-roll model routing, an agent framework, or a sandbox kernel.

## Reproduce Tests

Requires Go, Python 3, OpenSSL, and Docker:

```sh
docker build --target gateway -f deploy/docker/Dockerfile -t circuit-onboarding-test:local .
python3 examples/sandbox-validation.py
```

The script creates an isolated throwaway TLS workspace gateway, checks restricted execution, blocked public egress, verified MCP discovery, normal/EOF/interruption cleanup, and absence of credentials from captured connector logs. CI repeats these checks. An explicit `--url --network --token-file --ca-cert` targets an existing disposable fixture instead; tests do not submit provider writes. This evidence does not qualify arbitrary agent images, production networks, model brokers, or gVisor.
