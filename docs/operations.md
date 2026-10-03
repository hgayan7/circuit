# Operational Extensions

The optional Docker operations layer supplies Prometheus, Alertmanager, encrypted hourly backups, and a 72-hour read-only fixture soak. Action integrations use the separate [plugin contract](plugin-contract.md). These services do not replace the gateway's policy/approval/audit core.

## Email BYOK

Copy `deploy/docker/email.example.yaml` into the private deployment directory as `alertmanager.yaml`. Configure your SMTP host/port, sender, username, and recipient there. Supply an operator-owned credential file using `CIRCUIT_SMTP_PASSWORD_FILE`; mount only that file into Alertmanager. It must be readable by Alertmanager's UID while protected by its host parent directory. Do not put passwords in YAML or command-line arguments.

The template requires SMTP TLS with certificate verification. Adjust the minimum TLS version to your provider's supported secure version if needed; never disable certificate verification. Prometheus and Alertmanager interfaces are loopback-only and intended for a trusted operator host, not direct public exposure. A remote deployment needs access control at its ingress.

```bash
export CIRCUIT_DEPLOY_DIR=/private/circuit-staging
export CIRCUIT_SMTP_PASSWORD_FILE=/private/secrets/smtp-password
docker compose -p circuit-staging -f deploy/docker/compose.yaml \
  -f deploy/docker/operations.compose.yaml -f deploy/docker/email.compose.yaml \
  build backup
docker compose -p circuit-staging -f deploy/docker/compose.yaml \
  -f deploy/docker/operations.compose.yaml -f deploy/docker/email.compose.yaml \
  up -d gateway prometheus alertmanager backup
```

Prometheus scrapes with an observer token and explicit gateway CA. Alertmanager supports replacing the email receiver with another supported notification channel without modifying Circuit. Configuration follows the [Prometheus](https://prometheus.io/docs/prometheus/latest/configuration/configuration/) and [Alertmanager](https://prometheus.io/docs/alerting/latest/configuration/) contracts.

The local test uses Mailpit, without real email credentials or external recipients. [Validation evidence](operations-validation-results.json) includes firing and resolved messages caused by a real gateway outage. This proves the local delivery pipeline, not your mail provider's authentication, delivery, spam filtering, or availability. Run a delivery rehearsal after supplying BYOK settings.

## Protected Backups

```bash
circuit gateway backup-keygen \
  --identity-out /private/offline/backup-identity \
  --recipient-out /private/circuit-staging/secrets/backup-recipient
```

Keep the private identity offline and escrow another protected copy independently. Only the public recipient goes to the backup worker. Encryption uses the [age library](https://pkg.go.dev/filippo.io/age), not a custom encryption format.

The worker downloads a verified snapshot, encrypts it, then publishes a private archive into its dedicated volume. Plaintext exists only in its private `/tmp` tmpfs during verification/encryption. Default tmpfs capacity is 128 MiB, so increase it with matching memory limits if snapshots grow. Failed downloads/encryption never replace a successful archive. Retention runs only after successful publication and touches only owned regular `circuit-<timestamp>-<random>.db.age` files.

Defaults: hourly backups, 168 retained archives, alerts after two hours without success. `CIRCUIT_BACKUP_INTERVAL` and `CIRCUIT_BACKUP_RETAIN` configure cadence/count; adjust the stale rule with the cadence. Failure/age/worker-availability alerts are enabled. Prometheus retains seven days, capped at 256 MiB.

```bash
circuit gateway restore --backup /private/archive.db.age \
  --identity-file /private/offline/backup-identity \
  --out /private/state/new-restored.db
```

Decryption authenticates the complete stream before verified state publication. Restore still pauses dispatch for provider reconciliation. Wrong keys, truncation, tampering, existing destinations, and unsafe identity permissions fail closed.

Local Docker volume retention is not disaster recovery. Copy encrypted archives to independently durable storage with an operator-owned uploader; keep the private key separate. Remote upload and restore-from-remote tests remain required for a deployment that promises host-loss recovery.

## Soak And Upgrade

`examples/operations-validation.py --deployment-dir <fixture-directory> --start-soak` validates local monitoring, retention, encrypted restore, and SMTP delivery, then starts the long fixture worker. It deliberately stops the fixture gateway briefly. Never point it at a customer deployment.

`examples/upgrade-validation.py --deployment-dir <fixture-directory>` pauses workers, upgrades to the current image, rolls back to the actual previous image on current state, verifies history/idempotency/readiness, returns to the new image, then restarts the soak. [Upgrade evidence](upgrade-validation-results.json) is specific to those two image versions and the GitHub state schema, not arbitrary cross-version compatibility.

The soak worker performs a harmless authorized GitHub PR read every minute. Its report is stored in the separate `soak-status` volume and exported on the internal `/report` endpoint. The `complete` and `multi_day_soak_complete` fields remain false until the actual uninterrupted duration passes without failed samples or detected timing gaps. Startup rejects trusted certificates that expire before the run plus a one-minute margin. The local harness renews its own fixture certificate for seven days when needed; it never changes system trust. Restarting the worker resets its continuity clock and report. Completed workers remain idle so their evidence can be scraped; they do not automatically begin another run.

Inspect with `docker compose ... exec -T soak wget -qO- http://127.0.0.1:9091/report`. The worker has only the agent token and trusted CA, no App credentials, operator tokens, backup keys, or gateway state. Backups have admin access but no provider key or decryption identity. Do not run untrusted agent code in the operator services.

This is fixture-read soak evidence, not the intended real agent workload. Keep Docker and the host running; timing gaps or failures invalidate success. Host disk capacity, certificate expiry, off-host backup copies, SMTP delivery health, SSO/MFA needs, and provider-specific plugin tests must be addressed for the actual deployment. Independent review was explicitly deferred by the operator, not completed.
