# Getting Started with Circuit

Circuit enforces policies, budgets, and approvals on every outbound call
your AI agent makes — with **zero changes to your agent code**.

## Install

```bash
brew install hgayan7/circuit/circuit
```

Or from source:

```bash
go install github.com/hgayan7/circuit/cmd/circuit@latest
```

---

## Step 1 — Generate your policy

Run the interactive wizard in your project directory:

```bash
circuit init
```

It asks 5 questions and writes a `circuit.yaml` tailored to your stack:

```
⚡ Circuit Init — generate a circuit.yaml for your project

How are you running your agent?
  1) circuit run   — wrap an agent process (HTTP APIs)
  2) circuit mcp   — wrap an MCP stdio server (tool calls)
Choice [1]: 1

What API or server are you protecting?
  1) OpenAI  2) Anthropic  3) Postgres MCP  4) Stripe  5) Custom
Choice [1]: 1

Add spending/action budgets? [y/N]: y
  Max actions per hour [100]: 50
  Max spend per hour in USD [5.00]: 2.00

Enable tamper-evident audit log? [y/N]: y
Require human approval for sensitive actions? [y/N]: n

✅ Created ./circuit.yaml
```

## Step 2 — Validate your policy

```bash
circuit check
# ✅ Policy 'openai-guard' (version 1.0) compiled successfully with 2 rule(s).
```

## Step 3 — Run your agent through Circuit

Pick the mode that matches your setup:

---

### Mode A — HTTP Agent (Python, Node, Go, etc.)

```bash
circuit run -- python agent.py
circuit run -- node agent.js
circuit run -- go run ./agent
```

Circuit starts an ephemeral proxy and injects `HTTP_PROXY` into your agent's
environment automatically. Every outbound HTTP call is intercepted — no SDK,
no import, no code change.

```
Agent process              Circuit proxy           Upstream API
python agent.py   ──────►  127.0.0.1:XXXX  ──────► api.openai.com
                   (HTTP_PROXY injected)    (policies enforced)
```

With options:

```bash
# Use a specific policy file
circuit run --policy ./policies/strict.yaml -- python agent.py

# Write an audit log
circuit run --audit ./logs/agent.ndjson -- python agent.py

# Both
circuit run --policy strict.yaml --audit agent.ndjson -- python agent.py
```

---

### Mode B — MCP Server (Claude Desktop, Cursor, etc.)

Edit your `claude_desktop_config.json` — wrap the MCP server command:

```json
{
  "mcpServers": {
    "postgres": {
      "command": "circuit",
      "args": [
        "mcp", "wrap", "--",
        "npx", "-y", "@modelcontextprotocol/server-postgres",
        "postgresql://localhost/mydb"
      ]
    }
  }
}
```

Circuit sits between the LLM host and the MCP server. Every `tools/call`
JSON-RPC message is evaluated against your `circuit.yaml` before being
forwarded.

```
Claude Desktop  ──►  circuit mcp wrap  ──►  MCP Server
                     (policies enforced)
```

---

### Mode C — Persistent Proxy Daemon (Docker / k8s)

```bash
circuit serve --target https://api.openai.com --port 8080
# ⚡ Circuit proxy listening on http://localhost:8080 -> https://api.openai.com
```

Point your agent at it:

```bash
HTTP_PROXY=http://localhost:8080 python agent.py
```

Docker Compose example:

```yaml
services:
  circuit:
    image: ghcr.io/hgayan7/circuit   # coming soon
    command: serve --target https://api.openai.com
    volumes:
      - ./circuit.yaml:/circuit.yaml

  agent:
    build: .
    environment:
      HTTP_PROXY: http://circuit:8080
    depends_on: [circuit]
```

---

## Next steps

- **[How the policy YAML works →](./policy.md)**
- **[Example policies →](../examples/policies/)**
- **[CLI reference →](./cli.md)**
