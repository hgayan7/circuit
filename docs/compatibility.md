# Compatibility And Upgrade Policy

This policy defines the supported contract. It is not a claim that every previous binary, provider, or state schema has been qualified. Nothing is published by the local packaging scripts; distribution is limited to validated assets in this repository.

## Client Contract

- The action API uses `/v1/actions`; the OpenAPI document is contract version `1.0.0`. SDK package versions are separate from API contract versions.
- Compatible additions may add optional fields or operations. Existing request fields, action-state meanings, authentication, idempotency, and approval binding must not silently change in a compatible contract update.
- A breaking wire change requires a new API major version and explicit migration instructions. Pre-1.0 product releases still need release notes for configuration or deployment changes; a product version alone does not promise cross-version storage compatibility.
- Clients must inspect `action.state`, not just HTTP success. Unknown states, uncertain outcomes, and unexpected responses stop automation. Stable idempotency keys identify one logical request; never create a new key to recover an uncertain write.
- Maintained facades are qualified together with the candidate gateway. Other generated bindings need their own TLS, timeout, state, and no-redispatch acceptance tests.

## Current-source policy change after v0.2.0

Explicit matching gateway ALLOW rules now override built-in and target approval defaults. DENY and matching REQUIRE_APPROVAL rules still take precedence; scope, safety checks, budgets, provider preconditions, and uncertain-write protections remain enforced. Review existing broad ALLOW rules before upgrading because they can now authorize writes that previously required review. Configurations without matching ALLOW rules keep their defaults. Published v0.2.0 binaries retain the old behavior. See [gateway policy](gateway-policy.md) for examples and migration guidance.

## Supported Deployment Boundary

One process owns each bbolt state file. The locally tested enforced deployment uses Docker Linux containers with `runc`, gateway-only TCP/8443 egress, read-only workspace mounts, and separate provider/operator credentials. Native Windows, distributed replicas, other runtimes, and equivalent Kubernetes/VM isolation are not qualified by these tests.

Provider manifests and agent images are operator-reviewed inputs. An SDK installed on an unrestricted host is not a mandatory execution boundary. Registered services still require acceptance with the operator's actual credentials, scopes, and workload.

## Upgrade Procedure

1. Pin the previous binary/image, candidate binary/image, configuration, and checksums. Read the candidate's scope and configuration changes.
2. Pause agents and wait for in-flight execution to finish. Export and verify a backup, and record any uncertain actions for provider-side reconciliation. Keep the backup private.
3. Stop the old gateway. Test the candidate on a disposable copy using a fixture or non-mutating provider credentials; never give a copied live store permission to replay production writes.
4. Start exactly one candidate process on the current state. Verify readiness, history, role isolation, scoped reads, idempotency, and pending/uncertain states before resuming agents.
5. Rehearse rollback before accepting the deployment. If changing schemas or introducing a feature the old binary cannot parse, do not assume downgrade is supported.

## Rollback Procedure

- Pause agents and stop the candidate before starting a compatible previous binary on the **current** state.
- The local rc.2/candidate rehearsal covers unchanged legacy fixture configuration, persisted pending/denied/succeeded/uncertain actions, idempotency, and no automatic replay. It does not qualify rc.2 for new REST/MCP routes, generated deployment files, or future schema changes.
- New-feature deployments must roll back to a version that supports those features. Do not use rc.2 as a fallback for gateway-only orchestration or governed REST routes it does not implement.
- If current state cannot be read by the fallback, use the documented verified restore procedure. Restored state is paused until an operator reconciles provider activity since the snapshot and acknowledges restore offline. Never replace current state with an old backup and immediately resume writes.
- Moving an operator directory or switching from host state to a generated Docker volume is a migration, not an upgrade shortcut. `up` refuses host history rather than silently starting an empty store.

See [deployment and restore](production-deployment.md) for commands and [local artifact testing](local-release-testing.md) for reproducible checks.
