# Guided Setup

These commands are available in v0.2.0-rc.2 and on `main`. Use a downloaded rc.2 binary, or build its source with Go 1.26.7 or later:

```sh
go build -o bin/circuit ./cmd/circuit
bin/circuit setup
```

Choose a demo or a real integration, supply your own credential-file references, and confirm the displayed permissions. Demo mode never contacts a provider. Real setup never falls back to simulation when credentials are missing.

## The Normal Flow

1. Run `circuit setup` and choose the integration and permission preset.
2. Run `circuit start` in the operator-owned environment. This starts the gateway and review UI, not an agent or sandbox.
3. In another terminal, run `circuit doctor` to check configuration, verified TLS, readiness, and agent-scoped MCP discovery.
4. Register the generated `mcp.json` server entry in an MCP-capable agent client. It invokes `circuit connect`, which forwards tool calls to the gateway. Clients that accept Streamable HTTP can use `/mcp` directly with the scoped agent token and trusted CA.
5. Review consequential proposals in the gateway UI using the separately stored reviewer/admin credential. Reuse the original idempotency key when retrying an action; inspect uncertain outcomes before acting again.

By default, files live under your OS user-configuration directory in `circuit/setup`. Use `--out /private/operator/setup` for another directory and pass the same `--dir` to `start` and `doctor`. Setup refuses to overwrite an existing directory. Keep this directory, provider credentials, TLS private keys, and operator tokens outside every agent workspace and sandbox mount.

The exported MCP configuration references only the agent token and public CA, never raw secrets, provider keys, or operator tokens. On a separate agent host, transfer only those two files through your secret-management process, restrict the token to its agent user, and adjust the connection paths. Do not copy the complete operator directory. A client path is not a security boundary if the agent can read the operator's filesystem or environment; isolate them.

## Permission Presets

| Integration | Preset | Access |
| --- | --- | --- |
| GitHub | `read-only` | Read files and PR metadata in one repository. |
| GitHub | `pr-author` | Also propose branch creation, file changes, and PR creation under `circuit/`. Each write requires approval. |
| GitHub | `pr-author-with-merge` | Also propose an exact-head-SHA merge, with approval. |
| PostgreSQL | `read-only` | Query and inspect schema in the configured database, using the gateway's least-privilege role. |
| PostgreSQL | `review-writes` | Also propose SQL mutations, with approval. |
| Workspace | `read-only` | Read/list an explicit directory. No shell or file-write capability. |
| Plugin | `read-only` | Only the explicitly declared non-mutating operations. |
| Plugin | `review-writes` | Declared operations; non-read-only calls require approval. |

Presets limit total actions to 100/hour and write-capable presets to five writes/hour. They generate editable gateway policy, not a second enforcement engine. GitHub is the only integration accepted by the existing `--production` profile. PostgreSQL, workspace, and plugin setup are real integrations but need their own deployment/workload qualification.

## Explicit Setup

GitHub App credentials must already exist in a private RSA PEM file. Install the App only on the intended repository, with the minimum provider permissions for the preset:

```sh
bin/circuit setup --non-interactive --integration github \
  --preset pr-author --repo your-org/your-repo \
  --app-id 123 --installation-id 456 \
  --key-file /private/operator/github-app.pem
bin/circuit start
```

For local read-only exploration without provider keys:

```sh
bin/circuit setup --non-interactive --integration workspace \
  --workspace /absolute/path/to/project
bin/circuit start
```

In a second terminal, `bin/circuit doctor` verifies the running connection. `doctor --offline` checks local configuration/credential files only; it does not prove readiness, provider access, approvals, or isolation. The workspace setup directory must be outside the chosen workspace, including through symlinks.

PostgreSQL uses `--integration postgres --dsn-env CIRCUIT_PG_DSN`, where the named environment variable is set only in the gateway environment. Do not place a literal DSN in that flag. Query results are limited to 100 rows and a 15-second timeout; use a least-privilege database role and SQL-specific policies. See [database integration](database-gateway.md).

Plugins use `--integration plugin --endpoint https://your-plugin/actions --plugin-token-file /private/operator/plugin-token --operations lookup,modify --read-only-operations lookup`. Only declare genuinely non-mutating operations read-only. The plugin credential is distinct from agent/operator credentials. Use the versioned [plugin contract](plugin-contract.md); registering a plugin does not certify a new provider.

## Before Deployment

Generated TLS is local-only, expires after 30 days, and is not installed in the system trust store. The browser may warn until you configure trust. Production needs managed certificate renewal and the [deployment](production-deployment.md), [operations](operations.md), and [release](release-checklist.md) checks, not just a successful `doctor` result.

Circuit does not automatically replace every tool in an agent framework. Route tools through its MCP/REST interfaces, remove alternative provider credentials, and test that bypass paths are blocked. Use your existing sandbox or the optional [Docker runner](sandbox.md). Model access belongs in a separately governed model gateway; no model routing, provider keys, or unrestricted internet are added by setup.
