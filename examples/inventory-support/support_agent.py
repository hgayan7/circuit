"""Deterministic support workflow. Receives only Circuit's agent connection."""
import json
import ssl
import urllib.error
import urllib.request
from pathlib import Path


class CircuitClient:
    def __init__(self, connection):
        self.url = connection["url"]
        self.token = Path(connection["token_file"]).read_text().strip()
        self.tls = ssl.create_default_context(cafile=connection["ca_cert"])

    def request(self, path, method="GET", body=None, key=None):
        headers = {"Authorization": "Bearer " + self.token, "Content-Type": "application/json"}
        if key:
            headers["Idempotency-Key"] = key
        req = urllib.request.Request(self.url + path, method=method, headers=headers,
                                     data=None if body is None else json.dumps(body).encode())
        try:
            response = urllib.request.urlopen(req, context=self.tls, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.code, json.load(response)

    def action(self, operation, args, key, tool="inventory"):
        return self.request("/v1/actions", "POST", {
            "operation": operation, "custom_tool": tool, "args": args,
        }, key)


class SupportAgent:
    def __init__(self, client):
        self.client = client

    def replacement(self, order_id):
        print("\nCustomer: My headphones arrived damaged. Replace " + order_id + ".", flush=True)
        _, action = self.client.action("inspect_order", {"order_id": order_id}, order_id + "-order")
        if action["state"] != "succeeded":
            raise RuntimeError("Order lookup was blocked: " + action["reason"])
        order = action["outcome"]["body"]
        if not order.get("eligible"):
            print("Agent: This order is not eligible; no reservation requested.", flush=True)
            return None
        _, action = self.client.action("inspect_item", {"sku": order["sku"]}, order_id + "-stock")
        if action["state"] != "succeeded" or action["outcome"]["body"]["available"] < 1:
            raise RuntimeError("No stock available")
        _, action = self.client.action("reserve_item", {"order_id": order_id, "sku": order["sku"]},
                                       order_id + "-replacement")
        print("Agent: Replacement request → " + action["state"] + ". " + action["reason"], flush=True)
        return action
