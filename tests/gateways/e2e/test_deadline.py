"""The request deadline (REQUEST_DEADLINE_SECONDS) reaches PostgreSQL: a
handler that runs past it is cancelled on the server, not left holding a
pooled connection, and answered 504 pgmi.deadline_exceeded."""

import json
import time
import unittest

from harness import request, world

DEADLINE = 2


class DeadlineTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.world = world()
        cls.gateway = cls.world.start_gateway(REQUEST_DEADLINE_SECONDS=str(DEADLINE), POOL_MAX_SIZE="1")

    def assert_cancelled_on_the_server(self):
        settle = time.monotonic() + 3
        while self.world.backends_sleeping() and time.monotonic() < settle:
            time.sleep(0.1)
        self.assertEqual(self.world.backends_sleeping(), 0, "the slow statement still runs")

    def test_the_deadline_is_the_statement_timeout(self):
        response = request(self.gateway.base, "GET", "/api/transport/statement-timeout")
        self.assertEqual(response.status, 200, response.body)
        self.assertTrue(0 < response.json()["milliseconds"] <= DEADLINE * 1000, response.body)

    def test_a_rest_handler_past_the_deadline_is_504_and_cancelled(self):
        started = time.monotonic()
        response = request(self.gateway.base, "GET", "/api/transport/slow")
        elapsed = time.monotonic() - started
        self.assertEqual(response.status, 504, response.body)
        self.assertTrue((response.header("Content-Type") or "").startswith("application/problem+json"))
        self.assertEqual(response.json()["code"], "pgmi.deadline_exceeded")
        self.assertLess(elapsed, DEADLINE + 2)
        self.assert_cancelled_on_the_server()
        after = request(self.gateway.base, "GET", "/api/transport/statement-timeout")
        self.assertEqual(after.status, 200, "the one pooled connection must serve the next request: " + after.body.decode())

    def test_an_mcp_tool_past_the_deadline_is_504_and_cancelled(self):
        body = {"jsonrpc": "2.0", "id": 7, "method": "tools/call",
                "params": {"name": "transport_slow", "arguments": {},
                           "_meta": {"io.modelcontextprotocol/protocolVersion": "2026-07-28",
                                     "io.modelcontextprotocol/clientCapabilities": {}}}}
        headers = [("Content-Type", "application/json"), ("MCP-Protocol-Version", "2026-07-28"),
                   ("Mcp-Method", "tools/call"), ("Mcp-Name", "transport_slow")]
        started = time.monotonic()
        response = request(self.gateway.base, "POST", "/mcp", headers, json.dumps(body).encode())
        elapsed = time.monotonic() - started
        self.assertEqual(response.status, 504, response.body)
        self.assertEqual(response.json()["id"], 7)
        self.assertEqual(response.json()["error"]["code"], -32603)
        self.assertEqual(response.json()["error"]["data"]["code"], "pgmi.deadline_exceeded")
        self.assertLess(elapsed, DEADLINE + 2)
        self.assert_cancelled_on_the_server()


class LongDeadlineTest(unittest.TestCase):
    """A request without a body is not cut off by the body-read deadline."""

    @classmethod
    def setUpClass(cls):
        cls.world = world()
        cls.gateway = cls.world.start_gateway(REQUEST_DEADLINE_SECONDS="60")

    def test_a_bodiless_request_outlives_the_body_read_deadline(self):
        response = request(self.gateway.base, "GET", "/api/transport/slower-than-a-body-read", timeout=60)
        self.assertEqual(response.status, 200, response.body)


if __name__ == "__main__":
    unittest.main()
