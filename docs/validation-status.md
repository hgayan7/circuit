# Validation Status

Validated locally on 2026-10-03. Circuit is ready for a bounded pilot of the real adapters below, not an unrestricted production release. A passing policy simulator does not establish provider support.

## Evidence

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
- Every shell command requires approval. Read-only workspaces reject shell execution. Shells do not inherit gateway credentials; this is not OS isolation.
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

## Remaining Release Gates

- Run a multi-day staging soak with the intended agent sandbox and deployment topology. Test crash/kill points, disk full, backup/restore, key rotation/revocation, and provider outages. The current restart and fault tests are bounded scenarios, not exhaustive failure testing.
- Keep shell executors in an external OS sandbox. File roots do not prevent hard-link, mount, privileged-process, or shell-based host access.
- Use least-privilege database roles. Static analysis does not fully model views, custom operators, triggers, or function bodies. `max_affected_rows` bounds reported affected rows, not all indirect side effects.
- Validate non-merge reconciliation procedures with operators. Circuit prevents automatic replay of claimed writes, not exactly-once network delivery.
- Add individual reviewer identity/SSO, operational alerts, protected backups, retention controls, and deployment monitoring before wider production use. The current shared operator token and single-process bbolt store are pilot limitations.
- Implement and live-test each additional provider before advertising cloud, communication, or payment integration support. Static custom-tool headers are operator configuration; environment-backed header injection and universal response-secret redaction are not implemented.

The private GitHub App remains installed on the two authorized fixture repositories only. Its PEM remains local with restricted permissions. Temporary public tunnels and repository hooks were removed after testing; no product repository received App access.
