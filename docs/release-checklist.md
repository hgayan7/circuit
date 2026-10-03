# BYOK Release Qualification

Circuit is a self-hosted BYOK gateway, not a hosted service with vendor-provided credentials. Plugins extend integrations; they cannot bypass core policy, approval, budget, durable claims, or audit checks.

## Supported Release Scope

- Production-oriented profile: single-process gateway, GitHub App-backed action integration, TLS 1.3, named operator tokens, bbolt state, REST/MCP and approval UI.
- Operational extensions: Prometheus, Alertmanager email BYOK, age-encrypted hourly backups, optional rclone storage adapter with authenticated S3-compatible conformance evidence.
- PostgreSQL execution: real database integration tested; operators still need least-privilege roles and query-specific policies. Not enabled by the GitHub-only production profile.
- Shell execution requires an external OS sandbox. Simulation-only adapters and arbitrary third-party plugins are not production-supported merely because they register successfully.
- One process owns state. No distributed replicas, SSO/MFA, exactly-once provider delivery, or universal cross-version migration guarantee.

## Engineering Evidence

- CI runs full Go race tests/vet, live PostgreSQL, reachable vulnerability checking, restricted Docker builds, real ENOSPC, authenticated S3 recovery, and Linux/macOS cross-platform builds.
- [GitHub and local workflows](validation-status.md), [deployment recovery](production-validation-results.json), [email/backups](operations-validation-results.json), [actual-image upgrade/rollback](upgrade-validation-results.json), and [S3 storage](archive-validation-results.json) retain bounded evidence.
- The 72-hour read-only GitHub fixture soak is a time-dependent gate, not an accelerated test. Read the running worker's `/report`; require `multi_day_soak_complete=true` and zero failures. A restart resets continuity.
- Independent security review was deferred by the owner, not performed. Automated security checks do not imply an external certification.

## Operator Acceptance

1. Supply your GitHub App, repository scopes, separate agent/operator credentials, trusted certificates, and restrictive policies. No fixture keys in production.
2. Supply SMTP and independently durable storage credentials through protected files. Test actual firing/resolved email and complete remote readback.
3. Escrow the offline age identity and receipts independently. Restore on a separate host, reconcile provider activity, and record achieved RPO/RTO.
4. Run the intended agent workload in staging, inspect approval/rejection/uncertain-action behavior, complete the uninterrupted soak, and validate capacity/expiry monitoring.
5. Pause agents, rehearse the exact release image upgrade/rollback, preserve evidence, and designate an operator for incident response.

Do not label a deployment production-qualified until its acceptance checks and the full soak are recorded. Developers can configure and evaluate the BYOK release without giving the product owner their credentials.
