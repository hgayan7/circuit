# Communication & Work Tools Gateway Action Adapter

Circuit provides a secure, durable, and policy-governed gateway adapter for communication platforms and work management tools (Slack, Teams, Discord, Email, Jira, Notion, Linear, GitHub Issues). Like GitHub, Shell, Database, and Cloud adapters, the Communication adapter enables AI agents to coordinate with humans and systems while enforcing rigorous guardrails:

- **Channel & Provider Scoping**: Every action targets an isolated communication integration (e.g. `team-slack`, `corporate-email`, `jira-support`).
- **Destination Allowlisting**: Agents can only send messages, post tickets, or email addresses explicitly allowed in `allowed_channels`, `allowed_projects`, or `internal_domains`.
- **Broadcast Mention Interception**: Messages containing broadcast pings (e.g., `@channel`, `@here`, `@everyone`) automatically trigger **mandatory human operator approval** to prevent notification flooding.
- **External Email Protection**: Outbound emails with recipients outside configured `internal_domains` require operator approval when `require_approval_for_external: true`.
- **Document Publication Gates**: High-visibility knowledge base or documentation publications (`publish_document`) require human review when `require_approval_for_publish: true`.
- **Rate & Velocity Limiting**: Scoped rate limits per `channel` or `agent_channel` prevent accidental chat spam, duplicate notification loops, or API quota exhaustion.
- **Zero-Dependency Simulation Mode**: Built-in mock communication simulator for local testing and CI without requiring active webhook URLs or SMTP servers.

---

## Configuration

Communication integrations are declared in your Circuit gateway YAML configuration under `communications`:

```yaml
name: support-ops-gateway
admin_token_env: CIRCUIT_ADMIN_TOKEN
approval_ttl: 24h

communications:
  - id: team-slack
    type: slack
    name: Core Engineering Slack
    allowed_channels: ["#eng-alerts", "#releases", "#incident-room"]
    require_approval_for_broadcast: true
    max_timeout: 10s

  - id: corp-email
    type: email
    name: Outbound Email Dispatcher
    internal_domains: ["mycompany.com", "partner.org"]
    require_approval_for_external: true
    max_timeout: 15s

  - id: company-wiki
    type: notion
    name: Internal Knowledge Base
    require_approval_for_publish: true

agents:
  - id: incident-bot
    token_env: INCIDENT_BOT_TOKEN
    channels: ["team-slack", "corp-email", "company-wiki"]
    actions:
      - send_message
      - send_email
      - create_ticket
      - update_ticket
      - publish_document

limits:
  - id: slack-message-rate
    actions: ["send_message"]
    scope: channel
    window: 1m
    max_calls: 10

  - id: outbound-email-rate
    actions: ["send_email"]
    scope: agent_channel
    window: 1h
    max_calls: 5
```

---

## Available Actions & MCP Tools

Circuit exposes communication actions over REST (`POST /v1/actions`) and the Model Context Protocol (MCP `/mcp`):

| Action | MCP Tool Name | Description | Required Arguments | Optional Arguments |
|---|---|---|---|---|
| `send_message` | `comm_send_message` | Send a chat message to a designated channel. Intercepted on `@channel` / `@here` broadcast. | `channel`, `message` | `comm_channel` |
| `send_email` | `comm_send_email` | Dispatch an email to recipients. Intercepted on external recipient domains. | `to`, `subject`, `body` | `comm_channel` |
| `create_ticket` | `comm_create_ticket` | Create a new work ticket or issue in an issue tracker. | `title` | `project`, `description`, `comm_channel` |
| `update_ticket` | `comm_update_ticket` | Update status or post a comment on an existing ticket. | `key` | `status`, `comment`, `comm_channel` |
| `publish_document`| `comm_publish_document` | Publish a knowledge base page or wiki document. | `title`, `content` | `comm_channel` |

---

## Safety Guarantees

### 1. Broadcast Mention Interception
Autonomous bots should never be able to wake an entire company without human authorization. If `send_message` contains `@channel`, `@here`, or `@everyone`, Circuit catches it during request analysis and places the action in `pending` status until an authorized operator approves it.

### 2. External Domain Boundary
When interacting with customer or vendor email endpoints, any email recipient domain outside `internal_domains` requires operator confirmation to prevent data leaks, spam, or premature communications.

### 3. Velocity and Spam Protection
Using `channel` and `agent_channel` limit scopes, teams can set hard ceilings on how frequently an agent can post to communication channels, protecting team attention and avoiding rate-limit bans from upstream providers.

---

## Verification & Serving

Validate your gateway configuration:
```bash
circuit gateway check -config gateway.yaml
```

Run Circuit gateway:
```bash
export CIRCUIT_ADMIN_TOKEN="a-secure-secret-token-with-at-least-32-chars"
export INCIDENT_BOT_TOKEN="agent-token-12345"
circuit gateway serve -config gateway.yaml -addr 127.0.0.1:8080
```
