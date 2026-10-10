# Communication Policy Simulator

This adapter is **simulation only**. It does not call Slack, Teams, email, Jira, Notion, Linear, or other providers. Successful outcomes include `simulated: true`; simulated messages, tickets, and documents are held in memory and reset on restart. The action audit and budgets persist separately.

```yaml
name: communication-fixture
simulation: true
communications:
  - id: team-chat
    kind: chat
    allowed_channels: [engineering]
  - id: email-fixture
    kind: email
    internal_domains: [example.com]
    max_recipients: 5
    require_approval_for_external: true
agents:
  - id: assistant
    token_env: CIRCUIT_AGENT_TOKEN
    channels: [team-chat, email-fixture]
    actions: [send_message, send_email, create_ticket, update_ticket, publish_document]
```

REST actions select an integration using `channel`. The destination for `send_message` is supplied separately in `args.channel`. MCP exposes `comm_send_message`, `comm_send_email`, `comm_create_ticket`, `comm_update_ticket`, and `comm_publish_document`.

Broadcast mentions and document publication default to approval. External email defaults to approval when configured. In v0.3.0, explicit gateway ALLOW rules can override approval defaults; matching REQUIRE_APPROVAL and DENY rules take precedence. See [gateway policy](gateway-policy.md). Limits support `channel` and `agent_channel`. These are policy behavior tests, not evidence of delivery, provider permissions, or credential isolation on a real communication service.

```bash
circuit gateway check gateway.yaml
circuit gateway serve --config gateway.yaml --listen 127.0.0.1:8080
```

See [validation status](validation-status.md).
