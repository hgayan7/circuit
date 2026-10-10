# Autonomous execution rules

On current source after v0.2.0, operators can explicitly authorize supported actions through gateway rules. An `ALLOW` rule permits execution without a human prompt, including REST POST/PUT/PATCH/DELETE, model inference routes, plugin writes, and native actions. Published v0.2.0 binaries retain the previous mandatory approval behavior.

## Decision order

1. Authenticate the agent and validate its operation, target, scope, and configured safety checks. A rule cannot grant access outside these boundaries.
2. Evaluate all matching gateway rules. `DENY` wins; otherwise any matching `REQUIRE_APPROVAL` wins; otherwise a matching `ALLOW` authorizes autonomous execution. Rule order does not change that precedence. Evaluation errors deny the action.
3. When no rule matches, retain the existing scoped-action defaults. Merges, shell commands, file deletion/overwrite, governed writes, and actions covered by target approval settings default to review.
4. Reserve and recheck all matching budgets before dispatch. Approval expiry, policy changes, provider preconditions, schema checks, and durable execution claims still apply.

Target flags such as `require_approval`, production-environment review, and payment approval thresholds supply defaults. An explicit matching `ALLOW` overrides these approval defaults. To make a review requirement take precedence over other allow rules, encode it as a `REQUIRE_APPROVAL` rule. Transaction caps, read-only executor restrictions, and provider permissions remain enforced.

Rules belong to the operator-owned gateway configuration, not an agent request or an upstream manifest. Keep that file outside the agent workspace. Guided setup continues to generate conservative review rules; edit those rules to express your intended autonomy. Adding an `ALLOW` alongside a matching generated `REQUIRE_APPROVAL` still requires review.

## Example: ticket reservations

Assume a reviewed booking target declares `reserve_seats` and `purchase_tickets`. The agent also needs these operations in its `actions` and the target in its `custom_tools` allowlist. Add these rules and limits to its gateway configuration:

```yaml
rules:
  - id: reserve-seats
    actions: [reserve_seats]
    condition: 'agent_id == "booking-agent" && custom_tool == "booking"'
    action: ALLOW
  - id: small-purchase
    actions: [purchase_tickets]
    condition: 'agent_id == "booking-agent" && custom_tool == "booking" && args.amount_minor > 0 && args.amount_minor <= 200000'
    action: ALLOW
  - id: review-large-purchase
    actions: [purchase_tickets]
    condition: 'custom_tool == "booking" && args.amount_minor > 200000'
    action: REQUIRE_APPROVAL
  - id: reject-invalid-amount
    actions: [purchase_tickets]
    condition: 'custom_tool == "booking" && args.amount_minor <= 0'
    action: DENY
limits:
  - id: purchase-count
    actions: [purchase_tickets]
    scope: agent_custom_tool
    window: 1h
    max_calls: 5
```

Use a pinned input schema requiring integer `amount_minor`, a fixed currency, and a confirmation reference. These are illustrative operation names; Circuit does not ship a ticket-booking adapter. Five calls per hour is an action budget, not a monetary budget.

Customer purchase consent must be checked by the trusted booking service or plugin. It should validate an application-issued confirmation bound to customer, show, seats, amount, expiry, and a single purchase. An agent-provided `confirmed: true` is not trusted consent. This policy change does not implement customer sessions, payment authorization, or confirmation issuance.

## Write outcomes and retries

Autonomous authorization does not classify writes as reads. Keep REST write methods and MCP/plugin write operations classified as mutating. An ambiguous response remains `uncertain`, consumes its execution reservation, and cannot be automatically redispatched. Reuse the original idempotency key and reconcile with provider evidence.

`approved_by: policy` identifies automatically authorized actions. Human approval remains bound to the exact stored payload. GitHub merges still require the requested head SHA to match the provider head even when authorized by policy.

## Upgrade from v0.2.0

Review existing `ALLOW` rules before upgrading: broad allow rules that previously could not bypass built-in approval now authorize those matching actions automatically. Replace blanket allows with scoped conditions and explicit review rules where needed. Configurations without matching `ALLOW` rules retain their approval defaults. The API and action-state schema are unchanged; follow the [upgrade procedure](compatibility.md) for persisted state.

The legacy proxy/inspection configuration uses a different first-match schema; see [proxy policy](policy.md). These combined-rule semantics apply to the action gateway.
