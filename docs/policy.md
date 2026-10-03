# Circuit Proxy — How the Policy YAML Works

A `circuit.yaml` file is your **rulebook**. Circuit reads it at startup,
compiles every rule, and evaluates each intercepted request against them
in order — the first rule that matches wins.

---

## Full schema

```yaml
# ── Header ────────────────────────────────────────────────────────────────────
name: my-agent-guard          # required — human name for this policy
version: "1.0"                # optional — your own versioning
description: "..."            # optional — free text

default_action: ALLOW         # what to do when NO rule matches
                              # values: ALLOW | DENY  (default: ALLOW)

# ── Rules ─────────────────────────────────────────────────────────────────────
rules:
  - id: rule-id               # required — unique string identifier
    description: "..."        # optional — human explanation

    match:                    # required — what traffic this rule targets
      tool: query             # MCP tool name (supports glob: "db_*")
      endpoint: "POST /v1/chat/completions"  # HTTP method + path
      method: DELETE          # HTTP method only
      path: /v1/refunds       # HTTP path only (with method above)

    condition: |              # optional — CEL expression, must return bool
      args.amount > 10000     # only fires when amount > $100.00 (cents)

    action: DENY              # required — ALLOW | DENY | REQUIRE_APPROVAL

    reason: "Exceeds limit"   # optional — shown in audit log and CLI output

    budget:                   # optional — cumulative sliding window limits
      window: 1h              # time window: 30s, 5m, 1h, 24h, etc.
      max_calls: 100          # max number of matching requests in window
      max_amount: 5.00        # max total spend in window
      amount_field: |         # CEL expression to extract spend from args
        double(args.amount) / 100.0

    escalation:               # optional — used with REQUIRE_APPROVAL
      channel: cli            # "cli" = prompt in terminal (more coming)
      timeout: 30s            # how long to wait for human response
```

---

## How rules are evaluated

```
Incoming request
      │
      ▼
 Rule 1 — does match: match? ──── NO ──► Rule 2 ──► Rule 3 ──► ...
      │
     YES
      │
      ▼
 condition: CEL expr? ──── evaluates false ──► next rule
      │
   true (or no condition)
      │
      ▼
 budget check? ──── exceeded ──► DENY
      │
    within budget
      │
      ▼
 return action: ALLOW / DENY / REQUIRE_APPROVAL
      │
 No rule matched → default_action (ALLOW by default)
```

**First match wins.** Put your most specific rules first, catch-alls last.

---

## match — targeting traffic

### MCP tool calls

```yaml
match:
  tool: query          # exact match
  tool: "db_*"         # glob — matches db_read, db_write, etc.
```

### HTTP endpoint

```yaml
match:
  endpoint: "POST /v1/chat/completions"   # method + path, space-separated
  endpoint: "DELETE /v1/*"               # glob on the path
```

### HTTP method + path separately

```yaml
match:
  method: DELETE
  path: /v1/refunds
```

---

## condition — CEL expressions

The `condition` field is a **Google CEL** expression that gets access to
the runtime context of the intercepted call. It must return a `bool`.

### Available variables

| Variable | Type | Description |
|---|---|---|
| `tool` | `string` | MCP tool name (e.g. `"query"`) |
| `endpoint` | `string` | Combined `"METHOD /path"` |
| `method` | `string` | HTTP method (`"POST"`, `"DELETE"`) |
| `path` | `string` | HTTP path (`"/v1/chat/completions"`) |
| `args` | `map<string, any>` | Tool arguments or request body fields |
| `session_id` | `string` | Optional agent session identifier |
| `agent_id` | `string` | Optional agent identifier |

### Examples

```yaml
# Block DROP/TRUNCATE SQL from Postgres MCP
condition: 'args.query.matches("(?i)^\\s*(DROP|TRUNCATE|ALTER)")'

# Only fire when Stripe charge > $100 (amount is in cents)
condition: 'int(args.amount) > 10000'

# Block requests from a specific agent
condition: 'agent_id == "untrusted-agent-42"'

# Block only if both conditions are true
condition: 'method == "DELETE" && path.startsWith("/v1/users")'
```

> CEL is type-safe and pre-compiled at startup — evaluation overhead
> is **< 85µs** per request.

---

## action — what happens when a rule fires

| Value | Behaviour |
|---|---|
| `ALLOW` | Request is forwarded immediately |
| `DENY` | Request is blocked, error returned to agent |
| `REQUIRE_APPROVAL` | Pauses the request, prompts a human, then forwards or denies |

---

## budget — sliding window limits

Budgets track cumulative usage over a rolling time window, **per rule**.

```yaml
budget:
  window: 1h          # sliding window size
  max_calls: 100      # block after 100 matching requests in the window
  max_amount: 5.00    # block after $5.00 total spend in the window
  amount_field: |     # CEL expression — how to extract spend from args
    double(args.amount) / 100.0
```

- `window` supports Go duration strings: `30s`, `5m`, `1h`, `24h`
- `max_calls` and `max_amount` are independent — either can trigger a block
- Budget is tracked **per session** if `session_id` is set, **per agent** if
  `agent_id` is set, otherwise globally per rule

---

## default_action

What happens when no rule matches the request:

```yaml
default_action: ALLOW   # let it through (default)
default_action: DENY    # block everything not explicitly allowed
```

> **Tip:** Start with `default_action: ALLOW` while writing rules.
> Switch to `default_action: DENY` for a strict allowlist once you're confident.

---

## Complete examples

### OpenAI — action cap

```yaml
name: openai-guard
version: "1.0"
default_action: ALLOW

rules:
  - id: cap-openai-actions
    description: Limit OpenAI requests to 50/hour
    match:
      endpoint: "POST /v1/chat/completions"
    action: ALLOW
    budget:
      window: 1h
      max_calls: 50
```

Monetary limits require a valid `amount_field`. Token-based OpenAI cost accounting is not implemented.

### Postgres MCP — block destructive SQL

```yaml
name: postgres-guard
version: "1.0"
default_action: ALLOW

rules:
  - id: block-ddl
    description: Block DROP, TRUNCATE, ALTER statements
    match:
      tool: query
    condition: 'args.query.matches("(?i)^\\s*(DROP|TRUNCATE|ALTER)")'
    action: DENY
    reason: "Destructive DDL statements are not permitted"

  - id: approve-deletes
    description: Require human approval for DELETE statements
    match:
      tool: query
    condition: 'args.query.matches("(?i)^\\s*DELETE")'
    action: REQUIRE_APPROVAL
    escalation:
      channel: cli
      timeout: 30s
```

### Stripe — cap charge amount + require approval for large charges

```yaml
name: stripe-guard
version: "1.0"
default_action: ALLOW

rules:
  - id: block-large-charges
    description: Block charges over $500
    match:
      endpoint: "POST /v1/charges"
    condition: 'int(args.amount) > 50000'
    action: DENY
    reason: "Single charge exceeds $500 limit"

  - id: approve-medium-charges
    description: Require approval for charges $100–$500
    match:
      endpoint: "POST /v1/charges"
    condition: 'int(args.amount) > 10000'
    action: REQUIRE_APPROVAL
    escalation:
      channel: cli
      timeout: 60s

  - id: hourly-spend-cap
    description: Total Stripe spend capped at $50/hour
    match:
      endpoint: "POST /v1/charges"
    action: ALLOW
    budget:
      window: 1h
      max_amount: 50.00
      amount_field: "double(args.amount) / 100.0"
```

---

## Validate before running

Always check your policy compiles cleanly:

```bash
circuit check
# ✅ Policy 'stripe-guard' (version 1.0) compiled successfully with 3 rule(s).
```

CEL syntax errors are caught at compile time — not at runtime when your
agent is already running.

## Optional safety layer

See [the safety guide](./safety.md) for HTTPS inspection, request/result injection checks, and parser-based shell/SQL guards. These checks run before first-match CEL action selection. Match fields are combined with AND; `host` is also available for HTTP traffic.
