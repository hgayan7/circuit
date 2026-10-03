# Custom HTTP Tools Gateway

Custom tools dispatch JSON to a configured HTTP endpoint. Local integration tests cover successful calls, timeouts, failures, and a lost write response that remains uncertain across restarts. This is not validation of arbitrary third-party APIs.

```yaml
name: custom-gateway
custom_tools:
  - id: internal-api
    name: Internal API
    endpoint: http://127.0.0.1:9090/actions
    operations: [sync_customer]
    require_approval: true
    max_timeout: 10s
agents:
  - id: assistant
    token_env: CIRCUIT_AGENT_TOKEN
    custom_tools: [internal-api]
    actions: [call_custom_tool, sync_customer]
```

REST requests use `custom_tool: internal-api`, `operation: call_custom_tool`, and `args: {operation: sync_customer, payload: {...}}`. A configured operation can also be used directly as the action name. MCP exposes `custom_call` and `custom_<operation>` tools. Limits support `custom_tool` and `agent_custom_tool` scopes.

`require_approval: true` gates every operation on that tool. Use CEL rules for selective approval. Approval binds the exact request digest; it is not an individual reviewer's digital signature.

Simulation must be explicit: set `endpoint: mock:fixture` or `endpoint: sim:fixture`. Results include `simulated: true`. Empty endpoints are rejected, not treated as simulators.

The implemented credential configuration is a static `headers` map in the operator-owned YAML. Environment-backed header injection is not implemented. Protect the configuration file and do not mount it into the agent. Upstream responses can contain secrets and must be trusted or filtered; no universal response-secret prevention is promised.

```bash
circuit gateway check gateway.yaml
circuit gateway serve --config gateway.yaml --listen 127.0.0.1:8080
```

See [validation status](validation-status.md) for evidence and remaining limits.
