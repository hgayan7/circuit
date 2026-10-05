"""Local inventory service: business rules and upstream idempotency live here."""
import hmac
import json
import socket
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse


def start_inventory(token):
    state = {
        "stock": {"headphones-black": 10},
        "orders": {
            "order-1001": {"sku": "headphones-black", "eligible": True},
            "order-1002": {"sku": "headphones-black", "eligible": True},
            "order-1003": {"sku": "headphones-black", "eligible": True},
            "order-1004": {"sku": "headphones-black", "eligible": False},
        },
        "reservations": {}, "keys": {}, "writes": 0,
    }
    lock = threading.Lock()

    class Inventory(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def reply(self, status, body):
            encoded = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

        def authorized(self):
            if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + token):
                self.reply(403, {"error": "inventory credential required"})
                return False
            return True

        def do_GET(self):
            if not self.authorized():
                return
            url = urlparse(self.path)
            query = parse_qs(url.query)
            with lock:
                if url.path == "/orders":
                    order_id = query.get("order_id", [""])[0]
                    order = state["orders"].get(order_id)
                    self.reply(200 if order else 404, {"order_id": order_id, **(order or {})})
                elif url.path == "/items":
                    sku = query.get("sku", [""])[0]
                    self.reply(200, {"sku": sku, "available": state["stock"].get(sku, 0)})
                else:
                    self.reply(404, {"error": "unknown route"})

        def do_POST(self):
            if not self.authorized():
                return
            if self.path != "/reservations":
                self.reply(404, {"error": "unknown route"})
                return
            try:
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            except (ValueError, TypeError):
                self.reply(400, {"error": "invalid JSON"})
                return
            key = self.headers.get("Idempotency-Key", "")
            with lock:
                if not key:
                    self.reply(400, {"error": "idempotency key required"})
                    return
                if key in state["keys"]:
                    self.reply(200, state["keys"][key])
                    return
                order_id, sku = body.get("order_id"), body.get("sku")
                order = state["orders"].get(order_id)
                if not order or not order["eligible"] or order["sku"] != sku:
                    self.reply(422, {"error": "order is not eligible for this replacement"})
                    return
                if order_id in state["reservations"]:
                    self.reply(409, {"error": "replacement already reserved"})
                    return
                if state["stock"].get(sku, 0) < 1:
                    self.reply(409, {"error": "out of stock"})
                    return
                result = {"reservation_id": "replacement-" + order_id, "order_id": order_id,
                          "sku": sku, "quantity": 1}
                state["stock"][sku] -= 1
                state["reservations"][order_id] = result
                state["keys"][key] = result
                state["writes"] += 1
                if order_id == "order-1002":
                    # Commit the reservation, then lose the reply: retrying could double-write.
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                    return
                self.reply(201, result)

    server = ThreadingHTTPServer(("127.0.0.1", 0), Inventory)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, state
