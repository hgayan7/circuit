# Circuit expanded build plan

Historical planning document. Current adapter support and tested behavior are tracked in [validation status](validation-status.md).

## Product promise and USP
Circuit applies configurable safety checks to agent HTTP requests and MCP tool calls before execution, and checks untrusted text responses before returning them to the agent. The intended advantage is one local, framework-independent enforcement point combining transport inspection, content detection, and structured action checks.

## This increment
1. HTTP forward proxy with HTTPS CONNECT inspection using a locally generated CA. No system trust-store changes: only child processes receive the CA trust environment. Verify upstream TLS normally. Keep fixed-target reverse proxy mode available.
2. Local prompt-injection detector for instruction overrides, role spoofing, and exfiltration language. Inspect request arguments and HTTP/MCP text responses. Return explicit reasons. This is heuristic detection, not a guarantee against general prompt injection; unknown and obfuscated attacks remain possible.
3. Parse shell commands and SQL before making a safety decision. Default guarded shell execution to explicit read-oriented commands, reject dynamic expansion, redirection, and unsupported syntax. Default guarded SQL to read-only PostgreSQL-compatible statements and approved functions. Deny parse failures. This checks submitted text; it is not an OS sandbox or database permission replacement.
4. Repair wizard generation and regression-check onboarding. Add meaningful adversarial and local integration tests before handoff.

## Enforcement boundary
Only cooperating HTTP clients and tools routed through Circuit are covered. Clients ignoring proxy configuration, pinned TLS, QUIC, raw sockets, custom trust stores, and direct credentials need separate integration or network/sandbox enforcement. Payload inspection has bounded sizes and rejects unsupported inspection streams rather than silently passing them. Safety controls are opt-in in YAML to preserve existing deployments.

## Acceptance
- HTTPS request reaches its original local test origin and policy denial prevents the origin receiving it.
- Untrusted upstream certificates fail; no insecure upstream verification.
- Benign text passes and known instruction injection is blocked in both requests and tool results.
- Compound shell commands, substitutions, redirects, SQL multi-statements, writable CTEs, and malformed inputs are covered by adversarial tests.
- Every wizard preset validates through the real config loader and CEL compiler.
- Race tests, vet, and cross-platform compilation pass.

## Later validation
Run real SDK/MCP client compatibility tests and build an independently curated detection corpus. Measure false positives, false negatives, and response buffering overhead before making production or broad detection claims. Durable approvals, identity, and budget composition remain separate follow-up work.
