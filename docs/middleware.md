# Govern Existing Tools

Available on `main` after v0.2.0-rc.2, not in its existing downloads. Circuit governs existing Streamable HTTP MCP tools and fixed REST routes without a provider-specific execution adapter:

```text
Agent -> Circuit MCP / REST action API -> Shared enforcement core
                                                   |
                                          Approval when required
                                                   |
                                       Existing MCP server / REST API
```

Both ingress protocols use identity, scope, policy, atomic budgets, exact approvals, durable claims, and audit. This is not a transparent network proxy or automatic interception of all agent activity. Standard MCP clients can connect, but Circuit's tool envelopes expose pending approvals and uncertain outcomes explicitly.

For an enforced deployment, follow [isolated agents and generated clients](isolated-agents.md): `setup -> up -> agent run --dir`. Current-source `--production` accepts these governed transports. The host-side `start` example below tests integration only, not isolation. A manifest can contain 1-32 reviewed targets, including separately scoped model access.

## Connect An MCP Server

From current source, build Circuit and discover a candidate manifest using a gateway-owned least-privilege bearer credential:

```sh
go build -o bin/circuit ./cmd/circuit
bin/circuit gateway discover-mcp \
  --endpoint https://your-mcp-server.example/mcp \
  --token-file /private/operator/upstream-token \
  > /private/operator/upstream.yaml
```

Review the manifest and remove unwanted operations before importing it. Discovery performs initialization/listing only, never tool execution. It grants no agent permissions, trusts no read-only annotations, and does not import upstream descriptions. Add operator-authored binding descriptions when useful.

```sh
bin/circuit setup --non-interactive --integration middleware \
  --preset review-writes \
  --upstream-manifest /private/operator/upstream.yaml
bin/circuit start
```

In another terminal, run `bin/circuit doctor` and register the generated agent-only `mcp.json` in your agent client. See [guided setup](onboarding.md) for directories, TLS trust, and credential separation. Doctor verifies the agent-to-Circuit connection, not upstream behavior or isolation.

Setup grants only the manifest's operations, limits all actions to 100/hour, and approval-gated operations to five/hour. Discovery aliases such as `tool_001` map to upstream names and pinned input schemas. Rename aliases during review only if the `operations` list and `mcp_tools` keys stay aligned.

All discovered operations require approval by default. On current source after v0.2.0, [explicit gateway rules](gateway-policy.md) can authorize writes automatically while preserving their mutating classification and uncertain-outcome protections. A matching generated REQUIRE_APPROVAL rule must be narrowed or removed to permit autonomy; an ALLOW alone cannot override another matching review rule. Only after validating behavior should you explicitly add operations to `read_only_operations`; a `read-only` setup refuses an empty list. Before each execution Circuit verifies the selected tool's schema against the pinned schema. Changes fail closed and require review. Newly discovered tools never gain permissions automatically. Schema pinning cannot prove an upstream implementation's behavior.

The agent calls `forward_<target-id>_<operation>` with this envelope:

```json
{"idempotency_key":"task-42-lookup-1","args":{"sku":"item-42"}}
```

`args` is the original upstream tool input. The target, credential, and tool name are fixed by configuration. The result is a Circuit action containing its ID, state, and outcome. For `pending`, await operator approval and poll `circuit_action_status`. Reuse the original key; do not resubmit with fresh keys. For `uncertain`, stop and reconcile. Successful MCP outcomes retain the upstream MCP result in `outcome.body`.

## Register REST Routes

Use a reviewed manifest with literal routes and explicit input schemas:

```yaml
custom_tools:
  - id: inventory
    protocol: rest-routes-v1
    endpoint: https://inventory.example
    token_file: /private/operator/upstream-token
    operations: [inspect_item, reserve_item]
    read_only_operations: [inspect_item]
    routes:
      - operation: inspect_item
        method: GET
        path: /items
        query_params: [sku]
        input_schema:
          type: object
          additionalProperties: false
          required: [sku]
          properties:
            sku: {type: string}
      - operation: reserve_item
        method: POST
        path: /reservations
        input_schema:
          type: object
          additionalProperties: false
          required: [sku]
          properties:
            sku: {type: string}
```

Import it with the same middleware setup flow. A manifest contains exactly one target and no agent/policy configuration; setup generates its own credentials and grants. Relative token/CA references resolve relative to the manifest file.

An MCP agent calls `forward_inventory_reserve_item`. A REST client submits:

```http
POST /v1/actions
Authorization: Bearer <agent-only-circuit-token>
Idempotency-Key: task-42-reserve-1
Content-Type: application/json

{"operation":"reserve_item","custom_tool":"inventory","args":{"sku":"item-42"}}
```

GET/HEAD encode only declared string query parameters from `args`; POST/PUT/PATCH/DELETE send `args` as JSON. The agent cannot override origins, paths, methods, or headers. Redirects are not followed. Paths must be literal and canonical: arbitrary URL fetching and path templates are unsupported. GET/HEAD alone does not establish safety; other methods cannot be classified read-only.

Successful 2xx REST responses become durable JSON; text/empty responses are wrapped as `{"text":"..."}`. Responses are bounded and echoed upstream credentials are redacted. Non-successful or lost write responses become uncertain, not retries.

## Boundaries

- Forwarding uses gateway-owned bearer credentials distinct from agent/operator tokens, verified TLS 1.3, optional `ca_cert`, no inline authorization headers, and origin-bound authentication. Plain HTTP is loopback-only for local development.
- Upstream MCP sessions are separate: inbound session/auth context is not forwarded, while cancellation is preserved. SDK negotiation starts with the 2025-11-25 protocol. Tool schema discovery is rechecked per execution, not cached across sessions.
- REST receives `Idempotency-Key`; MCP receives `circuit/action_id` metadata. Upstreams may deduplicate with these, but Circuit does not assume they do. Ambiguous writes are not automatically replayed, even across restart. Exactly-once execution across systems is not promised.
- v0.2.0/current source accepts these governed transports under `--production`; rc.2 did not. Registering a target is not production qualification; validate its scopes, semantics, credentials, failures, and intended workload.
- Direct provider credentials or alternative network routes bypass enforcement. Use an existing isolated environment or the [Docker runner](sandbox.md). Local actions outside the gateway remain outside its control.
- Sampling, elicitation, resources/prompts, upstream stdio launching, OAuth enrollment, asynchronous tool workflows, OpenAPI import, dynamic path bindings, and replicated shared storage are not implemented. This remains a single-owner bbolt deployment.

`go test -race ./...` exercises real local MCP/REST servers, agent MCP ingress, approvals, scopes, schemas, budgets, credential separation, verified CA trust, redirects, bounded/redacted responses, discovery without execution, and lost write replies across restart. `python3 examples/middleware-validation.py` also rehearses actual CLI setup/start/doctor, verified gateway TLS, REST ingress, reviewer approval, fixed upstream dispatch, retry protection, and credential isolation. CI repeats the rehearsal. Fixture evidence does not certify arbitrary upstreams.
