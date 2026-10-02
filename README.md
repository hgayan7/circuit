# ⚡ Circuit

> **The Circuit Breaker & Safety Proxy for Autonomous AI Agents and MCP Tooling.**  
> *Trip the breaker before an agent burns down production.*

[![CI](https://github.com/hgayan7/circuit/actions/workflows/ci.yml/badge.svg)](https://github.com/hgayan7/circuit/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/hgayan7/circuit)](https://goreportcard.com/report/github.com/hgayan7/circuit)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Latency](https://img.shields.io/badge/Evaluation%20Overhead-%3C%2085%C2%B5s-brightgreen.svg)](#benchmarks)
[![Web](https://img.shields.io/badge/Web-circuitproxy.com-purple.svg)](https://circuitproxy.com)

---

## Why Circuit?

Autonomous agents are probabilistic, non-deterministic reasoning engines. Giving them direct access to production APIs, databases, and MCP servers creates critical vulnerabilities:

1. **The Blast-Radius Dilemma:** An agent compromised via indirect prompt injection or hallucination makes calls with legitimate tokens. To an API gateway like Kong or AWS, wiping 5,000 customers looks like a normal migration.
2. **Traditional Rate Limits Fail Agents:** Gateways rate-limit on *requests per second*. A runaway agent only needs **1 bad request** to execute `DELETE /users` or cycle 10 x $500 refunds over 30 minutes.
3. **LLM Guardrails Miss Mutations:** Text guardrails (NeMo, Guardrails AI) scan prompts for strings, but cannot calculate real-world side effects once an action enters the execution domain.
4. **All-or-Nothing Bearer Tokens:** If you hand an agent a live GitHub or Stripe credential, its blast radius is whatever that key allows.

**Circuit acts as an electrical circuit breaker on the network and stdio wire:**  
It intercepts tool calls, evaluates semantic arguments using Google Common Expression Language (CEL), enforces cumulative financial and action budgets, virtualizes credentials, and parks high-risk transactions for human sign-off without dropping agent state.

---

## Architecture

```
                      [ AGENT RUNTIME ]
         (Cursor, Claude Desktop, Python, LangGraph, Node.js)
                          │
            ┌─────────────┴─────────────┐
            │                           │
    [ MCP stdio / SSE ]          [ HTTP / REST ]
    (circuit mcp wrap)          (circuit run / serve)
            │                           │
            ▼                           ▼
  ┌────────────────────────────────────────────────────────┐
  │                    CIRCUIT GATEWAY                     │
  │                                                        │
  │  • CEL Policy Engine (Google Common Expression)        │
  │  • Cumulative Action & Spend Budgets (Rolling Window)  │
  │  • Zero-Trust Token Virtualization (Ephemeral Vault)   │
  │  • Stateful Transaction Parking (Asynchronous HITL)    │
  │  • Tamper-Evident NDJSON Audit Ledger                  │
  └───────────────────────────┬────────────────────────────┘
                              │ (Only when ALLOWED or APPROVED)
            ┌─────────────────┴─────────────────┐
            ▼                                   ▼
  [ Downstream MCP Server ]            [ External REST API ]
 (Postgres, Git, Filesystem)         (Stripe, GitHub, Cloud)
```

---

## 🚀 Quickstart

### 1. Installation

**Via Homebrew (macOS & Linux):**
```bash
brew install hgayan7/tap/circuit
```

**Via Pre-Built Binary:**
Download the latest pre-compiled binary for macOS, Linux, or Windows from [GitHub Releases](https://github.com/hgayan7/circuit/releases).

**From Source:**
```bash
go install github.com/hgayan7/circuit/cmd/circuit@latest
```

### 2. Automatic Proxy Injection (`circuit run`)

Run any agent script without changing code or configuring ports. Circuit spins up an ephemeral proxy, injects `HTTP_PROXY` into the child environment, enforces policy, and tears down cleanly when done:

```bash
circuit run --policy ./circuit.yaml -- python agent.py
```

### 3. Wrap an MCP Server (`circuit mcp wrap`)

Protect local developer tools (Cursor, Claude Desktop, Antigravity) from accidental `DROP TABLE` or destructive commands:

```bash
circuit mcp wrap \
  --policy ./circuit.yaml \
  --prefix postgres \
  --audit ./mcp-audit.log \
  -- npx -y @modelcontextprotocol/server-postgres "postgresql://user:pass@localhost:5432/mydb"
```

In your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "postgres": {
      "command": "circuit",
      "args": [
        "mcp", "wrap",
        "--policy", "/path/to/circuit.yaml",
        "--prefix", "postgres",
        "--",
        "npx", "-y", "@modelcontextprotocol/server-postgres", "postgresql://..."
      ]
    }
  }
}
```

### 4. Standalone Reverse Proxy Daemon (`circuit serve`)

Run Circuit in production or Kubernetes in front of critical APIs:

```bash
circuit serve \
  --policy ./circuit.yaml \
  --target "https://api.stripe.com" \
  --port 8080 \
  --inject-token "$STRIPE_LIVE_KEY" \
  --audit ./stripe-audit.log
```

---

## Declarative Policy (`circuit.yaml`)

Policies use simple YAML with Google CEL expressions:

```yaml
version: "v1alpha1"
name: "prod-safety-circuit"
default_action: ALLOW

rules:
  # 1. MCP Database Guard: Block destructive DDL statements
  - id: "block-database-ddl"
    description: "Prevent DROP, TRUNCATE, and ALTER TABLE"
    match:
      tool: "postgres.query"
    condition: "args.sql.matches(r'(?i)(DROP\\s+TABLE|TRUNCATE|ALTER\\s+TABLE)')"
    action: DENY
    reason: "Destructive DDL operations are forbidden by Circuit policy"

  # 2. Cumulative Spend Budget: Cap refunds across a rolling 1-hour window
  - id: "hourly-spend-cap"
    match:
      endpoint: "POST /v1/refunds"
    budget:
      window: "1h"
      max_amount: 500.00
      amount_field: "args.amount / 100.0" # Stripe amount in cents
    action: ALLOW

  # 3. Micro-Approval Escalation: Park transactions > $100 for human review
  - id: "escalate-high-refund"
    match:
      endpoint: "POST /v1/refunds"
    condition: "args.amount > 10000"
    action: REQUIRE_APPROVAL
    reason: "Single refund exceeds $100 threshold"
    escalation:
      channel: "terminal"
      target: "operator"
```

Validate your rules at any time:
```bash
circuit check circuit.yaml
# ✅ Policy 'prod-safety-circuit' (version v1alpha1) compiled successfully with 3 rule(s).
```

---

## Benchmarks

Benchmarked on Apple M2 Pro (`darwin/arm64`):

```
BenchmarkEngine_Evaluate-12    14791    80604 ns/op (0.08ms per evaluation)
```

Policy validation and CEL AST evaluation incur less than **0.1ms overhead**, making Circuit practically invisible in the critical path.

---

## License

MIT License. See [LICENSE](LICENSE) for details.
