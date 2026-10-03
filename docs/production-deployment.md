# Single-Instance GitHub Deployment

Production-oriented foundation, not production certification. The first deployment profile supports one gateway process, GitHub App authentication, named operator tokens, TLS, and an independently isolated agent. [Docker validation](production-validation-results.json) passed 23 checks and a 60-second soak. The [operations layer](operations.md) adds tested email delivery, encrypted retention, image upgrade/rollback evidence, and a running multi-day fixture soak. Independent review was deferred by the operator, not completed.

## Local Docker

```bash
python3 examples/production-validation.py \
  --app-id 5174435 --private-key-file /private/path/to/test-app.pem \
  --repo hgayan7/circuit-gateway-pilot-20261003 \
  --pr 3 --duration-seconds 60 --keep-running
```

The fixture harness builds images, creates a private deployment directory and temporary certificate, tests recovery, and optionally leaves the gateway running. It prints the loopback HTTPS URL and credential directory. It permits only authorized hgayan7 fixture repositories and performs real reads, not approved provider writes. The browser must trust its local test certificate; API and agent tests trust only that explicit certificate. Never install a test CA system-wide automatically.

For another deployment, adapt `deploy/docker/gateway.example.yaml` to your least-privilege App, repositories, and operator identities. Set `CIRCUIT_DEPLOY_DIR` to an operator-owned mode-0700 directory with `gateway.yaml` and `secrets/`: `github-app.pem`, distinct `admin-token`, `reviewer-token`, `observer-token`, `agent-token`, `tls.crt`, and `tls.key`. Generate tokens with `circuit gateway token`. Use a client-trusted certificate with SANs for the actual gateway addresses. Never commit credentials.

Files must be readable by container UID 10001. The fixture uses read-only 0444 mounts beneath the private host directory, not publicly accessible host directories. Use a managed secret system with correct ownership on a real host.

```bash
export CIRCUIT_DEPLOY_DIR=/private/circuit-staging
export CIRCUIT_BIND_PORT=8443
docker compose -p circuit-staging -f deploy/docker/compose.yaml build
docker compose -p circuit-staging -f deploy/docker/compose.yaml up -d gateway
```

The host port is loopback-only. Containers use nonroot UID, read-only root filesystems, dropped capabilities, no-new-privileges, and resource limits. Only the gateway state volume is writable; no Docker socket or host-home mounts are supplied. The sample agent gets only its token and certificate on an [internal Docker network](https://docs.docker.com/reference/compose-file/networks/#internal); the gateway also has an upstream network.

This is a deployment template, not an agent framework. Run the real agent inside that network without host shell/Docker access, provider credentials, gateway configuration, or state mounts. Isolation depends on that deployment. Keep bbolt on local block-backed storage, not a shared network filesystem. Never run two gateway processes on one state file.

`--production` requires a real GitHub App, named operators including an admin, GitHub-only targets, and `--tls-cert`/`--tls-key`. It rejects simulation and other target types in this initial profile. Other adapters remain available for bounded nonproduction pilots.

## Operators And Rotation

| Role | Access |
| --- | --- |
| observer | Action/event history, identity, metrics |
| reviewer | Observer access plus exact approval/rejection |
| admin | Reviewer access plus reconciliation and complete-state backup |

Each `operators` entry has an ID, role, and exactly one `token_file` or `token_env`. Agents support those credential sources too. Named operators disable the legacy shared admin token. Decisions record `operator:<id>` and `approved_by`; `GET /admin/me` returns identity/role. UI controls reflect the role, while server checks enforce it independently.

These are named bearer identities, not SSO/MFA accounts. Do not share tokens. Rotate by replacing a secret file and recreating the gateway, then verify old-token 401 and new-token success. In-flight claimed writes may finish during drain. Immediate revocation requires stopping the service; it cannot undo an already accepted provider write. Removing an identity or changing policy invalidates outstanding approvals.

For App keys: add a new key, replace the gateway-only mount, recreate, verify a scoped live read/token exchange, then revoke the old key in GitHub. Live cache refresh is tested; destructive App-key revocation is not exercised by the fixture harness.

## Monitoring

- `/healthz`: unauthenticated liveness, no sensitive details.
- `/readyz`: local-state readiness; 503 during drain, latched storage failure, or pending restore acknowledgment. It does not check GitHub availability or free disk space.
- `/admin/metrics`: operator-authenticated Prometheus counters and state/storage/restore/drain gauges. Request counters reset on restart; action gauges use durable state.
- JSON request logs contain normalized method/route, status, and duration, not raw URLs, headers, bodies, tokens, or provider responses. Sensitive payloads still exist in protected state.

Scrape with a dedicated observer token file. The optional [operations overlay](operations.md) connects Prometheus and Alertmanager with tested local SMTP delivery and a BYOK production email template. Monitor disk space, certificate expiry, and restarts externally. Provider failures can arrive in HTTP-200 action records: monitor failed/uncertain gauges, not only HTTP 5xx.

## Backup And Restore

Backups use consistent [bbolt transaction snapshots](https://pkg.go.dev/go.etcd.io/bbolt#Tx.WriteTo), verified before streaming. They contain full payloads/results: encrypt and access-control them outside the state volume. Downloaded files are private and never implicitly replace existing files.

```bash
circuit gateway backup --url https://127.0.0.1:8443 \
  --ca-cert /private/circuit-staging/secrets/tls.crt \
  --token-file /private/circuit-staging/secrets/admin-token \
  --out /private/backups/circuit-unique.db
circuit gateway restore --backup /private/backups/circuit-unique.db \
  --out /private/state/restored.db
```

Restore verifies a new destination and pauses dispatch. **Older snapshots cannot know about later provider writes.** Recovery procedure:

1. Stop agents and the old gateway. Never run old and restored instances together.
2. Start the restored path: history/UI are available, but proposals/approvals are blocked and readiness is 503. Previously executing actions become uncertain.
3. Compare provider activity and independent operator records with the snapshot, including writes absent from it. Account for missing post-backup history and consumed quotas. Never resubmit missing writes blindly.
4. Reconcile uncertain actions with concrete evidence. If post-backup activity or budgets cannot be established, do not resume agent access. Apply additional policy/budget restrictions or a new tightly scoped agent identity as needed.
5. Stop the restored gateway, record evidence offline, then restart:

```bash
circuit gateway acknowledge-restore --data /private/state/restored.db \
  --note 'Provider activity reviewed through incident cutoff; later writes and remaining budgets accounted for in incident record INC-123.'
```

Acknowledgment requires exclusive offline state access and records the offline state owner, not an SSO reviewer. It does not reconstruct missing history or prove the review was correct. Each restoration requires fresh reconciliation. Define backup retention and recovery objectives before production use.

## Incident And Upgrade Runbook

**Unknown outcome:** preserve action ID/idempotency key; do not retry with a new key. Inspect provider state. Admin reconciliation needs evidence. Signed webhooks resolve only exact-head merges.

**Disk full/read-only/I/O failure:** failures latch dispatch off and readiness becomes 503. Stop, preserve state, repair/free storage, verify state, then restart. Failed validation requires a known-good backup and restore procedure. A bounded 2 MiB Docker tmpfs test verifies real ENOSPC without exhausting host storage; it does not cover every hardware fault.

**Corrupt state:** startup checks database structure, required buckets, action digests/states, and referenced records. It fails instead of initializing over damaged state. Preserve the original and restore to a new path. Hash checks are not encryption or an externally anchored tamper-evident ledger.

**Upgrade/rollback:** pause agents; export a verified backup; stop the old instance; test the new image; start exactly one instance on current state. Check readiness, history, and a harmless scoped read before resuming. Roll back to a compatible previous binary on current state when possible. Restoring old state always requires post-backup reconciliation. Cross-version/schema migration testing remains a gate.

**Shutdown:** SIGTERM drains new requests and waits up to 35 seconds; claimed execution has a 30-second timeout and survives agent disconnect. Forced exits recover claimed actions as uncertain. Docker allows 45 seconds. Manual `docker kill` can suppress restart policy; the harness explicitly starts the container afterward.

## Remaining Gates

The completed foundation includes roles/attribution, TLS 1.3, mounted credentials, Docker isolation, monitoring endpoints/logs, verified backups, paused restore, rotation tests, process-kill tests, corruption checks, read-only storage, and real tmpfs ENOSPC testing.

The updated runtime/dependencies pass `govulncheck` with no reported reachable Go vulnerabilities. The initially reported dependency advisories were addressed by security updates. This is not an independent audit or complete container-image scan.

Before an actual deployment release: finish the 72-hour soak and test the intended real agent workload, configure and rehearse BYOK email, copy encrypted backups to independently durable storage and test host-loss recovery, and monitor disk/certificate capacity. Upgrade/rollback was rehearsed for the recorded images on current GitHub state, not arbitrary future schemas. Independent review was explicitly deferred. Broader organizations may also require SSO/MFA, reviewer quorum, identity-specific repository scopes, and distributed storage.

`--duration-seconds 259200` runs the original long fixture exercise. The operations overlay also supplies a persistent Docker soak worker with periodic isolated GitHub reads. Reports claim multi-day completion only after the full duration passes without failures or continuity gaps. Keep the host awake and use a sufficiently long-lived certificate. The worker is started by the operations/upgrade rehearsal scripts, not a Codex scheduler.
