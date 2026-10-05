#!/usr/bin/env python3
"""One-command end-to-end demo with real Circuit and a local inventory fixture."""
import json
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error

from inventory_api import start_inventory
from support_agent import CircuitClient, SupportAgent

ROOT = Path(__file__).resolve().parents[2]


def check(label, condition):
    if not condition:
        raise AssertionError(label)
    print("PASS " + label, flush=True)


def schema(*fields):
    return {"type": "object", "additionalProperties": False, "required": list(fields),
            "properties": {field: {"type": "string", "minLength": 1} for field in fields}}


def main():
    with tempfile.TemporaryDirectory(prefix="circuit-inventory-support-") as tmp:
        work = Path(tmp)
        binary = work / "circuit"
        print("Building Circuit; all demo state and credentials are temporary.", flush=True)
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/circuit"], cwd=ROOT, check=True)
        upstream_token = secrets.token_urlsafe(32)
        upstream_file = work / "inventory-token"
        upstream_file.write_text(upstream_token)
        upstream_file.chmod(0o600)
        inventory, state = start_inventory(upstream_token)
        process = None
        log = None
        try:
            manifest = {"custom_tools": [{"id": "inventory", "protocol": "rest-routes-v1",
                "endpoint": "http://127.0.0.1:" + str(inventory.server_port),
                "token_file": str(upstream_file),
                "operations": ["inspect_order", "inspect_item", "reserve_item"],
                "read_only_operations": ["inspect_order", "inspect_item"],
                "routes": [
                    {"operation": "inspect_order", "method": "GET", "path": "/orders",
                     "query_params": ["order_id"], "input_schema": schema("order_id")},
                    {"operation": "inspect_item", "method": "GET", "path": "/items",
                     "query_params": ["sku"], "input_schema": schema("sku")},
                    {"operation": "reserve_item", "method": "POST", "path": "/reservations",
                     "input_schema": schema("order_id", "sku")},
                ]}]}
            manifest_file = work / "manifest.yaml"
            manifest_file.write_text(json.dumps(manifest))  # JSON is valid YAML; no Python dependencies.
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                port = sock.getsockname()[1]
            operator = work / "operator"
            subprocess.run([str(binary), "setup", "--non-interactive", "--integration", "middleware",
                "--preset", "review-writes", "--upstream-manifest", str(manifest_file),
                "--out", str(operator), "--port", str(port)], check=True,
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            setup = json.loads((operator / "setup.json").read_text())
            config = Path(setup["config"])
            policy = config.read_text()
            # Tighten setup's existing write quota for this demonstration.
            if policy.count("max_calls: 5\n") != 1:
                raise RuntimeError("Setup write quota changed; review the demo configuration")
            config.write_text(policy.replace("max_calls: 5\n", "max_calls: 2\n"))
            agent_connection = setup["connection"]
            client = CircuitClient(agent_connection)
            reviewer = CircuitClient({**agent_connection,
                "token_file": str(operator / "secrets" / "reviewer-token")})
            admin = CircuitClient({**agent_connection,
                "token_file": str(operator / "secrets" / "admin-token")})
            agent = SupportAgent(client)
            log = (work / "gateway.log").open("w")

            def start_gateway():
                running = subprocess.Popen([str(binary), "start", "--dir", str(operator)],
                                           stdout=log, stderr=log)
                try:
                    deadline = time.monotonic() + 30
                    while time.monotonic() < deadline:
                        if running.poll() is not None:
                            raise RuntimeError("Gateway exited; see temporary gateway log")
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

            def approve(action):
                # The operator harness is separate from the agent; automatic approval is demo-only.
                print("Operator: Approving the exact replacement request.", flush=True)
                status, result = reviewer.request("/admin/actions/" + action["id"] + "/decision",
                    "POST", {"digest": action["digest"], "decision": "approve"})
                check("Operator decision recorded", status in (200, 202)
                      and result.get("approved_by", "").startswith("operator:")
                      and result["state"] in ("succeeded", "uncertain"))
                return result

            process = start_gateway()
            print("\nSupport agent → Circuit (verified TLS) → local inventory REST API", flush=True)
            print("Policy: reads automatic; reservations reviewed; 2 reservations/agent/hour.", flush=True)
            first = agent.replacement("order-1001")
            check("Reservation waits; inventory has not changed", first["state"] == "pending" and state["writes"] == 0)
            status, _ = client.request("/admin/actions/" + first["id"] + "/decision", "POST",
                                      {"digest": first["digest"], "decision": "approve"})
            check("Agent cannot approve its own action", status == 403)
            completed = approve(first)
            check("Replacement reserved and stock reduced once", completed["state"] == "succeeded"
                  and state["stock"]["headphones-black"] == 9)
            print("Inventory: " + json.dumps(completed["outcome"]["body"]), flush=True)
            _, retry = client.action("reserve_item", first["request"]["args"], "order-1001-replacement")
            check("Retry returns the same action without another reservation", retry["id"] == first["id"] and state["writes"] == 1)
            _, denied = client.action("delete_inventory", {}, "forbidden-delete")
            check("Undeclared destructive operation denied", denied["state"] == "denied")
            _, bad = client.action("inspect_item", {"sku": "headphones-black", "method": "DELETE"}, "route-override")
            check("Agent cannot override the declared request schema", bad["state"] == "failed" and state["writes"] == 1)
            check("Ineligible order never requests a replacement", agent.replacement("order-1004") is None)

            second = agent.replacement("order-1002")
            uncertain = approve(second)
            check("Lost reply leaves an uncertain action after the upstream write", uncertain["state"] == "uncertain" and state["writes"] == 2)
            print("Operator: Inventory confirms order-1002 is reserved; Circuit requires reconciliation.", flush=True)
            stop_gateway(process)
            process = start_gateway()
            _, retry = client.action("reserve_item", second["request"]["args"], "different-key-after-restart")
            check("Restart and a new key do not replay the uncertain reservation", retry["id"] == second["id"] and state["writes"] == 2)
            third = agent.replacement("order-1003")
            check("Durable quota blocks a third reservation after restart", third["state"] == "denied" and "budget" in third["reason"] and state["writes"] == 2)
            check("Inventory confirms the uncertain replacement exists", "order-1002" in state["reservations"])
            status, reconciled = admin.request("/admin/actions/" + second["id"] + "/reconcile", "POST",
                {"digest": second["digest"], "state": "succeeded",
                 "note": "Inventory verified replacement-order-1002; stock reduced once."})
            check("Operator reconciles the verified outcome without re-execution", status == 200
                  and reconciled["state"] == "succeeded" and state["writes"] == 2)
            export = (operator / "mcp.json").read_text()
            check("Agent export contains no inventory or operator credential", all(secret not in export
                  for secret in (upstream_token, reviewer.token, admin.token)))
            stop_gateway(process)
            log.flush()
            check("Gateway logs contain no demo credentials", all(secret not in (work / "gateway.log").read_text()
                  for secret in (upstream_token, client.token, reviewer.token, admin.token)))
            print("\nComplete: 2 replacements reserved, 8 units remain, third reservation blocked.", flush=True)
        finally:
            stop_gateway(process)
            if log:
                log.close()
            inventory.shutdown()
            inventory.server_close()


def stop_gateway(process):
    if process is not None and process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


if __name__ == "__main__":
    main()
