# Validation Status

Validated locally on 2026-10-03. Circuit is ready for a bounded pilot of the real adapters below, not an unrestricted production release. A passing policy simulator does not establish provider support.

## Evidence

Current main (newer than rc.2) additionally includes a generated Docker deployment with a dedicated gateway-only namespace firewall, OpenAPI wire bindings and safety-aware TypeScript/Python/Go clients. See [isolated agents](isolated-agents.md) and their reproducible validation scripts. `--production` now accepts governed MCP/REST/plugin transports as well as GitHub App targets; this configuration gate is not universal provider certification or a completed soak. Legacy method/path overrides are disabled and legacy custom HTTP is excluded from that profile.

| Surface | Verified | Evidence |
| --- | --- | --- |
| GitHub App + isolated agent | 44 checks: branches, approved files, reads, PRs, merges, issues, stale-head rejection, branch protection, scope denial, budgets, idempotency, restart persistence. Agent had no upstream credentials, credential files, or upstream network egress. | [App pilot report](github-app-pilot-results.json) |
| App scope and refresh | A live installation token could list exactly the requested fixture repository. Forced local cache expiry caused a fresh successful token exchange. | `TestGitHubAppLiveScopeAndRefresh` |
| Signed GitHub delivery | 12 checks, including a deliberately discarded successful merge response, uncertain retry without replay, exact approved-head webhook recovery, persisted attribution, and GitHub delivery status 200. Public proxy exposed only POST `/webhooks/github`. | [Webhook report](webhook-validation-results.json) |
| PostgreSQL 18.6 | Real query truncation, affected-row rollback, commit, denied tables, nested mutations/functions, timeout, PostgreSQL role permission failure, read-only target, public schema metadata, and official SDK MCP over HTTP. | `TestPostgresLive`; dedicated PostgreSQL 18 CI job |
| Workspace | Exclusive file publication, explicit overwrite review, escaping symlink parents, concurrent symlink replacement, read-only shell denial, minimal shell environment, mandatory approval for interpreter/redirect bypass attempts. | `hardening_test.go`, `workspace_test.go` |
| CLI / REST / browser | 14 checks through the actual gateway binary: PostgreSQL reads/rollback, overwrite controls, agent denied operator access, uncertain custom HTTP write, no replay, browser exact-payload approval and evidence reconciliation, audit history, restart and budget persistence. | [Local workflow report](local-validation-results.json) |
| MCP | Official Go SDK Streamable HTTP sessions against real local files and PostgreSQL, plus GitHub HTTP-origin fixtures and scope tests. | `http_test.go`, `workspace_test.go`, `TestPostgresLive` |
| Cloud / communication / payments | Simulator policy and MCP registration tests only. No actual provider calls. | Unit tests; explicit `simulation: true` required |
| Custom HTTP | Local HTTP origins, response loss, failure and timeout tests. Not arbitrary provider certification. | `custom_test.go`, local workflow report |

The complete suite passed with `go test -race ./...` and a real PostgreSQL DSN enabled. `go vet ./...` passed. GitHub CI also passed the race suite and dedicated PostgreSQL/MCP job in [the validation run](https://github.com/hgayan7/circuit/actions/runs/37125101934). Cross-platform builds gate on both jobs.

## Compatibility Changes

- Cloud, communication, and payment targets require top-level `simulation: true`. Successful simulator bodies say `simulated: true`; provider state remains in memory, not the durable action store.
- Database simulation requires `driver: mock`. Real databases need credentials and a successful startup connection check. An absent DSN never selects a simulator.
- Custom simulation requires an explicit `mock:` or `sim:` endpoint. Empty endpoints fail.
- Shell commands default to approval. On current source after v0.2.0, explicit gateway ALLOW rules can authorize supported actions automatically; see [gateway policy](gateway-policy.md). The recorded earlier pilots exercised the prior approval behavior. Read-only workspaces reject shell execution. Shells do not inherit gateway credentials; this is not OS isolation.
- File replacement requires `overwrite: true` and approval. Implicit replacement returns a conflict.
- Automatic webhook reconciliation is limited to merged PR events matching repository, PR number, and exact approved head SHA. Other uncertain actions require operator evidence.

## Reproduction

Use a disposable database owned by a fixture user: the live test creates and removes `circuit_pilot_items`, `circuit_pilot_secrets`, and a fixture role. Never point it at a production database.

```bash
export CIRCUIT_TEST_POSTGRES_DSN='host=127.0.0.1 port=55439 user=circuit_fixture dbname=circuit_validation sslmode=disable'
go test -race ./... -count=1
go vet ./...

export CIRCUIT_PILOT_APP_ID=5174435
export CIRCUIT_PILOT_APP_KEY=/private/path/to/test-app.pem
export CIRCUIT_PILOT_REPO=hgayan7/circuit-gateway-pilot-20261003
go test -race ./pkg/gateway -run TestGitHubAppLiveScopeAndRefresh -count=1 -v

python3 examples/github-pilot.py --help
python3 examples/local-validation.py
```

The GitHub script requires Docker and an operator-authenticated `gh` account. Use its `--app-id`, `--private-key-file`, and `--isolated` flags for the App pilot. It performs real writes only on the fixture repositories. The operator credential is used for fixture setup; it is not given to the gateway or agent.

`examples/webhook-validation.py` requires the App variables above, authenticated `gh`, and `CIRCUIT_PILOT_WEBHOOK_URL` pointing to the temporary signed-only proxy on port 55442. It creates and removes a temporary repository hook and merges a synthetic PR. It deliberately discards one successful merge response. It is a test harness, not a production gateway mode. Repository webhook delivery was validated; GitHub App-level webhook registration was not separately validated.

## Evidence And Qualification Exceptions

The [production-oriented Docker foundation](production-deployment.md) and [23-check staging report](production-validation-results.json) cover named operator roles, TLS, credential rotation, verified backup/paused restore, and a 60-second soak. Process-kill, corruption, read-only storage, and real bounded Docker ENOSPC tests also pass. The [operations evidence](operations-validation-results.json) covers encrypted scheduled backups/retention and firing/resolved SMTP fixture notifications. [Upgrade evidence](upgrade-validation-results.json) covers the recorded old/new image pair on current state. The 72-hour soak has not passed; the owner waived it for this product release on 2026-10-05. Independent review was explicitly deferred. Versioned [plugin conformance tests](plugin-contract.md) pass; arbitrary plugin providers are not certified by those tests.

The [local/artifact qualification](local-release-testing.md) additionally installs packed Python/Node/Go clients in clean consumer directories, exercises the actual TLS gateway, and rehearses rc.2 upgrade/rollback with unchanged fixture configuration. It checks history, pending/denied/uncertain states, idempotency, budget reservations, and no automatic replay. The [compatibility policy](compatibility.md) limits what this evidence proves. CI repeats these checks; exact draft release artifacts must pass their own checks before publication.

- Run a multi-day staging soak with the intended agent sandbox and deployment topology. Test crash/kill points, disk full, backup/restore, key rotation/revocation, and provider outages. The current restart and fault tests are bounded scenarios, not exhaustive failure testing.
- Keep shell executors in an external OS sandbox. File roots do not prevent hard-link, mount, privileged-process, or shell-based host access.
- Use least-privilege database roles. Static analysis does not fully model views, custom operators, triggers, or function bodies. `max_affected_rows` bounds reported affected rows, not all indirect side effects.
- Validate non-merge reconciliation procedures with operators. Circuit prevents automatic replay of claimed writes, not exactly-once network delivery.
- Named bearer identities/roles, metrics and sample alert rules, and verified backups are implemented. SSO/MFA, connected alert delivery, protected off-volume backups, retention controls, and multi-instance storage remain release work. The single-process bbolt store is an explicit deployment limit.
- Implement and live-test each additional provider before advertising cloud, communication, or payment integration support. Static custom-tool headers are operator configuration; environment-backed header injection and universal response-secret redaction are not implemented.

The private GitHub App remains installed on the two authorized fixture repositories only. Its PEM remains local with restricted permissions. Temporary public tunnels and repository hooks were removed after testing; no product repository received App access.
