# Getting Started

Circuit is a self-hosted action gateway with CLI administration, REST/MCP integration, and a built-in operator review interface. Start with the credential-free demo, then configure the GitHub App deployment with your own credentials.

## Install The Candidate

[v0.2.0-rc.2](https://github.com/hgayan7/circuit/releases/tag/v0.2.0-rc.2) is a developer preview / release candidate. Download the archive for Linux/macOS and amd64/arm64, compare its SHA-256 with the published `checksums.txt`, and extract it. The stable Homebrew tap does not install this candidate. Native Windows gateway execution is not supported.

For the sample agent, Docker builds, and rehearsal scripts, use a source checkout. Requires Go 1.26.7 or later; the sample Python agent requires Python 3.

```sh
git clone --branch v0.2.0-rc.2 https://github.com/hgayan7/circuit.git
cd circuit
go build -ldflags '-X main.version=0.2.0-rc.2' -o bin/circuit ./cmd/circuit
bin/circuit version
```

Building without the version flag still works, but does not embed the candidate version.

## Run The Demo

```sh
bin/circuit gateway demo
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). Enter the public demo operator token printed by the command. In a second terminal from the same source checkout:

```sh
python3 examples/github-agent.py --demo
```

The agent submits simulated GitHub actions. Review the file-write and merge proposals in the interface, approve or reject them, and inspect the resulting history. The demo never contacts GitHub and requires no upstream credentials. Do not use demo credentials or its plaintext local listener in production.

## Connect A Real Agent

Use the rc.2 binary or matching source and follow [guided setup](onboarding.md): `circuit setup`, `circuit start`, then `circuit doctor` in another terminal. Register its generated agent-only `mcp.json` in your client. The optional [Docker runner](sandbox.md) uses an existing internal gateway network and your trusted agent image.

Run Circuit as a persistent gateway and configure your agent's tools to call its authenticated REST or Streamable HTTP MCP endpoint. Integration requires routing those tool calls through Circuit; it is not automatic protection for every outbound request.

The agent receives only a scoped Circuit token. Keep GitHub App credentials in the gateway, and isolate the agent from direct provider access and alternative execution paths. Circuit supplies policy enforcement, not OS isolation or model routing.

Follow the [GitHub gateway guide](github-gateway.md) for action requests, idempotency keys, approvals, and MCP connections. Use the [production-oriented deployment guide](production-deployment.md) for GitHub App configuration, TLS, named roles, and restricted Docker services.

## Bring Your Own Operations

The [operations guide](operations.md) configures SMTP alert delivery, encrypted backup retention, optional storage adapters, and recovery rehearsals. Keep credentials in protected operator-owned files and the backup decryption identity offline. Test your actual email/storage destinations and restore on a separate host before promising disaster recovery.

Use the [release checklist](release-checklist.md) for acceptance. The full 72-hour fixture soak remains pending; it tests uninterrupted read-path stability, not every intended agent workload.

## Optional Inspection Tools

The older HTTP proxy and MCP wrapper use `circuit.yaml`, separately from the action gateway's `gateway.yaml`:

```sh
bin/circuit init
bin/circuit check
bin/circuit run --policy circuit.yaml -- python agent.py
bin/circuit mcp wrap --policy circuit.yaml -- your-mcp-server
```

Proxy inspection depends on client proxy/trust configuration. Heuristic detection does not guarantee prompt-injection prevention, and these tools do not replace a sandbox. Read the [safety guide](safety.md) and [policy guide](policy.md) for coverage and first-match policy semantics.
