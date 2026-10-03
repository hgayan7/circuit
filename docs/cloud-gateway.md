# Cloud & Deployments Gateway Action Adapter

This adapter is **simulation only**. It does not call cloud providers or Kubernetes. Set `simulation: true`; successful outcomes include `simulated: true`. Simulated deployments and revision history are in memory and reset on restart; action audit and budgets persist separately. The following controls describe simulator policy behavior, not production deployment guarantees.

- **Environment Scoping**: Every action targets an isolated cloud environment (e.g. `staging`, `production`, `preview`, `dev`).
- **Service Allowlisting**: Agents can only interact with services explicitly listed in `allowed_services`.
- **Production Guardrails**: Any operation targeting an environment marked `production: true` automatically triggers **mandatory human operator approval**.
- **Destructive Operation Protection**: Destructive operations like `rollback_deployment` or `scale_service` to 0 replicas require operator approval even in non-production environments.
- **Replica Bounding**: `min_replicas` and `max_replicas` bounds prevent misconfigured scaling commands from causing outages or runaway cloud provider costs.
- **Durable Budgets & Velocity Limits**: Scoped rate limits per `environment` or `agent_environment` prevent deployment loops or resource exhaustion.
- **Zero-Dependency Simulation Mode**: Built-in simulated cloud provider for unit testing, CI pipelines, and prototyping without requiring live Kubernetes clusters or cloud credentials.

---

## Configuration

Cloud environments are declared in your Circuit gateway YAML configuration under `environments`:

```yaml
name: production-deployment-gateway
simulation: true
admin_token_env: CIRCUIT_ADMIN_TOKEN
approval_ttl: 1h

environments:
  - id: staging
    name: Staging Cluster (us-east1)
    production: false
    allowed_services: ["web-api", "worker", "frontend"]
    min_replicas: 1
    max_replicas: 10
    max_timeout: 30s

  - id: prod
    name: Production Cluster (us-east1)
    production: true
    allowed_services: ["web-api", "worker", "frontend"]
    min_replicas: 2
    max_replicas: 50
    max_timeout: 45s

agents:
  - id: release-agent
    token_env: RELEASE_AGENT_TOKEN
    environments: ["staging", "prod"]
    actions:
      - deploy_service
      - rollback_deployment
      - restart_service
      - get_deployment_status
      - scale_service

limits:
  - id: staging-deploy-rate
    actions: ["deploy_service"]
    scope: environment
    window: 10m
    max_calls: 5

  - id: prod-deploy-rate
    actions: ["deploy_service"]
    scope: agent_environment
    window: 1h
    max_calls: 3
```

---

## Available Actions & MCP Tools

Circuit exposes these actions over REST (`POST /v1/actions`) and the Model Context Protocol (MCP `/mcp`):

| Action | MCP Tool Name | Description | Required Arguments | Optional Arguments |
|---|---|---|---|---|
| `deploy_service` | `cloud_deploy` | Deploy or update a container image/revision for a service. | `service`, `image` | `environment`, `version` |
| `rollback_deployment` | `cloud_rollback` | Rollback a service to its prior deployment revision. (Mandatory approval). | `service` | `environment` |
| `restart_service` | `cloud_restart` | Trigger a rolling restart of pods/containers for a service. | `service` | `environment` |
| `get_deployment_status` | `cloud_status` | Inspect current deployment revision, image, replicas, and status. | `service` | `environment` |
| `scale_service` | `cloud_scale` | Scale replica count within configured min/max boundaries. (Scaling to 0 requires approval). | `service`, `replicas` | `environment` |

---

## Safety Guarantees

### 1. Production Approval Gate
When `deploy_service`, `restart_service`, or `scale_service` targets an environment with `production: true`, Circuit intercepts the action, marks it `pending`, and awaits approval from an operator with the `CIRCUIT_ADMIN_TOKEN` via Circuit's web UI (`http://127.0.0.1:8080/`) or Admin REST API.

### 2. Replica Limits & Scale-to-Zero Interception
If an agent attempts to scale a service to 0 replicas:
- Circuit flags the action as traffic-halting and requires human review.
- If an agent specifies replicas above `max_replicas` or below `min_replicas`, Circuit rejects the request with HTTP 400 without executing.

### 3. Revision History & Rollback
The simulator tracks previous images in memory. It does not verify images or restore an actual deployment.

---

## Verification & Serving

Validate your gateway configuration:
```bash
circuit gateway check gateway.yaml
```

Serve the deployment gateway:
```bash
CIRCUIT_ADMIN_TOKEN="..." RELEASE_AGENT_TOKEN="..." circuit gateway serve --config gateway.yaml
```
