# 🛡️ si-shield

> **The Zero-Trust Security Sidecar & Blast-Radius Gateway for AI Agents and MCP Tooling.**

[![Go Report Card](https://goreportcard.com/badge/github.com/himshikhargayan/si-shield)](https://goreportcard.com/report/github.com/himshikhargayan/si-shield)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Latency](https://img.shields.io/badge/Policy%20Latency-%3C%2085%C2%B5s-brightgreen.svg)](#benchmarks)

---

## The Problem

Traditional security solutions fail when facing autonomous LLMs:
1. **API Gateways (Kong, AWS API Gateway)** inspect the *Token*, not the *Intent*. An agent with write permissions deleting 10,000 records looks identical to a senior engineer performing a routine migration.
2. **Traditional Rate-Limiting** counts *requests per minute*. A runaway agent only needs **1 request** to wipe a production database or issue 50 unmonitored refunds.
3. **LLM Guardrails (NeMo, Guardrails AI)** scan text strings for toxicity and prompt injection, but are blind to real-world side effects once an action enters the execution domain.
4. **All-or-Nothing Bearer Tokens:** Giving an agent direct API keys or database credentials creates a catastrophic blast radius if the agent hallucinates or is manipulated.

---

## The Solution: `si-shield`

`si-shield` is an ultra-fast, language-agnostic, zero-trust security sidecar that sits between autonomous agent runtimes and downstream execution environments (MCP servers, databases, and REST APIs).

```
                      [ AGENT RUNTIME ]
             (Claude, Cursor, Python/LangGraph, Node.js)
                          │
            ┌─────────────┴─────────────┐
            │                           │
     [ MCP stdio / SSE ]         [ HTTP / REST ]
            │                           │
            ▼                           ▼
  ┌────────────────────────────────────────────────────────┐
  │                   SI-SHIELD GATEWAY                    │
  │  • CEL Policy Evaluator (Google Common Expression)     │
  │  • Cumulative Session Budgets (Financial / Action)     │
  │  • Ephemeral Token Virtualization (Zero-Trust Vault)   │
  │  • Stateful Asynchronous HITL (Micro-Approvals)        │
  │  • Tamper-Evident NDJSON Audit Log                     │
  └───────────────────────────┬────────────────────────────┘
                              │ (Only when ALLOWED or APPROVED)
            ┌─────────────────┴─────────────────┐
            ▼                                   ▼
  [ Downstream MCP Server ]            [ External REST API ]
 (Postgres, Git, Filesystem)         (Stripe, GitHub, Cloud)
```

---

## Key Features & USPs

1. **Semantic Blast-Radius & Action Budgets:** Rate-limit by *cumulative dollars, deletion counts, or mutation frequency* over sliding time windows (e.g. `max_amount: 500.00` per hour).
2. **Zero Code Refactoring:** Operates at the network and stdio wire level. Drop it in front of existing tools using standard `HTTP_PROXY` or wrap MCP servers directly.
3. **Google CEL Policy Engine:** Declarative, non-Turing complete, memory-safe rules compiled down to bytecode, evaluating in under **85 microseconds**.
4. **Human-in-the-Loop (HITL) Micro-Approvals:** Suspends high-risk transactions, renders a visual diff/payload to the human operator (CLI prompt or Slack), and seamlessly resumes execution without dropping agent context.
5. **Zero-Trust Token Virtualization:** The agent interacts only with mock tokens. The gateway mints and injects ephemeral, real downstream credentials only *after* policy authorization.

---

## ⚡ Quick Start

### 1. Installation

```bash
git clone https://github.com/himshikhargayan/si-shield.git
cd si-shield
go build -o /usr/local/bin/si-shield ./cmd/si-shield
```

### 2. Wrapping an MCP Server (Cursor / Claude Desktop)

Protect your production database from accidental `DROP TABLE` or mass `DELETE`:

```bash
si-shield wrap \
  --policy ./examples/policies/postgres_guard.yaml \
  --prefix postgres \
  --audit ./mcp-audit.log \
  -- npx -y @modelcontextprotocol/server-postgres "postgresql://user:pass@localhost:5432/mydb"
```

Add to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "postgres": {
      "command": "si-shield",
      "args": [
        "wrap",
        "--policy", "/path/to/postgres_guard.yaml",
        "--prefix", "postgres",
        "--",
        "npx", "-y", "@modelcontextprotocol/server-postgres", "postgresql://..."
      ]
    }
  }
}
```

### 3. Outbound HTTP Reverse Proxy (Stripe, GitHub, SaaS)

Run `si-shield` in front of production APIs:

```bash
si-shield serve \
  --policy ./examples/policies/stripe_billing.yaml \
  --target "https://api.stripe.com" \
  --port 8080 \
  --inject-token "$STRIPE_SECRET_KEY" \
  --audit ./stripe-audit.log
```

Now direct your agent to `http://localhost:8080/v1/refunds` instead of hitting Stripe directly. The agent only holds mock credentials; `si-shield` validates budget caps, requests operator approval on high amounts, and injects the live secret key downstream.

---

## Policy Examples

### Guarding MCP Postgres

```yaml
version: "v1alpha1"
name: "postgres-guard"
default_action: ALLOW

rules:
  # Block destructive DDL
  - id: "block-ddl"
    match:
      tool: "postgres.query"
    condition: "args.sql.matches(r'(?i)(DROP\\s+TABLE|TRUNCATE|ALTER\\s+TABLE)')"
    action: DENY
    reason: "Destructive DDL operations are blocked by security policy"

  # Require human sign-off on user updates
  - id: "require-signoff-mutations"
    match:
      tool: "postgres.query"
    condition: "args.sql.matches(r'(?i)UPDATE\\s+users')"
    action: REQUIRE_APPROVAL
    reason: "Direct updates to users table require operator approval"
```

### Cumulative Action & Spending Budget

```yaml
version: "v1alpha1"
name: "stripe-budget"
default_action: ALLOW

rules:
  # Max $500 total spend across any 1-hour rolling window
  - id: "refund-budget-cap"
    match:
      endpoint: "POST /v1/refunds"
    budget:
      window: "1h"
      max_amount: 500.00
      amount_field: "args.amount / 100.0"
    action: ALLOW

  # Require approval for single refunds > $100
  - id: "large-refund-signoff"
    match:
      endpoint: "POST /v1/refunds"
    condition: "args.amount > 10000"
    action: REQUIRE_APPROVAL
    reason: "Single refund exceeds $100 threshold"
```

---

## Benchmarks

Benchmarked on Apple M2 Pro (`darwin/arm64`):

```
BenchmarkEngine_Evaluate-12    14791    80604 ns/op (0.08ms per evaluation)
```

Policy validation and CEL AST evaluation incur less than **0.1ms overhead**, making `si-shield` virtually invisible in the execution critical path.

---

## License

MIT License. See [LICENSE](LICENSE) for details.
