"""A client that hangs up mid-request frees its database connection: the
gateway cancels the running statement instead of letting it finish on a
pooled connection."""

import json
import socket
import time
import unittest

from harness import world

MODERN = "2026-07-28"


class HangupTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.world = world()

    def wait_for(self, condition, seconds):
        deadline = time.monotonic() + seconds
        while not condition() and time.monotonic() < deadline:
            time.sleep(0.05)
        return condition()

    def hang_up_mid_request(self, raw):
        host, port = self.world.base.removeprefix("http://").split(":")
        with socket.create_connection((host, int(port)), timeout=5) as sock:
            sock.sendall(raw)
            self.assertTrue(self.wait_for(lambda: self.world.backends_sleeping() > 0, 3),
                            "the slow statement never started")
        self.assertTrue(self.wait_for(lambda: self.world.backends_sleeping() == 0, 3),
                        "the slow statement still runs 3 s after the client hung up")

    def test_a_rest_client_that_hangs_up_cancels_its_statement(self):
        self.hang_up_mid_request(b"GET /api/transport/slow HTTP/1.1\r\nHost: x\r\n\r\n")

    def test_an_mcp_client_that_hangs_up_cancels_its_statement(self):
        body = json.dumps({"jsonrpc": "2.0", "id": 7, "method": "tools/call",
                           "params": {"name": "transport_slow", "arguments": {},
                                      "_meta": {"io.modelcontextprotocol/protocolVersion": MODERN,
                                                "io.modelcontextprotocol/clientCapabilities": {}}}}).encode()
        head = (f"POST /mcp HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n"
                f"MCP-Protocol-Version: {MODERN}\r\nMcp-Method: tools/call\r\nMcp-Name: transport_slow\r\n"
                f"Content-Length: {len(body)}\r\n\r\n").encode()
        self.hang_up_mid_request(head + body)


if __name__ == "__main__":
    unittest.main()
