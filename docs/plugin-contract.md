# Extension Contract

Circuit is a trusted enforcement core with replaceable integrations. Policy, scopes, exact approvals, budgets, durable execution claims, recovery, and audit remain in the core. An integration does not get to bypass those decisions.

## Action Plugins

The existing `custom_tools` registration supports a versioned sidecar/service protocol. No Go shared libraries or arbitrary plugin code are loaded into the gateway process. Plugins can be implemented in any language.

```yaml
custom_tools:
  - id: inventory
    protocol: circuit-plugin-v1
    endpoint: https://inventory.internal/execute
    token_file: /run/secrets/inventory-plugin-token
    operations: [inspect_item, reserve_item]
    read_only_operations: [inspect_item]
    max_timeout: 15s
agents:
  - id: inventory-agent
    token_file: /run/secrets/agent-token
    custom_tools: [inventory]
    actions: [inspect_item, reserve_item]
```

This is a nonproduction plugin pilot configuration. The initial `--production` profile remains GitHub-only until another integration passes its provider-specific release tests. Extensibility is not a claim that arbitrary plugins are production-safe.

An agent sends an explicit `custom_tool: inventory`. The agent never selects the plugin URL, transport method, credential, or protocol version. Reserved built-in operation names cannot be shadowed. Unregistered operations and ambiguous implicit plugin targets are denied. Reads may run automatically only when the operator explicitly declares them read-only; all other plugin operations require exact approval. Additional rules and budgets still apply.

The gateway sends a fixed POST with a gateway-only bearer credential and `Idempotency-Key` set to its durable action ID:

```json
{"protocol":"circuit-plugin-v1","action_id":"DURABLE_ACTION_ID","request":{"operation":"reserve_item","custom_tool":"inventory","args":{"sku":"item-123"}}}
```

The plugin returns HTTP 200 with the matching version and action ID:

```json
{"protocol":"circuit-plugin-v1","action_id":"DURABLE_ACTION_ID","outcome":{"status":200,"body":{"reservation":"receipt-123"}}}
```

`outcome.error` describes a known failure; `outcome.uncertain: true` means the provider result cannot be established. Success needs a 2xx outcome status. Redirects, malformed/oversized responses, mismatched IDs/versions, timeouts, or non-200 transport status become uncertain for writes. There is no automatic write replay, including when the client supplies a new idempotency key for the same unresolved payload.

Plugin responsibilities:

- Authenticate the gateway; bind provider credentials only to the plugin, never to the agent.
- Validate arguments and independently enforce least privilege at the provider.
- Persist action-ID deduplication and receipts atomically with writes when the provider supports it. Reject the same ID with different payloads.
- Return truthful known/unknown outcomes and bounded data. Support evidence-based reconciliation outside automatic dispatch.
- Run outside the agent sandbox; restrict network access and credential mounts. Gateway TLS requires trusted certificates, TLS 1.3, and a fixed endpoint; loopback HTTP is allowed only for local fixtures.

Exported Go DTOs are `gateway.PluginCall` and `gateway.PluginResult`; embedded adapters continue to implement `gateway.Executor`. `pkg/gateway/plugin_test.go` exercises approvals, explicit scopes, endpoint immutability, idempotency, redirects, and unsafe registrations. Add provider-specific tests for every new plugin; the generic contract cannot prove a declared read is harmless.

## Operational Plugins

Monitoring and notification integrations stay outside action dispatch. Prometheus consumes the protected metrics contract; Alertmanager supplies replaceable email, webhook, and other receiver configurations. Email is BYOK through the operator-owned config and mounted SMTP credential file.

The backup worker publishes verified age-encrypted archives into a dedicated mount. Replace that mount or add an operator-owned upload sidecar for a storage service without changing policy execution. The decryption identity is offline and never mounted into the gateway, agent, or backup worker. A remote storage uploader and its durability/retrieval tests are not implemented here.

Identity providers, new policy languages, audit sinks, and arbitrary backup drivers are not yet dynamic plugin APIs. Extend those with explicit contracts and compatibility tests when needed; do not expose arbitrary hooks into approval or execution state.
