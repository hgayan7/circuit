# Circuit GitHub action gateway

Circuit gives an unattended engineering agent a bounded allowance to act on selected GitHub repositories. The gateway holds the GitHub credential. Each agent receives its own Circuit credential, explicit repository/action permissions, branch restrictions, and durable limits. An operator reviews consequential actions in a separate web interface.

## Try a local simulation

```sh
go build -o bin/circuit ./cmd/circuit
bin/circuit gateway demo
```

Open http://127.0.0.1:8080 and enter the public demo operator token printed by the command. This command never contacts GitHub. It uses temporary state and simulated outcomes; shutdown removes its state.

In another terminal, run the supplied workflow:

```sh
python3 examples/github-agent.py --demo
```

The script creates a branch, proposes a file change, opens a PR, and requests a merge. File writes and merges pause in the review queue. Review the full payload and click **Approve this exact action**. The script polls action status with its agent credential and continues after approval. Approval does not require keeping the original request open.

The demo credentials are public and cannot be used with a production deployment.

## Configure a real repository

```sh
bin/circuit gateway init --repo your-org/your-repo
bin/circuit gateway check gateway.yaml

export CIRCUIT_ADMIN_TOKEN="$(bin/circuit gateway token)"
export CIRCUIT_AGENT_TOKEN="$(bin/circuit gateway token)"
# Set GITHUB_TOKEN securely in the gateway environment.
bin/circuit gateway serve --config gateway.yaml --data .circuit/gateway.db
```

Keep `GITHUB_TOKEN` and the operator token in the gateway/operator environment. Give the agent only `CIRCUIT_AGENT_TOKEN` and the gateway URL. Use a fine-grained GitHub token restricted to the configured repositories and required Contents, Pull requests, and Issues permissions. GitHub may require additional organization authorization. Existing GitHub branch protections still apply.

Keep this server on loopback, or deploy it behind authenticated TLS with restricted network access. Circuit's bearer authentication separates agent/operator roles but this initial server does not terminate HTTPS itself. Never use the public demo credentials for real repositories.

Use an existing OS/container sandbox to keep the agent from reading gateway credentials, modifying the gateway database, or making unrestricted direct requests. This gateway does not create that sandbox. An agent that already has independent GitHub credentials can bypass it.

## Policy and scope

`gateway.yaml` uses a separate schema from the older `circuit.yaml` inspection policies. Agent repositories are exact names; repository names are normalized to lowercase. Each agent's supported `actions` is an explicit allowlist. New tool types require reviewed adapters.

Supported operations:

| Operation | Required arguments | Optional arguments |
| --- | --- | --- |
| `read_file` | `path`, `ref` | — |
| `get_pr` | `number` | — |
| `create_branch` | `branch`, `sha` | — |
| `put_file` | `path`, `branch`, `content` (base64), `message` | `sha` for updating an existing file |
| `create_pr` | `title`, `head`, `base` | `body`, `draft` |
| `merge_pr` | `number`, `sha` (exact head commit) | `merge_method` |
| `create_issue` | `title` | `body` |
| `update_issue` | `number`, at least one changed field | `title`, `body`, `state` |

Branch creation, file writes, and PR heads must use the configured `branch_prefix` (default `circuit/`). Canonical relative file paths are required. File writes and merges cannot modify `.github/workflows`, `.github/actions`, or `.git`. Merge inspection checks changed and previous filenames, rejects more than 1,000 changed files, rechecks the PR head, and supplies GitHub's commit SHA precondition on the merge request.

All matching gateway rules combine. DENY overrides ALLOW and REQUIRE_APPROVAL; any matching REQUIRE_APPROVAL requires review. ALLOW does not bypass scope or limits. Merges always require operator approval, even if an ALLOW rule matches. CEL conditions receive `args`, `action`, `repository`, and `agent_id`; runtime evaluation failures deny the action.

Limits combine atomically with scope and approvals. Supported scopes are `global`, `agent`, `repository`, and `agent_repository`. Limits count reserved/executed action attempts, not tokens or money. Pending proposals reserve capacity. Rejected, expired, or pre-execution policy-denied proposals release it. Executed attempts, including failures and uncertain outcomes, retain their usage for the window. Capacity is rechecked at approval and dispatch; reservations are renewed for the execution window. This prevents bypassing limits through queued approvals or changing a session header.

## Agent REST API

Submit a structured action:

```sh
curl http://127.0.0.1:8080/v1/actions \
  -H "Authorization: Bearer $CIRCUIT_AGENT_TOKEN" \
  -H "Idempotency-Key: task-42-open-pr" \
  -H 'Content-Type: application/json' \
  -d '{"operation":"create_pr","repository":"your-org/your-repo","args":{"title":"Fix issue 42","head":"circuit/fix-42","base":"main"}}'
```

The response includes an action ID, state, exact request digest, policy digest, expiry, reason, and execution outcome when available. Poll `GET /v1/actions/{id}` with the same agent credential. `GET /v1/actions` returns that agent's latest 100 actions; other agents' records are inaccessible.

Use a stable idempotency key for each intended action and persist it in your agent's task state. The same key and payload return the same action without another dispatch. Reusing a key for a changed payload fails. Identical unresolved actions also share the original action even if a client supplies another key. After a completed action, a new key can deliberately create another identical operation; semantic duplicates across different payloads are not automatically inferred.

## MCP connection

The server exposes Streamable HTTP MCP at `/mcp` using the official Go SDK. Configure your client with the gateway URL and an Authorization bearer header containing its Circuit agent token. Available tools are `github_<operation>` for that agent's action allowlist plus `circuit_action_status`.

Each GitHub tool takes `repository`, `args`, and `idempotency_key`. The tool description lists its operation-specific argument schema. An approval-required call returns a durable `pending` action immediately. The agent should poll `circuit_action_status` instead of generating new proposals. MCP sessions are stateless; authentication and identity are checked on every HTTP request.

## Operator approval and reconciliation

The web interface accepts the operator token and keeps it in tab memory, not browser storage. It shows up to 100 recent actions, payloads, commit SHAs, expiry, and event history. Reviewers can approve, reject, or reconcile uncertain outcomes. Agents cannot call operator endpoints.

Operator API:

- `GET /admin/actions`
- `GET /admin/actions/{id}/events`
- `POST /admin/actions/{id}/decision` with `{"digest":"...","decision":"approve"}` or `reject`
- `POST /admin/actions/{id}/reconcile` with `{"digest":"...","state":"succeeded","note":"Verified PR #42 exists"}` or `failed`

Approval authorizes the exact stored request for its configured lifetime (default one hour, maximum 24 hours). The policy version must still match at approval and execution. Reconfiguration invalidates old pending approvals. Changes to a PR's head invalidate its approved merge. Operator attribution is currently one authenticated `operator` role; individual reviewers and SSO are future work.

## Durability and retry semantics

A private bbolt database stores actions, atomic budget reservations, idempotency keys, and transition history with synchronous commits. The database allows one gateway process at a time. Back up the state and protect its filesystem access; it contains complete action payloads and upstream results. It is not an encrypted or externally anchored tamper-evident ledger.

Before an upstream call, Circuit persists `executing`. Completed calls become `succeeded` or `failed`. Transport failures and ambiguous write responses become `uncertain`. On restart, any leftover `executing` action becomes `uncertain` and will not be replayed. Pending approvals and usage persist.

GitHub does not offer a universal idempotency mechanism for all these operations. Therefore Circuit promises no automatic replay of a claimed action, not exactly-once delivery. A crash before dispatch can leave an action uncertain even if nothing happened. Verify the GitHub state and reconcile it through the operator interface before proposing a replacement. An operator reconciliation records the verified outcome; it does not retry the original operation.

Optional prompt checks use the existing heuristic detector. If a completed action's returned content is blocked, the action remains succeeded with a warning and a withheld result; it is not mislabeled as a failed write. Detection limitations are documented in [safety.md](safety.md).

## Validation and current limits

`go test -race ./...` covers real SDK MCP calls, the REST approval workflow, concurrent retries, multiple budgets, restart persistence, uncertain outcomes, payload integrity, stale policy/commit approvals, cross-agent access, and a full GitHub workflow against a local HTTP origin.

The [live GitHub pilot](github-pilot.md) passed 43 checks across private and protected public fixture repositories, including real merges, stale-SHA rejection, enforced required status checks, and restart persistence. It used an existing CLI OAuth credential; repository-scoped token and GitHub App permissions still need validation before unattended production deployment. History and budget queries scan retained records; this initial single-process implementation targets small pilots, not a multi-region control plane. There is no GitHub App installation flow, webhook reconciliation, retention management, distributed storage, notification integration, or sandbox provisioner yet.
