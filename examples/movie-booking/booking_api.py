"""Local booking fixture. Customer consent and purchase uniqueness live here."""
import copy
import hashlib
import hmac
import json
import secrets
import socket
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse


class BookingError(Exception):
    def __init__(self, status, message):
        super().__init__(message)
        self.status = status


class BookingState:
    def __init__(self, clock=time.time):
        self.clock = clock
        self.lock = threading.RLock()
        self.shows = {
            "matinee": {"movie": "The Last Orbit", "time": "18:00", "price_minor": 45000},
            "premiere": {"movie": "The Last Orbit — premiere", "time": "20:00", "price_minor": 150000},
            "late": {"movie": "Midnight Express", "time": "22:00", "price_minor": 60000},
        }
        self.holds, self.confirmations, self.bookings, self.keys = {}, {}, {}, {}
        self.purchase_calls = 0

    def quote(self, customer, hold_id):
        with self.lock:
            hold = self.holds.get(hold_id)
            if not hold or hold["customer_id"] != customer:
                raise BookingError(404, "hold not found for this customer")
            if hold["expires_at"] <= self.clock():
                raise BookingError(409, "seat hold expired")
            return copy.deepcopy(hold)

    def available(self, show_id):
        with self.lock:
            occupied = set()
            for hold in self.holds.values():
                if hold["show_id"] == show_id and (hold["expires_at"] > self.clock() or hold["hold_id"] in self.bookings):
                    occupied.update(hold["seats"])
            return [seat for seat in ("A1", "A2", "B1", "B2", "C1", "C2") if seat not in occupied]

    def confirm(self, customer, hold_id):
        with self.lock:
            quote = self.quote(customer, hold_id)
            if hold_id in self.bookings:
                raise BookingError(409, "hold already purchased")
            # Opaque, application-issued capability; the agent cannot mint one.
            reference = secrets.token_urlsafe(32)
            self.confirmations[reference] = {"quote": quote, "used": False}
            return {"confirmation_ref": reference, "quote": quote}

    def write(self, customer, path, args, key):
        with self.lock:
            if not key:
                raise BookingError(400, "idempotency key required")
            digest = hashlib.sha256(json.dumps([customer, path, args], sort_keys=True).encode()).hexdigest()
            if key in self.keys:
                old_digest, result = self.keys[key]
                if digest != old_digest:
                    raise BookingError(409, "idempotency key belongs to a different request")
                return copy.deepcopy(result), False
            if path == "/holds":
                show = self.shows.get(args.get("show_id"))
                seats = args.get("seats")
                if not show or not isinstance(seats, list) or not 1 <= len(seats) <= 2 or any(not isinstance(s, str) for s in seats) or len(set(seats)) != len(seats):
                    raise BookingError(422, "invalid show or seats")
                if not set(seats).issubset(self.available(args["show_id"])):
                    raise BookingError(409, "seats unavailable")
                hold_id = "hold-" + secrets.token_hex(8)
                result = {"hold_id": hold_id, "customer_id": customer, "show_id": args["show_id"],
                          "movie": show["movie"], "time": show["time"], "seats": sorted(seats),
                          "amount_minor": len(seats) * show["price_minor"], "currency": "INR",
                          "expires_at": self.clock() + 300}
                self.holds[hold_id] = result
                drop_reply = False
            elif path == "/purchases":
                self.purchase_calls += 1
                quote = self.quote(customer, args.get("hold_id"))
                confirmation = self.confirmations.get(args.get("confirmation_ref"))
                if not confirmation or confirmation["used"] or confirmation["quote"] != quote:
                    raise BookingError(422, "valid unused customer confirmation required")
                for field in ("hold_id", "show_id", "seats", "amount_minor", "currency"):
                    if args.get(field) != quote[field]:
                        raise BookingError(422, "purchase differs from customer-confirmed quote")
                if quote["hold_id"] in self.bookings:
                    raise BookingError(409, "hold already purchased")
                result = {**quote, "booking_id": "booking-" + secrets.token_hex(8), "status": "booked",
                          "payment": "fixture-only; no money moved"}
                self.bookings[quote["hold_id"]] = result
                confirmation["used"] = True
                # Failure injection is service-owned, not an agent input.
                drop_reply = quote["show_id"] == "late"
            else:
                raise BookingError(404, "unknown route")
            self.keys[key] = (digest, copy.deepcopy(result))
            return copy.deepcopy(result), drop_reply


def start_booking(upstream_token, customer_token):
    state = BookingState()

    class BookingAPI(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def reply(self, status, body):
            encoded = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

        def authorized(self, token):
            if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + token):
                raise BookingError(403, "credential cannot access this route")
            return "customer-1"  # The trusted application's fixed demo session.

        def do_GET(self):
            try:
                url = urlparse(self.path)
                query = parse_qs(url.query)
                if url.path == "/customer/quote":
                    customer = self.authorized(customer_token)
                    result = state.quote(customer, query.get("hold_id", [""])[0])
                else:
                    customer = self.authorized(upstream_token)
                    if url.path == "/showtimes":
                        result = {"shows": [{"show_id": sid, **show, "available_seats": state.available(sid)} for sid, show in state.shows.items()]}
                    elif url.path == "/bookings":
                        with state.lock:
                            result = copy.deepcopy(state.bookings.get(query.get("hold_id", [""])[0]))
                            if not result or result["customer_id"] != customer:
                                raise BookingError(404, "booking not found")
                    else:
                        raise BookingError(404, "unknown route")
                self.reply(200, result)
            except BookingError as error:
                self.reply(error.status, {"error": str(error)})

        def do_POST(self):
            try:
                customer = self.authorized(customer_token if self.path == "/customer/confirm" else upstream_token)
                length = int(self.headers.get("Content-Length", "0"))
                if not 0 < length <= 16384:
                    raise BookingError(400, "bounded JSON body required")
                args = json.loads(self.rfile.read(length))
                if not isinstance(args, dict):
                    raise BookingError(400, "JSON object required")
                if self.path == "/customer/confirm":
                    if set(args) != {"hold_id", "decision"} or args["decision"] != "confirm":
                        raise BookingError(422, "explicit customer confirmation required")
                    result, drop_reply = state.confirm(customer, args["hold_id"]), False
                else:
                    result, drop_reply = state.write(customer, self.path, args, self.headers.get("Idempotency-Key", ""))
                if drop_reply:
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                    return
                self.reply(200, result)
            except (ValueError, TypeError):
                self.reply(400, {"error": "invalid JSON"})
            except BookingError as error:
                self.reply(error.status, {"error": str(error)})

    server = ThreadingHTTPServer(("127.0.0.1", 0), BookingAPI)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, state
