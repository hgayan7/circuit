"""Deterministic chatbot tools. Receives only the agent's Circuit connection."""
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
            response = urllib.request.urlopen(req, context=self.tls, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.code, json.load(response)

    def action(self, operation, args, key, tool="booking"):
        return self.request("/v1/actions", "POST", {
            "operation": operation, "custom_tool": tool, "args": args,
        }, key)


class MovieChatbot:
    def __init__(self, client):
        self.client = client

    def prepare(self, show_id, seats, task):
        print(f"\nCustomer: Two tickets for {show_id}, seats {', '.join(seats)}.", flush=True)
        _, shows = self.client.action("search_showtimes", {}, task + "-search")
        if shows["state"] != "succeeded":
            raise RuntimeError("Showtime search did not succeed")
        selected = next(show for show in shows["outcome"]["body"]["shows"] if show["show_id"] == show_id)
        if not set(seats).issubset(selected["available_seats"]):
            raise RuntimeError("Selected seats unavailable")
        _, action = self.client.action("hold_seats", {"show_id": show_id, "seats": seats}, task + "-hold")
        if action["state"] != "succeeded":
            raise RuntimeError("Seat hold did not succeed: " + action["state"])
        quote = action["outcome"]["body"]
        print(f"Chatbot: Seats held. Confirm {quote['movie']} at {quote['time']}, {', '.join(quote['seats'])}, ₹{quote['amount_minor'] / 100:.2f}?", flush=True)
        return quote

    @staticmethod
    def purchase_args(quote, confirmation_ref):
        return {**{field: quote[field] for field in ("hold_id", "show_id", "seats", "amount_minor", "currency")},
                "confirmation_ref": confirmation_ref}

    def purchase(self, quote, confirmation_ref, task):
        _, action = self.client.action("purchase_tickets", self.purchase_args(quote, confirmation_ref), task + "-purchase")
        print("Chatbot: Purchase → " + action["state"], flush=True)
        return action
