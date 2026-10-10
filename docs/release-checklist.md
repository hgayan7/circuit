# BYOK Release Qualification

Circuit is a self-hosted BYOK gateway, not a hosted service with vendor-provided credentials. Plugins extend integrations; they cannot bypass core policy, approval, budget, durable claims, or audit checks.

## Supported Release Scope

The v0.3.0 scope includes explicit policy-authorized autonomous writes and the trusted-consent booking example, alongside the [generated isolated deployment and clients](isolated-agents.md): gateway-only Docker/runc namespaces, governed MCP/REST/plugin production targets, and OpenAPI-generated TypeScript/Python/Go clients with explicit stop/wait semantics. The older rc.2 binaries do not contain these additions.

- Production-oriented profile: single-process gateway, GitHub App or governed MCP/REST/plugin targets, TLS 1.3, named operator tokens, bbolt state, REST/MCP and approval UI. Mandatory routing is qualified only for the generated Docker/runc deployment, not unrestricted SDK hosts.
- Operational extensions: Prometheus, Alertmanager email BYOK, age-encrypted hourly backups, optional rclone storage adapter with authenticated S3-compatible conformance evidence.
- PostgreSQL execution: real database integration tested; operators still need least-privilege roles and query-specific policies. The native executor remains outside `--production`.
- Shell execution requires an external OS sandbox. Simulation-only adapters and arbitrary third-party plugins are not production-supported merely because they register successfully.
- One process owns state. No distributed replicas, SSO/MFA, exactly-once provider delivery, or universal cross-version migration guarantee. See the [compatibility and upgrade policy](compatibility.md).
- Release binaries target Linux/macOS amd64/arm64. Native Windows is not supported by the gateway process-isolation implementation; use a Linux host/container instead.

Review existing broad `ALLOW` rules before upgrading from v0.2.0; they can now authorize matching actions automatically. Matching `DENY` and `REQUIRE_APPROVAL` rules still take precedence. See [v0.3.0 release notes](releases/v0.3.0.md) and [gateway policy](gateway-policy.md).

## Engineering Evidence

- CI runs full Go race tests/vet, live PostgreSQL, reachable vulnerability checking, restricted Docker builds, real ENOSPC, authenticated S3 recovery, Linux/macOS cross-platform builds, clean packed-SDK installations, and previous-stable-release upgrade/rollback compatibility.
- Publishing requires green main CI for the exact tagged commit. Draft binary/SDK archives are then downloaded, checksum-verified, and exercised before public repository publication. Release archives include deployment templates and qualification documentation; prerelease version tags remain marked prerelease. No external registry or Homebrew publication is allowed.
- [GitHub and local workflows](validation-status.md), [deployment recovery](production-validation-results.json), [email/backups](operations-validation-results.json), [actual-image upgrade/rollback](upgrade-validation-results.json), and [S3 storage](archive-validation-results.json) retain bounded evidence.
- **Owner waiver, 2026-10-05:** the uninterrupted 72-hour soak is skipped for this product release. It is not completed or passed. The original exercise still requires `multi_day_soak_complete=true` and zero failures to claim completion; a restart resets continuity. Multi-day reliability remains unverified.
- Independent security review was deferred by the owner, not performed. Automated security checks do not imply an external certification.

## Operator Acceptance

1. Supply your GitHub App, repository scopes, separate agent/operator credentials, trusted certificates, and restrictive policies. No fixture keys in production.
2. Supply SMTP and independently durable storage credentials through protected files. Test actual firing/resolved email and complete remote readback.
3. Escrow the offline age identity and receipts independently. Restore on a separate host, reconcile provider activity, and record achieved RPO/RTO.
4. Run the intended agent workload in staging, inspect approval/rejection/uncertain-action behavior, and validate capacity/expiry monitoring. The product-release soak waiver does not establish reliability for that deployment; record its own soak evidence or explicit risk acceptance.
5. Pause agents, rehearse the exact release image upgrade/rollback, preserve evidence, and designate an operator for incident response.

Do not label a deployment production-qualified until its acceptance checks and any reliability exceptions are recorded. A stable product version is not independent certification or acceptance of every BYOK provider. Developers can configure and evaluate the BYOK release without giving the product owner their credentials. Test email destinations are restricted to `hgayan7@gmail.com`.
