"""Consent, expiry, and duplicate-purchase checks for the trusted fixture."""
import concurrent.futures
import unittest

from booking_api import BookingError, BookingState
from chatbot import MovieChatbot


class BookingTests(unittest.TestCase):
    def setUp(self):
        self.now = 1000.0
        self.state = BookingState(clock=lambda: self.now)
        self.quote, _ = self.state.write("customer-1", "/holds", {"show_id": "matinee", "seats": ["A1", "A2"]}, "hold")
        self.reference = self.state.confirm("customer-1", self.quote["hold_id"])["confirmation_ref"]
        self.args = MovieChatbot.purchase_args(self.quote, self.reference)

    def assert_rejected(self, args, customer="customer-1", key="purchase"):
        with self.assertRaises(BookingError):
            self.state.write(customer, "/purchases", args, key)
        self.assertEqual(len(self.state.bookings), 0)
        self.assertFalse(self.state.confirmations[self.reference]["used"])

    def test_forged_and_changed_confirmation(self):
        for field, value in (("confirmation_ref", "forged"), ("amount_minor", 89900),
                             ("seats", ["B1", "B2"]), ("show_id", "late"), ("currency", "USD")):
            with self.subTest(field=field):
                self.assert_rejected({**self.args, field: value}, key=field)
        self.assert_rejected(self.args, customer="customer-2")

    def test_confirmation_cannot_move_to_another_hold(self):
        other, _ = self.state.write("customer-1", "/holds", {"show_id": "matinee", "seats": ["B1", "B2"]}, "other-hold")
        self.assert_rejected(MovieChatbot.purchase_args(other, self.reference))

    def test_expiry_releases_seats_and_invalidates_consent(self):
        self.now += 301
        self.assert_rejected(self.args)
        self.assertIn("A1", self.state.available("matinee"))
        with self.assertRaises(BookingError):
            self.state.confirm("customer-1", self.quote["hold_id"])

    def test_concurrent_duplicate_and_same_key_idempotency(self):
        def purchase(key):
            try:
                return self.state.write("customer-1", "/purchases", self.args, key)[0]
            except BookingError:
                return None
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            results = list(pool.map(purchase, [f"key-{i}" for i in range(8)]))
        self.assertEqual(sum(result is not None for result in results), 1)
        self.assertEqual(len(self.state.bookings), 1)
        winning_key = next(key for key in self.state.keys if key.startswith("key-"))
        original = self.state.keys[winning_key][1]
        repeated, drop = self.state.write("customer-1", "/purchases", self.args, winning_key)
        self.assertEqual(repeated, original)
        self.assertFalse(drop)
        with self.assertRaises(BookingError):
            self.state.write("customer-1", "/purchases", {**self.args, "amount_minor": 1}, winning_key)
        self.now += 301
        self.assertNotIn("A1", self.state.available("matinee"), "purchased seats remain occupied after hold expiry")

    def test_customer_cannot_confirm_another_customers_hold(self):
        with self.assertRaises(BookingError):
            self.state.confirm("customer-2", self.quote["hold_id"])


if __name__ == "__main__":
    unittest.main()
