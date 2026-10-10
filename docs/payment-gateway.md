# Business & Payment APIs Gateway Action Adapter

This adapter is **simulation only**. It does not call Stripe, banks, or other payment providers and never moves real funds. Set `simulation: true`; successful outcomes include `simulated: true`. Its ledger is in memory and resets on restart; action audit and budgets persist separately. The following controls describe simulator policy behavior, not validated financial-service guarantees.

- **Account Scoping**: Every financial action targets an isolated payment/billing account (e.g. `corporate-ops`, `treasury`, `customer-billing`).
- **Destination Allowlisting**: Outbound transfers (`transfer_funds`) are restricted to pre-approved beneficiary accounts configured in `allowed_destinations`.
- **Per-Transaction Spending Limits**: `max_transaction_amount` enforces a hard ceiling on individual transfers or charges. Requests exceeding this are rejected at the policy engine before dispatch.
- **Threshold-Based Human Approval**: Transactions exceeding `auto_approval_threshold` automatically pause and enter `pending` state, requiring cryptographic sign-off from authorized operators.
- **Refund Governance**: `require_approval_for_refunds` defaults charge reversals and refunds to human review to prevent unauthorized charge manipulation.
- **Velocity Budgets**: Scoped limits (`account` and `agent_account`) constrain transaction frequency and prevent runaway loops.
- **Zero-Dependency Simulation Mode**: Built-in mock ledger tracks balances, charges, refunds, and transaction history locally without requiring third-party credentials.

---

On current source after v0.2.0, explicit matching gateway `ALLOW` rules can override payment approval defaults. Matching `REQUIRE_APPROVAL` and `DENY` rules take precedence; maximum transaction amounts and executor restrictions remain enforced. See [gateway policy](gateway-policy.md).

## Configuration

Payment accounts are declared in your Circuit gateway YAML configuration under `payment_accounts`:

```yaml
name: billing-ops-gateway
simulation: true
admin_token_env: CIRCUIT_ADMIN_TOKEN
approval_ttl: 24h

payment_accounts:
  - id: corporate-treasury
    name: Corporate Treasury USD
    currency: USD
    allowed_destinations:
      - "acct_vendor_payroll"
      - "acct_aws_infra"
      - "acct_tax_escrow"
    max_transaction_amount: 50000.00
    auto_approval_threshold: 500.00
    require_approval_for_refunds: true
    initial_balance: 250000.00
    max_timeout: 15s

agents:
  - id: finance-ops-agent
    token_env: FINANCE_OPS_AGENT_TOKEN
    accounts: ["corporate-treasury"]
    actions:
      - transfer_funds
      - create_charge
      - issue_refund
      - get_balance

limits:
  - id: daily-transfer-velocity
    actions: ["transfer_funds"]
    scope: agent_account
    window: 24h
    max_calls: 10
```

---

## Available Actions & MCP Tools

Circuit exposes payment actions over REST (`POST /v1/actions`) and the Model Context Protocol (MCP `/mcp`):

| Action | MCP Tool Name | Description | Required Arguments | Optional Arguments |
|---|---|---|---|---|
| `transfer_funds` | `payment_transfer` | Transfer funds to an external destination. Approvals required over threshold. | `amount`, `destination` | `currency`, `reason`, `account` |
| `create_charge` | `payment_charge` | Create a customer charge or payment intent. | `amount`, `customer_id` | `currency`, `description`, `account` |
| `issue_refund` | `payment_refund` | Refund a customer charge. Mandatory approval when configured. | `charge_id` | `amount`, `reason`, `account` |
| `get_balance` | `payment_balance` | Query current available balance and ledger statistics. | None | `currency`, `account` |

---

## Safety Guarantees

### 1. Dual-Boundary Transaction Verification
When an autonomous agent attempts `transfer_funds`:
1. **Hard Limit**: If `amount > max_transaction_amount`, the policy engine immediately denies the action with an immutable audit record.
2. **Soft Limit**: If `amount > auto_approval_threshold`, Circuit creates a pending action for review via Circuit's web UI or Admin REST API. Automatic operator notifications are not implemented.

### 2. Destination Enclosure
When configured, `allowed_destinations` restricts simulated beneficiary IDs. This does not validate actual bank beneficiaries or eliminate financial risk.

### 3. Balance & Refund Tracking
The gateway ledger tracks refunded amounts per charge, preventing double-refunds or refund amounts greater than the original charge.

---

## Verification & Serving

Validate your gateway configuration:
```bash
circuit gateway check gateway.yaml
```

Run Circuit gateway:
```bash
export CIRCUIT_ADMIN_TOKEN="a-secure-secret-token-with-at-least-32-chars"
export FINANCE_OPS_AGENT_TOKEN="agent-token-12345"
circuit gateway serve --config gateway.yaml --listen 127.0.0.1:8080
```
