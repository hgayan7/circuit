# Support agent: reserve a replacement through Circuit

A customer reports damaged headphones. The support workflow looks up the order,
checks stock, and requests one replacement. Circuit controls the inventory API
access, while the inventory service enforces replacement eligibility and prevents
multiple replacements for the same order.

## Run it

From the repository root, with Go 1.26.9+ and Python 3 installed:

```sh
python3 examples/inventory-support/demo.py
```

No Python packages, model API keys, Docker, or external accounts are required.
The runner builds Circuit, creates temporary role credentials and a local TLS
certificate, starts the inventory service and gateway, runs the workflow, and
removes its state after stopping both servers. It does not ship replacements or
contact a production inventory system.

```text
Support workflow -- agent-only credential --> Circuit -- inventory credential --> Inventory API
                                               ^
                                  Separate operator decision
```

## What you will see

1. `order-1001`: order and stock reads execute automatically. The reservation waits
   for a separate reviewer decision, then stock drops from 10 to 9.
2. A retry returns the original action without reserving more stock. An undeclared
   `delete_inventory` action is denied, and an attempt to override the HTTP method
   fails schema validation.
3. `order-1004`: the workflow finds the order ineligible and requests no replacement.
4. `order-1002`: inventory reserves stock but deliberately drops the response.
   Circuit records `uncertain`. After a gateway restart, even a new request key
   cannot replay that identical unresolved action.
5. `order-1003`: Circuit denies the request because the two-reservations-per-agent
   rolling hourly quota is exhausted. The quota survives the restart.
6. The operator verifies the second reservation in the inventory fixture and
   reconciles Circuit's record to `succeeded`, without another upstream call.

The final result is two reservations, eight units of stock, and a blocked third
reservation. The runner asserts these outcomes and credential-export/log checks.

## Implementation

| File | Responsibility |
| --- | --- |
| [support_agent.py](support_agent.py) | Support workflow and HTTPS Circuit client; receives only the agent connection. |
| [inventory_api.py](inventory_api.py) | Local authenticated REST API, order eligibility, stock, reservation uniqueness, and idempotency. |
| [demo.py](demo.py) | Operator-owned setup, fixed route manifest, two-write quota, reviewer/admin actions, restart, and assertions. |

The imported manifest declares three operations: `inspect_order` (`GET /orders`),
`inspect_item` (`GET /items`), and `reserve_item` (`POST /reservations`). Schemas
reject undeclared fields. The `review-writes` setup preset grants only these tools;
the runner tightens its default write quota from five to two per hour.

This is a deterministic agent workflow, not an LLM conversation. An LLM could choose
the order/tool arguments, but would use the same agent-only Circuit connection.
The operator harness automatically approves the two scripted demo requests; the
agent never receives reviewer/admin credentials. In a real deployment, a separate
operator would review them, or you would configure a policy for permitted automatic
writes.

## Moving to your own inventory system

Replace the local routes with your reviewed API routes and schemas, configure its
credential on the gateway, and retain provider-side checks for order ownership,
eligibility, stock, and duplicate replacements. Authenticate the customer before
selecting their order; this fixture uses fixed orders and has no customer login.
Validate your provider's failure and idempotency behavior before relying on it.
See the [REST forwarding guide](../../docs/middleware.md#register-rest-routes).

This demo runs locally without OS/container isolation. Separate credentials in a
workflow are not a security boundary against malicious code on the same host.
Deploy the agent in the [isolated environment](../../docs/isolated-agents.md) with
gateway-only networking and no operator files mounted. Circuit governs requests
that reach it; it does not verify customer facts or perform the shipment itself.
