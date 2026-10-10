# Movie-ticket chatbot through Circuit

Run a complete local conversation using the real Circuit gateway over verified TLS:

```sh
python3 examples/movie-booking/demo.py
```

Requires Go 1.26.9+ and Python 3. No Python dependencies, model keys, external accounts, or Docker are needed. Everything runs in temporary directories and stops on exit. This is a deterministic chatbot walkthrough with local booking/payment fixtures; no real tickets are bought or money moved.

For terminal customer confirmation and operator approval prompts:

```sh
python3 examples/movie-booking/demo.py --interactive
```

Answer `yes` to continue each purchase. Declining stops the walkthrough without that purchase; its five-minute hold expires. The default mode supplies explicitly labeled scripted consent and a scripted separate reviewer decision so CI can reproduce the entire workflow.

## The conversation

1. The customer requests two seats. Showtime search and a five-minute seat hold execute automatically through Circuit.
2. The trusted customer application fetches the authoritative show, seats, price, and expiry, then asks the customer to confirm.
3. A confirmed INR 900 purchase executes automatically under the operator's rules. A retry returns the same action.
4. A confirmed INR 3000 purchase waits for an operator. The agent credential cannot approve it; the separate reviewer approves the exact stored purchase.
5. A confirmed INR 1200 purchase commits upstream, but the fixture drops the response. Circuit records `uncertain`. After a gateway restart, the original key and a new key both return that unresolved action without another purchase call.
6. A read-only booking lookup verifies the booking. The admin records its ID as reconciliation evidence without buying again.

The runner also verifies that forged confirmation, a changed price, an agent-supplied `confirmed: true`, and reused consent cannot create another booking. An excessive amount is denied before dispatch. A provider-side write rejection is conservatively `uncertain` in the current REST contract; the fixture asserts no booking was created. This example does not change that contract or add automatic reconciliation.

## Who owns which decision

```text
Customer application -- separate session --> authoritative quote / customer confirmation
                                                     |
                                          purchase-bound reference
                                                     v
Chatbot -- agent credential --> Circuit -- service credential --> Booking API
                                  ^                              validates consent,
                           operator review                       price, seats, expiry,
                           above INR 2000                        and purchase uniqueness
```

Customer consent and operator approval are separate. Circuit decides whether the agent may request a purchase. The booking service validates an unguessable confirmation reference bound to the customer and exact held quote, and consumes it atomically with the fixture booking. A confirmation cannot be moved to other seats, another price, another customer, or another hold. Concurrent attempts with different keys create at most one booking. Provider-side idempotency binds each key to the exact request.

The agent gets its Circuit connection and the customer-confirmed reference. It gets no customer-session token, upstream service token, reviewer token, or admin token. Customer confirmation endpoints are absent from the reviewed Circuit manifest. The gateway's service credential cannot call them.

This uses a fixed fixture customer session, not a production login system. The service's in-memory holds, consent records, bookings, and idempotency state survive a Circuit restart because the fixture remains running; they do not survive a booking-service restart. A real service needs durable atomic state, authenticated customer sessions, trusted quote display, payment authorization, and its own provider-status recovery.

## Files and policy

| File | Responsibility |
| --- | --- |
| `chatbot.py` | Agent-only HTTPS client and deterministic search/hold/purchase tools. |
| `booking_api.py` | Credential-separated service/customer endpoints, expiring holds, consent binding, idempotency, and duplicate protection. |
| `policy.json` | Automatic search/holds and purchases up to INR 2000; review above INR 2000; deny above INR 5000; durable action quotas. |
| `demo.py` | Temporary operator setup, fixed schemas/routes, customer interaction, separate reviewer/admin decisions, restart and assertions. |
| `test_booking.py` | Adversarial consent, expiry, customer ownership, and concurrent duplicate tests. |

`policy.json` supplies `rules` and `limits` for an operator-owned gateway config. The runner replaces setup's blanket review rule with this policy. The schema requires integer minor-unit amounts, fixed INR currency, declared seats, and a confirmation reference. Write operations stay classified as mutating even when rules authorize them automatically. The quota is ten purchase attempts per agent/target/hour, including dispatched failed and uncertain attempts; it is not token or monetary accounting.

Run the business-rule checks separately:

```sh
python3 -m unittest discover -s examples/movie-booking -p 'test_*.py' -v
```

An LLM can select these tools through the same agent-only connection. This example does not use an LLM, expose a web chatbot, stream inference, or implement a real booking provider. It runs without OS/container isolation: credential separation in the code is not protection against malicious code sharing the host. For enforced routing, deploy the agent in the [isolated environment](../../docs/isolated-agents.md), keep operator/customer/service secrets outside it, and validate your actual provider.

The autonomy semantics require v0.3.0 or newer. Published v0.2.0 binaries retain mandatory write approvals. See [gateway policy](../../docs/gateway-policy.md) and [REST forwarding](../../docs/middleware.md).
