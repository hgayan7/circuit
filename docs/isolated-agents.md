# Isolated Agents And Generated Clients

This is the recommended **v0.2.0/current-source** flow, newer than v0.2.0-rc.2. It combines the existing action gateway with a fail-closed Docker network boundary and generated clients. Use a matching checkout and binary. These changes do not retroactively qualify rc.2 or complete the long soak; the owner waived that soak, not its evidence, for this release. See the [qualification checklist](release-checklist.md).

## What Circuit Does

Circuit is the only network destination reachable by the isolated agent. It authenticates the agent, checks target/operation scope, applies policies and budgets, records the action durably, obtains any required exact approval, and dispatches using gateway-owned provider credentials. REST, MCP and generated clients all reach this same action core.

```text
Your agent/app (Python, TypeScript, Go, JVM, .NET, or another runtime)
    | agent-only token; REST SDK or MCP
    | restricted network namespace: only gateway TCP/8443
    v
Circuit gateway <---- operator review UI
    | scopes -> policy/budgets -> exact approval -> durable execution claim
    | gateway-only upstream credentials
    v
Declared MCP servers / fixed REST routes / provider plugins / GitHub App
```

The agent may choose not to call Circuit, but it cannot reach another network destination through this deployment. It can compute and write temporary files, not mutate its read-only host workspace. This is action containment, not a guarantee of correct model reasoning or universal prompt-injection detection.

## First Integration

Prerequisites: Go matching `go.mod`, Docker Engine/Desktop with Linux containers, Docker Compose v2 supporting `--wait`, and a reviewed agent image. Build the two trusted images once:

```sh
go build -o bin/circuit ./cmd/circuit
docker build --target gateway -f deploy/docker/Dockerfile -t circuit-gateway:local .
docker build --target boundary -f deploy/docker/Dockerfile -t circuit-boundary:local .
```

Create an operator-owned manifest outside your agent workspace. This example is a template, not a bundled working provider. Replace the endpoint, credential path and schema with your service's real contract; the token must be a private file with at least 32 characters. Short provider keys need a gateway-owned authenticating plugin rather than weakening the Circuit credential contract.

```yaml
custom_tools:
  - id: inventory
    protocol: rest-routes-v1
    endpoint: https://inventory.example.com
    token_file: ./inventory-token
    operations: [lookup, reserve]
    read_only_operations: [lookup]
    routes:
      - operation: lookup
        method: GET
        path: /items
        query_params: [sku]
        input_schema:
          type: object
          additionalProperties: false
          required: [sku]
          properties: {sku: {type: string}}
      - operation: reserve
        method: POST
        path: /reserve
        input_schema:
          type: object
          additionalProperties: false
          required: [sku]
          properties: {sku: {type: string}}
```

Then:

```sh
bin/circuit setup --non-interactive --integration middleware \
  --upstream-manifest /private/operator/upstreams.yaml --preset review-writes \
  --out /private/operator/circuit
bin/circuit up --dir /private/operator/circuit
bin/circuit doctor --dir /private/operator/circuit
bin/circuit agent run --dir /private/operator/circuit \
  --image YOUR_REVIEWED_AGENT_IMAGE --workspace /path/to/clean/workspace \
  -- python agent.py
```

Use absolute paths appropriate to your machine. `--dir` removes the need to supply network names, gateway URLs, agent-token paths or CA paths to the runner. The image must contain your app and its dependencies. For MCP, it must also contain the Circuit connector binary; set your client's MCP configuration to `/run/circuit/mcp.json`. For REST clients, the runner supplies:

```text
CIRCUIT_GATEWAY_URL=https://gateway:8443
CIRCUIT_TOKEN_FILE=/run/circuit/agent-token
CIRCUIT_CA_CERT=/run/circuit/ca.crt
CIRCUIT_MCP_CONFIG=/run/circuit/mcp.json
```

Do not pass provider keys, operator keys, Docker sockets or privileged host mounts to the agent. Only the reviewed workspace and individual agent connection files are mounted. The boundary container, not the agent, receives `NET_ADMIN`. Its firewall blocks public/upstream/host connections, other gateway ports, UDP, Docker DNS and IPv6. Static gateway resolution is mounted read-only. The agent starts only after firewall initialization succeeds; exiting/interruption removes its agent and boundary containers. This mode explicitly uses `runc` for both containers; alternative runtimes may use different packet paths and are rejected until separately qualified. Docker/kernel administrators and malicious trusted images are outside this threat model. The legacy runner's optional gVisor mode is not qualification of this firewall deployment.

Open the setup's loopback review URL and use the **reviewer** token from the private operator directory. Agent tokens cannot approve actions. Local certificates expire after 30 days and browsers may show a trust warning; use your own certificate lifecycle for long-lived installations.

`circuit down --dir /private/operator/circuit` stops the gateway without deleting its state volume. Stop agent runs first. Host-side `circuit start`, proxy `run`, `mcp wrap`, and the old runner without `--dir` remain cooperative/inspection paths, not this enforced deployment.

The generated deployment uses its own named state volume. If a setup already has host-side action history, `up` refuses to silently start an empty store. Migrate a verified backup using the [paused restore and reconciliation procedure](production-deployment.md) before changing deployment ownership. Moving the operator directory also changes the generated project/volume name; never treat that as a state migration.

## Model Access

The runner also rejects workspaces containing known credential/configuration files or hard links to them, sockets, pipes, and devices. Read-only filesystem mounts can still expose live Unix sockets, so they are checked before launch. This is a preflight check, not a universal secret detector: review workspace contents and trusted images, and do not let other host processes replace them with sensitive files/services during a run.

The sandbox has no direct internet/model-provider access. Register model access as another fixed REST target (or a trusted plugin/MCP service) in the same manifest. Guided middleware setup accepts up to 32 reviewed targets. For example, declare `model_complete` bound to a fixed `POST /v1/chat/completions` with a schema limiting model names and rejecting streaming/unsupported fields. Call it through the same SDK and read the returned `action.outcome.body`.

REST POST routes require approval in the current contract, including inference. For autonomous inference, use a reviewed MCP tool (or a provider plugin configured through plugin setup) explicitly classified as non-mutating by the operator. Do not weaken the REST write classification merely to make a model SDK work. A model request may not mutate your tool resources but still incurs provider charges: action-count limits are not token/cost accounting. Circuit is not a transparent OpenAI/LiteLLM-compatible model router. Existing agent frameworks need their model/tool callbacks configured to use this contract; generated code cannot automatically rewrite arbitrary application calls. No wildcard internet proxy is added to make integrations work.

An app with no configurable callbacks can use MCP if supported, or needs a small adapter. Installing a client library on an unrestricted host does not prevent bypass. For Kubernetes, VMs, or another sandbox, enforce the equivalent deny-all egress and secret separation outside Circuit; those environments are not qualified by the Docker tests.

## Generated Clients

The versioned contract is [api/openapi.yaml](../api/openapi.yaml), also served at `GET /openapi.yaml`. OpenAPI matches Circuit's existing REST transport; a second protobuf/gRPC enforcement service is unnecessary. [OpenAPI Generator](https://openapi-generator.tech/) generates wire bindings using a pinned image. Handwritten facades add consistent stop/wait behavior without another policy engine.

Install from this checkout or [packed repository artifacts](local-release-testing.md); packages are **not published** to npm/PyPI:

```sh
python -m pip install ./sdk/python
npm ci --prefix sdk/typescript
npm install ./sdk/typescript
# In a Go app, use github.com/hgayan7/circuit/sdk/go with a local replace,
# or fetch the module from main once its commit is pushed.
```

Python:

```python
from circuit_client.safe import Circuit, ActionStopped

client = Circuit.from_environment()
try:
    action = client.execute("reserve", "inventory", {"sku": "item-42"}, "order-42-reserve")
    print(action.outcome.body)
except ActionStopped as stopped:
    print(stopped.action.state, stopped.action.reason)
finally:
    client.close()
```

Node (22.19+):

```javascript
const { fromEnvironment } = require("@circuit/agent-client/node.cjs");
const client = fromEnvironment();
try {
  const action = await client.execute(
    { operation: "reserve", customTool: "inventory", args: { sku: "item-42" } },
    "order-42-reserve"
  );
  console.log(action.outcome.body);
} finally {
  await client.close();
}
```

Go:

```go
client, err := circuitclient.FromEnvironment()
if err != nil { return err }
request := circuitclient.NewActionRequest("reserve", map[string]interface{}{"sku": "item-42"})
request.SetCustomTool("inventory")
action, err := client.Execute(ctx, *request, "order-42-reserve")
if err != nil { return err }
// Only succeeded actions return normally from Execute.
use(action.Outcome.Body)
```

`execute` submits once, polls that action while pending, and returns normally only for `succeeded`. It stops on denied/rejected/failed/expired/uncertain, transport errors, or approval-wait timeout. A wait timeout does not cancel an action already submitted; inspect its ID/status before proceeding. `submit` and `get` are available when your application manages approval waiting itself. Reuse a stable key for the same intent and exact payload. Never generate a fresh key to work around uncertain outcomes. The Python facade is convenient for custom targets; generated `ActionsApi` supports the full request contract. Go/TypeScript facades accept full requests.

Regenerate the maintained clients:

```sh
sh scripts/generate-sdks.sh
```

Generate bindings for another language without manually writing endpoint code:

```sh
sh scripts/generate-client.sh kotlin /new/output/kotlin-client
sh scripts/generate-client.sh csharp /new/output/dotnet-client
```

Other generators are possible, but generation is not runtime qualification. Configure verified TLS, agent bearer authentication, bounded timeouts, no automatic dispatch retries, and explicit handling of `action.state`. Generated libraries do not create network isolation or provider-specific schemas/permissions.

## Evidence And Limits

`examples/isolated-validation.py` exercises the actual CLI, Docker namespace firewall, scoped TLS action submission, no upstream call before approval, approved single dispatch and cleanup. `examples/sdk-validation.py` exercises TypeScript/Python/Go against the actual TLS gateway: reads, exact approvals, denial, ambiguous provider replies and no replay. CI regenerates bindings and rejects drift.

The gateway remains a single-process durable bbolt deployment. The new generated topology has no monitoring/backup sidecars by default; configure and rehearse [operations](operations.md) before operating it unattended. Host workspace and database native executors remain excluded from `--production`; expose those capabilities through separately isolated, least-privilege governed services. No test establishes universal provider support, a completed 72-hour soak, an independent security review, or resistance to kernel/container escape.
