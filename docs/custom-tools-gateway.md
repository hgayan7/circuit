# Custom Tools Gateway Action Adapter

Circuit provides a secure, durable, and policy-governed gateway adapter for custom tools and proprietary APIs. The Custom Tools adapter enables engineering and platform teams to bring any internal microservice, custom webhook, or third-party REST/JSON API under Circuit's institutional governance model:

- **Isolated Credentials**: Target API tokens and authorization headers are stored in gateway environment variables (`auth_header_env`) and injected at dispatch time. Autonomous agents never handle or inspect raw credentials.
- **Granular Operation Scoping**: Define allowed operations (e.g. `sync_customer`, `trigger_pipeline`, `reset_cache`) per custom tool. Agents can only execute declared operations.
- **Selective Human Approvals**: Sensitive operations configured in `require_approval_ops` automatically pause execution and transition into a `pending` approval state, requiring cryptographic operator sign-off before invocation.
- **REST & MCP Tool Exposure**: Agents can interact using the unified REST API (`POST /v1/actions` with `call_custom_tool`) or native MCP tools (`custom_call` and auto-generated `custom_<operation>`).
- **Velocity Budgets**: Scoped limits (`custom_tool` and `agent_custom_tool`) constrain call volume and frequency to prevent infinite loops or service degradation.
- **Zero-Dependency Simulator Mode**: Tools configured without an endpoint (or with `mock:` / `sim:` prefixes) execute in-memory with unique call tracing, enabling hermetic local development and CI/CD testing without real external infrastructure.

---

## Configuration

Custom tools are declared in your Circuit gateway YAML configuration under `custom_tools`:

```yaml
name: enterprise-agent-gateway
admin_token_env: CIRCUIT_ADMIN_TOKEN
approval_ttl: 24h

custom_tools:
  - id: crm-service
    name: Internal CRM & Account Sync Service
    endpoint: https://crm.internal.example.com/api/v1
    auth_header_env: CRM_SERVICE_API_KEY
    operations:
      - sync_customer
      - archive_customer
      - export_audit
    require_approval_ops:
      - archive_customer
    timeout: 10s

  - id: pipeline-trigger
    name: CI/CD Pipeline Orchestrator
    endpoint: mock:pipelines
    operations:
      - trigger_pipeline
      - cancel_pipeline
    require_approval_ops:
      - trigger_pipeline
    timeout: 5s

agents:
  - id: devops-agent
    token_env: DEVOPS_AGENT_TOKEN
    custom_tools: ["crm-service", "pipeline-trigger"]
    actions:
      - call_custom_tool
      - sync_customer
      - trigger_pipeline

limits:
  - id: crm-sync-velocity
    actions: ["call_custom_tool", "sync_customer"]
    scope: agent_custom_tool
    window: 1h
    max_calls: 50
```

---

## Available Actions & MCP Tools

Circuit exposes custom tools over REST (`POST /v1/actions`) and the Model Context Protocol (MCP `/mcp`):

| Action | MCP Tool Name | Description | Required Arguments | Optional Arguments |
|---|---|---|---|---|
| `call_custom_tool` | `custom_call` | Invoke a custom tool with a specific operation and JSON payload. | `tool`, `operation` | `payload`, `method` |
| `<custom_op>` | `custom_<op>` | Dedicated MCP tool registered for each configured operation (e.g. `custom_sync_customer`). | None | `payload`, `tool` |

### REST Request Example

```json
POST /v1/actions HTTP/1.1
Host: 127.0.0.1:8080
Authorization: Bearer <DEVOPS_AGENT_TOKEN>
Content-Type: application/json

{
  "operation": "call_custom_tool",
  "tool": "crm-service",
  "args": {
    "operation": "sync_customer",
    "payload": {
      "customer_id": "cust_12345",
      "status": "active"
    }
  }
}
```

Alternatively, direct action names matching configured operations are accepted:

```json
{
  "operation": "sync_customer",
  "tool": "crm-service",
  "args": {
    "payload": {
      "customer_id": "cust_12345"
    }
  }
}
```

---

## Safety Guarantees

### 1. Zero Credential Exposure
Agent prompts, runtime contexts, and tool outputs never receive API keys or upstream authorization tokens. Circuit securely retrieves the credential from the environment variable specified in `auth_header_env` and attaches it to outbound HTTP requests inside the gateway process.

### 2. Selective Human Approvals
When an agent attempts an operation listed in `require_approval_ops` (such as `archive_customer` or `trigger_pipeline`):
1. The gateway intercepts the request and marks it as `pending`.
2. A cryptographic approval request is generated with an immutable hash of the operation arguments and payload.
3. Upstream HTTP requests are never sent until an authorized operator reviews and approves the request via the Circuit Web UI (`http://127.0.0.1:8080/`) or Admin REST API.

### 3. Velocity Limits & Scope Boundaries
Circuit enforces rate limits at two granularities:
- `custom_tool`: Bounds the total number of calls across all agents to a specific tool target.
- `agent_custom_tool`: Bounds an individual agent's calls to a specific tool target.

### 4. Built-In Simulation Mode
For local development, testing, and continuous integration, any tool configured with an endpoint starting with `mock:` or `sim:` (or an empty endpoint) executes using Circuit's simulated tool handler. It records call IDs, timestamps, operations, and payloads without opening outbound network sockets.

---

## Verification & Serving

Validate your gateway configuration:
```bash
circuit gateway check -config gateway.yaml
```

Run Circuit gateway:
```bash
export CIRCUIT_ADMIN_TOKEN="a-secure-secret-token-with-at-least-32-chars"
export DEVOPS_AGENT_TOKEN="agent-token-xyz"
export CRM_SERVICE_API_KEY="Bearer internal-secret-token"
circuit gateway serve -config gateway.yaml -addr 127.0.0.1:8080
```
