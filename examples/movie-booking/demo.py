#!/usr/bin/env python3
"""Actual Circuit, verified TLS, local booking API, and separate customer consent."""
import argparse
import json
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

from booking_api import start_booking
from chatbot import CircuitClient, MovieChatbot

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent


def check(label, condition):
    if not condition:
        raise AssertionError(label)
    print("PASS " + label, flush=True)


def schema(properties):
    return {"type": "object", "additionalProperties": False, "required": list(properties), "properties": properties}


def stop_gateway(process):
    if process is not None and process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--interactive", action="store_true", help="ask for customer consent and the larger-purchase operator decision")
    options = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="circuit-movie-booking-") as tmp:
        work = Path(tmp)
        binary = work / "circuit"
        print("Building Circuit. Local fixtures only; no tickets purchased or money moved.", flush=True)
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/circuit"], cwd=ROOT, check=True)
        upstream_token, customer_token = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        upstream_file = work / "booking-token"
        upstream_file.write_text(upstream_token)
        upstream_file.chmod(0o600)
        booking, state = start_booking(upstream_token, customer_token)
        process, log = None, None
        try:
            string = {"type": "string", "minLength": 1}
            seats = {"type": "array", "items": {"type": "string", "enum": ["A1", "A2", "B1", "B2", "C1", "C2"]}, "minItems": 1, "maxItems": 2, "uniqueItems": True}
            purchase_schema = schema({"hold_id": string, "show_id": string, "seats": seats,
                "amount_minor": {"type": "integer", "minimum": 1}, "currency": {"type": "string", "enum": ["INR"]}, "confirmation_ref": string})
            operations = ["search_showtimes", "hold_seats", "purchase_tickets", "inspect_booking"]
            manifest = {"custom_tools": [{"id": "booking", "protocol": "rest-routes-v1",
                "endpoint": f"http://127.0.0.1:{booking.server_port}", "token_file": str(upstream_file),
                "operations": operations, "read_only_operations": ["search_showtimes", "inspect_booking"],
                "routes": [
                    {"operation": "search_showtimes", "method": "GET", "path": "/showtimes", "input_schema": schema({})},
                    {"operation": "hold_seats", "method": "POST", "path": "/holds", "input_schema": schema({"show_id": string, "seats": seats})},
                    {"operation": "purchase_tickets", "method": "POST", "path": "/purchases", "input_schema": purchase_schema},
                    {"operation": "inspect_booking", "method": "GET", "path": "/bookings", "query_params": ["hold_id"], "input_schema": schema({"hold_id": string})},
                ]}]}
            manifest_file = work / "manifest.yaml"
            manifest_file.write_text(json.dumps(manifest))
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                port = sock.getsockname()[1]
            operator = work / "operator"
            subprocess.run([str(binary), "setup", "--non-interactive", "--integration", "middleware",
                "--preset", "review-writes", "--upstream-manifest", str(manifest_file),
                "--out", str(operator), "--port", str(port)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            setup = json.loads((operator / "setup.json").read_text())
            connection = setup["connection"]
            # Operator-owned fixture config replaces the generated blanket review
            # rule with the exact autonomy policy; no agent can edit these rules.
            policy = json.loads((HERE / "policy.json").read_text())
            config = {"name": "movie-booking-demo", "approval_ttl": "15m", "custom_tools": manifest["custom_tools"],
                "agents": [{"id": "agent", "token_file": connection["token_file"], "custom_tools": ["booking"], "actions": operations}],
                "operators": [{"id": role, "role": role, "token_file": str(operator / "secrets" / (role + "-token"))} for role in ("admin", "reviewer", "observer")], **policy}
            Path(setup["config"]).write_text(json.dumps(config))
            client = CircuitClient(connection)
            reviewer = CircuitClient({**connection, "token_file": str(operator / "secrets/reviewer-token")})
            admin = CircuitClient({**connection, "token_file": str(operator / "secrets/admin-token")})
            chatbot = MovieChatbot(client)
            log = (work / "gateway.log").open("w")

            def start_gateway():
                running = subprocess.Popen([str(binary), "start", "--dir", str(operator)], stdout=log, stderr=log)
                try:
                    deadline = time.monotonic() + 30
                    while time.monotonic() < deadline:
                        if running.poll() is not None:
                            raise RuntimeError("Gateway exited during startup")
                        try:
                            if client.request("/readyz")[0] == 200:
                                return running
                        except (OSError, urllib.error.URLError):
                            pass
                        time.sleep(0.1)
                    raise RuntimeError("Gateway startup timed out")
                except BaseException:
                    stop_gateway(running)
                    raise

            def customer_request(path, body=None, token=customer_token):
                req = urllib.request.Request(f"http://127.0.0.1:{booking.server_port}" + path,
                    headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
                    data=None if body is None else json.dumps(body).encode())
                try:
                    response = urllib.request.urlopen(req, timeout=10)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    return response.code, json.load(response)

            def confirm(quote):
                # Fetch authoritative details with the customer application's
                # credential; never trust a chatbot's displayed amount alone.
                status, trusted = customer_request("/customer/quote?" + urllib.parse.urlencode({"hold_id": quote["hold_id"]}))
                check("Customer application fetches the authoritative quote", status == 200 and trusted == quote)
                print(f"Customer application: {trusted['movie']}, {trusted['time']}, seats {', '.join(trusted['seats'])}, INR {trusted['amount_minor'] / 100:.2f}.", flush=True)
                if options.interactive and input("Customer: Confirm this exact purchase? [yes/no] ").strip().lower() != "yes":
                    print("Customer declined. Confirmed purchase not submitted; the hold expires automatically.", flush=True)
                    return None
                print("Customer: Yes. (Scripted fixture consent.)" if not options.interactive else "Customer consent recorded.", flush=True)
                status, result = customer_request("/customer/confirm", {"hold_id": quote["hold_id"], "decision": "confirm"})
                check("Trusted application issues purchase-bound confirmation", status == 200 and result["quote"] == trusted)
                return result["confirmation_ref"]

            process = start_gateway()
            print("\nChatbot → Circuit over verified TLS → booking fixture.", flush=True)
            print("Policy: search/holds automatic; purchases ≤ INR 2000 automatic after customer consent; larger purchases reviewed.", flush=True)
            quote = chatbot.prepare("matinee", ["A1", "A2"], "matinee")
            check("Seat hold executes automatically without buying tickets", len(state.holds) == 1 and not state.bookings)
            status, _ = customer_request("/customer/confirm", {"hold_id": quote["hold_id"], "decision": "confirm"}, token=upstream_token)
            check("Gateway's booking credential cannot mint customer consent", status == 403)
            _, denied = client.action("confirm_purchase", {"hold_id": quote["hold_id"]}, "agent-mints-consent")
            check("Customer confirmation is not an agent-accessible Circuit operation", denied["state"] == "denied" and not state.confirmations)
            forged = chatbot.purchase(quote, "agent-forged-confirmation", "forged")
            check("Service rejects forged consent without purchasing", forged["state"] == "uncertain" and not state.bookings)
            fake_bool = {**chatbot.purchase_args(quote, "forged"), "confirmed": True}
            _, rejected = client.action("purchase_tickets", fake_bool, "fake-confirmed-field")
            check("An agent confirmation boolean is rejected by the pinned schema", rejected["state"] == "failed" and not state.bookings)
            reference = confirm(quote)
            if reference is None:
                return
            tampered = {**quote, "amount_minor": quote["amount_minor"] - 100}
            bad = chatbot.purchase(tampered, reference, "tampered")
            check("Consent cannot authorize an altered price", bad["state"] == "uncertain" and not state.bookings)
            first = chatbot.purchase(quote, reference, "matinee")
            check("Confirmed INR 900 purchase executes without operator approval", first["state"] == "succeeded" and first["approved_by"] == "policy" and len(state.bookings) == 1)
            args = chatbot.purchase_args(quote, reference)
            _, retry = client.action("purchase_tickets", args, "matinee-purchase")
            check("Same-key retry returns the original purchase", retry["id"] == first["id"] and len(state.bookings) == 1)
            duplicate = chatbot.purchase(quote, reference, "duplicate")
            check("A new key cannot reuse consumed customer consent", duplicate["state"] == "uncertain" and len(state.bookings) == 1)
            excessive = {**args, "amount_minor": 600000}
            before = state.purchase_calls
            _, denied = client.action("purchase_tickets", excessive, "excessive")
            check("DENY wins over the large-purchase review rule", denied["state"] == "denied" and state.purchase_calls == before)

            premium = chatbot.prepare("premiere", ["A1", "A2"], "premiere")
            reference = confirm(premium)
            if reference is None:
                return
            lowered = chatbot.purchase({**premium, "amount_minor": 90000}, reference, "lowered-premiere")
            check("Lowering the amount cannot bypass larger-purchase review", lowered["state"] == "uncertain" and len(state.bookings) == 1)
            pending = chatbot.purchase(premium, reference, "premiere")
            check("Confirmed INR 3000 purchase waits for operator review", pending["state"] == "pending" and len(state.bookings) == 1)
            status, _ = client.request("/admin/actions/" + pending["id"] + "/decision", "POST", {"digest": pending["digest"], "decision": "approve"})
            check("Agent cannot approve its own purchase", status == 403)
            decision = "approve"
            if options.interactive and input("Operator: Approve the INR 3000 purchase? [yes/no] ").strip().lower() != "yes":
                decision = "reject"
            print("Operator harness: " + decision + " exact purchase (scripted in default mode).", flush=True)
            status, completed = reviewer.request("/admin/actions/" + pending["id"] + "/decision", "POST", {"digest": pending["digest"], "decision": decision})
            if decision == "reject":
                check("Operator rejection prevents the larger purchase", completed["state"] == "rejected" and len(state.bookings) == 1)
                return
            check("Separate reviewer completes the exact larger purchase", status == 200 and completed["state"] == "succeeded" and completed["approved_by"].startswith("operator:") and len(state.bookings) == 2)

            late = chatbot.prepare("late", ["B1", "B2"], "late")
            reference = confirm(late)
            if reference is None:
                return
            uncertain = chatbot.purchase(late, reference, "late")
            check("Lost reply leaves the committed purchase uncertain", uncertain["state"] == "uncertain" and len(state.bookings) == 3)
            stop_gateway(process)
            process = start_gateway()
            before = state.purchase_calls
            for key in ("late-purchase", "different-key-after-restart"):
                _, retry = client.action("purchase_tickets", chatbot.purchase_args(late, reference), key)
                check("Uncertain purchase is not redispatched after restart", retry["id"] == uncertain["id"] and retry["state"] == "uncertain" and state.purchase_calls == before)
            _, evidence = client.action("inspect_booking", {"hold_id": late["hold_id"]}, "late-status")
            check("Read-only provider lookup verifies the exact confirmed booking", evidence["state"] == "succeeded" and all(evidence["outcome"]["body"][field] == late[field] for field in ("hold_id", "customer_id", "show_id", "seats", "amount_minor", "currency")))
            booking_id = evidence["outcome"]["body"]["booking_id"]
            status, reconciled = admin.request("/admin/actions/" + uncertain["id"] + "/reconcile", "POST", {"digest": uncertain["digest"], "state": "succeeded", "note": f"Verified {booking_id} through inspect_booking for exact hold {late['hold_id']}."})
            check("Admin records verified recovery without another purchase", status == 200 and reconciled["state"] == "succeeded" and state.purchase_calls == before and len(state.bookings) == 3)
            export = (operator / "mcp.json").read_text()
            private_tokens = (upstream_token, customer_token, reviewer.token, admin.token)
            check("Agent connection export excludes service/customer/operator credentials", all(token not in export for token in private_tokens))
            stop_gateway(process)
            log.flush()
            check("Gateway logs do not expose credentials", all(token not in (work / "gateway.log").read_text() for token in (*private_tokens, client.token)))
            print("\nComplete: 3 fixture bookings; 2 autonomous, 1 reviewed; lost response recovered without buying twice.", flush=True)
        finally:
            stop_gateway(process)
            if log:
                log.close()
            booking.shutdown()
            booking.server_close()


if __name__ == "__main__":
    main()
