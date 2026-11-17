"""POST /mcp the way an MCP client speaks it: both protocol eras, the
Streamable HTTP header rules, the status mapping, the serialization-failure
retry and pool exhaustion."""

import base64
import json
import threading
import time
import unittest
import uuid

from harness import RETRY_ATTEMPTS, Browser, request, world

MODERN = "2026-07-28"
META = {"io.modelcontextprotocol/protocolVersion": MODERN, "io.modelcontextprotocol/clientCapabilities": {}}


def b64(text):
    return "=?base64?" + base64.b64encode(text.encode()).decode() + "?="


def message(method, params=None, rid=7, meta=True):
    body = {"jsonrpc": "2.0", "method": method, "params": dict(params or {})}
    if meta:
        body["params"]["_meta"] = META
    if rid is not None:
        body["id"] = rid
    return body


def modern(method, **extra):
    return {"MCP-Protocol-Version": MODERN, "Mcp-Method": method, **extra}


class MCPTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.world = world()

    def post(self, body, headers=None, content_type="application/json", method="POST", raw=None, base=None):
        data = raw if raw is not None else (json.dumps(body).encode() if body is not None else b"")
        sent = [("Content-Type", content_type)] if content_type else []
        sent += list((headers or {}).items())
        return request(base or self.world.base, method, "/mcp", sent, data)

    def exchanges(self):
        return self.world.scalar("SELECT count(*) FROM api.mcp_exchange WHERE mcp_name = 'transport_echo'")

    def assert_rpc_error(self, response, status, code, rid=7):
        self.assertEqual(response.status, status, response.body)
        self.assertTrue((response.header("Content-Type") or "").startswith("application/json"))
        self.assertEqual(response.json()["jsonrpc"], "2.0")
        self.assertEqual(response.json()["id"], rid)
        self.assertEqual(response.json()["error"]["code"], code)

    def assert_mismatch(self, response, header, rid=7):
        self.assert_rpc_error(response, 400, -32020, rid)
        self.assertEqual(response.json()["error"]["data"], {"header": header})

    # -- docs/advanced/MCP-GATEWAY.md, Quick Start ----------------------------------

    def test_the_quick_start_sequence(self):
        base = self.world.base
        browser = Browser()
        subject = "quickstart-" + uuid.uuid4().hex[:8]
        signed_in = browser.request("POST", base + "/auth/dev-login",
                                    body={"subject": subject, "email": subject + "@example.com", "displayName": subject})
        self.assertEqual(signed_in.status, 204, signed_in.body)
        csrf = [("X-CSRF-Token", browser.cookies["__Host-csrf"])]

        org = browser.request("POST", base + "/api/organizations", csrf, {"name": "Acme " + subject, "slug": subject})
        self.assertEqual(org.status, 201, org.body)

        approved = browser.request("POST", base + "/api/connections", csrf,
                                   {"clientName": "Desktop assistant", "organizationId": org.json()["id"],
                                    "roleId": "agent.contributor", "validDays": 90})
        self.assertEqual(approved.status, 201, approved.body)
        handle = approved.json()["connection"]

        def tools(headers):
            response = self.post(message("tools/list", rid=1), {**modern("tools/list"), **headers})
            self.assertEqual(response.status, 200, response.body)
            return {t["name"] for t in response.json()["result"]["tools"]}

        as_agent = tools({"X-Connection": handle})
        self.assertIn("transport_whoami", as_agent, "an authenticated tool is listed for the agent")
        self.assertNotIn("transport_whoami", tools({}), "and not for an anonymous caller")

    # -- 2026-07-28 ------------------------------------------------------------

    def test_matching_headers_reach_the_database(self):
        for method, params, extra in [
            ("tools/list", {}, {}),
            ("tools/call", {"name": "transport_echo", "arguments": {"a": 1}}, {"Mcp-Name": "transport_echo"}),
            ("tools/call", {"name": "transport_echo", "arguments": {}}, {"Mcp-Name": b64("transport_echo")}),
            ("resources/read", {"uri": "note://transport"}, {"Mcp-Name": "note://transport"}),
        ]:
            with self.subTest(method=method, extra=extra):
                response = self.post(message(method, params), modern(method, **extra))
                self.assertEqual(response.status, 200, response.body)
                self.assertEqual(response.json()["result"]["resultType"], "complete")
                self.assertEqual(response.json()["id"], 7)

    def test_server_discover(self):
        response = self.post(message("server/discover"), modern("server/discover"))
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(response.json()["result"]["supportedVersions"], [MODERN])
        self.assertEqual(response.json()["result"]["cacheScope"], "public")

    def test_a_header_mismatch_is_400_and_never_reaches_the_database(self):
        before = self.exchanges()
        call = message("tools/call", {"name": "transport_echo", "arguments": {}})
        for label, body, headers, header in [
            ("no Mcp-Method", message("tools/list"), {"MCP-Protocol-Version": MODERN}, "Mcp-Method"),
            ("another Mcp-Method", message("tools/list"), modern("prompts/list"), "Mcp-Method"),
            ("no Mcp-Name", call, modern("tools/call"), "Mcp-Name"),
            ("another Mcp-Name", call, modern("tools/call", **{"Mcp-Name": "other"}), "Mcp-Name"),
            ("a Base64 Mcp-Name of another name", call, modern("tools/call", **{"Mcp-Name": b64("other")}), "Mcp-Name"),
            ("an Mcp-Name that is not Base64", call, modern("tools/call", **{"Mcp-Name": "=?base64?***?="}), "Mcp-Name"),
            ("no _meta", message("tools/list", meta=False), modern("tools/list"), "MCP-Protocol-Version"),
            ("a modern body without the version header", call, {}, "MCP-Protocol-Version"),
            ("a modern body under a legacy header", call,
             {"MCP-Protocol-Version": "2025-11-25", "Mcp-Method": "tools/call", "Mcp-Name": "transport_echo"},
             "MCP-Protocol-Version"),
        ]:
            with self.subTest(label):
                self.assert_mismatch(self.post(body, headers), header)
        self.assertEqual(self.exchanges(), before, "a mismatched call reached the database")

    def test_a_request_without_client_capabilities_is_400(self):
        before = self.exchanges()
        body = message("tools/call", {"name": "transport_echo", "arguments": {}})
        body["params"]["_meta"] = {"io.modelcontextprotocol/protocolVersion": MODERN}
        self.assert_rpc_error(self.post(body, modern("tools/call", **{"Mcp-Name": "transport_echo"})), 400, -32602)
        self.assertEqual(self.exchanges(), before, "a malformed request reached the database")

    def test_unsupported_version_is_400_unknown_method_404(self):
        body = message("tools/list")
        body["params"]["_meta"] = {**META, "io.modelcontextprotocol/protocolVersion": "2026-09-01"}
        response = self.post(body, modern("tools/list", **{"MCP-Protocol-Version": "2026-09-01"}))
        self.assert_rpc_error(response, 400, -32022)
        self.assertEqual(response.json()["error"]["data"], {"supported": [MODERN], "requested": "2026-09-01"})

        call = message("tools/call", {"name": "transport_echo", "arguments": {}})
        response = self.post(call, modern("tools/call", **{"Mcp-Name": "transport_echo", "MCP-Protocol-Version": "2026-09-01"}))
        self.assert_rpc_error(response, 400, -32022)
        self.assertEqual(response.json()["error"]["data"], {"supported": [MODERN], "requested": "2026-09-01"})

        self.assert_rpc_error(self.post(message("ping"), modern("ping")), 404, -32601)

    def test_an_unknown_tool_is_200_invalid_params(self):
        body = message("tools/call", {"name": "no_such_tool", "arguments": {}})
        response = self.post(body, modern("tools/call", **{"Mcp-Name": "no_such_tool"}))
        self.assert_rpc_error(response, 200, -32602)

    def test_a_notification_is_202_without_a_body(self):
        note = message("notifications/initialized", rid=None, meta=False)
        response = self.post(note, modern("notifications/initialized"))
        self.assertEqual(response.status, 202, response.body)
        self.assertEqual(response.body, b"")
        self.assert_mismatch(self.post(note, {"MCP-Protocol-Version": MODERN}), "Mcp-Method", rid=None)

    def test_no_credential_is_anonymous(self):
        body = message("tools/call", {"name": "transport_whoami", "arguments": {}})
        self.assert_rpc_error(self.post(body, modern("tools/call", **{"Mcp-Name": "transport_whoami"})), 200, -32602)
        self.assertEqual(self.post(message("tools/list"), modern("tools/list")).status, 200)

    def test_session_headers_are_ignored(self):
        response = self.post(message("tools/list"),
                             modern("tools/list", **{"Mcp-Session-Id": "abc", "Last-Event-ID": "9"}))
        self.assertEqual(response.status, 200, response.body)
        self.assertIsNone(response.header("Mcp-Session-Id"))

    # -- legacy ----------------------------------------------------------------

    def test_a_legacy_client_initializes_and_lists(self):
        response = self.post({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                              "params": {"protocolVersion": "2025-11-25", "capabilities": {}}})
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(response.json()["result"]["protocolVersion"], "2025-11-25")
        response = self.post({"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
                             {"MCP-Protocol-Version": "2025-11-25"})
        self.assertEqual(response.status, 200, response.body)
        self.assertIn("transport_echo", [t["name"] for t in response.json()["result"]["tools"]])

    def test_a_legacy_body_reaches_the_database_unchecked(self):
        before = self.exchanges()
        response = self.post({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
                              "params": {"name": "transport_echo", "arguments": {}}},
                             {"MCP-Protocol-Version": "2025-11-25"})
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(self.exchanges(), before + 1)
        response = self.post({"jsonrpc": "2.0", "id": 4, "method": "nope"}, {"MCP-Protocol-Version": "2025-06-18"})
        self.assert_rpc_error(response, 200, -32601, rid=4)

    def test_an_unknown_legacy_version_is_refused(self):
        response = self.post({"jsonrpc": "2.0", "id": 5, "method": "tools/list"}, {"MCP-Protocol-Version": "2023-01-01"})
        self.assert_rpc_error(response, 400, -32022, rid=5)
        error = response.json()["error"]
        self.assertEqual(error["data"]["requested"], "2023-01-01")
        self.assertIn("2025-11-25", error["data"]["supported"])
        self.assertIn(MODERN, error["data"]["supported"])

    # -- the HTTP surface ---------------------------------------------------------

    def test_only_post(self):
        for method in ("GET", "DELETE", "PUT"):
            with self.subTest(method):
                response = self.post(None, method=method, content_type=None)
                self.assert_gateway_problem(response, 405, "pgmi.method_not_allowed")
                self.assertEqual(response.header("Allow"), "POST")

    def test_not_json_is_415(self):
        response = self.post(None, modern("tools/list"), content_type="text/plain",
                             raw=json.dumps(message("tools/list")).encode())
        self.assert_gateway_problem(response, 415, "pgmi.unsupported_media_type")

    def test_an_accept_that_refuses_json_is_406(self):
        response = self.post(message("tools/list"), {**modern("tools/list"), "Accept": "text/event-stream"})
        self.assert_gateway_problem(response, 406, "pgmi.not_acceptable")

    def assert_gateway_problem(self, response, status, code):
        self.assertEqual(response.status, status, response.body)
        self.assertTrue((response.header("Content-Type") or "").startswith("application/problem+json"), response.body)
        self.assertEqual(response.json()["code"], code)

    def test_an_unparsable_body_is_400_parse_error(self):
        for raw in (b"not-json", b"\xff\xfe{}", b""):
            with self.subTest(raw):
                self.assert_rpc_error(self.post(None, raw=raw), 400, -32700, rid=None)

    def test_a_body_jsonb_refuses_is_400_parse_error_not_a_server_error(self):
        for raw in (b'{"jsonrpc":"2.0","id":"a\\u0000b","method":"ping"}',
                    b'{"jsonrpc":"2.0","id":"a\\ud800b","method":"ping"}'):
            with self.subTest(raw):
                before = len(self.world.gateway.output())
                self.assert_rpc_error(self.post(None, raw=raw), 400, -32700, rid=None)
                logged = self.world.gateway.output()[before:]
                for sign in ("22P05", "22P02", "Unicode escape", "invalid input syntax"):
                    self.assertNotIn(sign, logged)

    def test_a_disallowed_origin_is_403(self):
        response = self.post(message("tools/list"), {**modern("tools/list"), "Origin": "http://evil.example"})
        self.assert_gateway_problem(response, 403, "pgmi.origin_refused")
        response = self.post(message("tools/list"), {**modern("tools/list"), "Origin": self.world.base})
        self.assertEqual(response.status, 200, response.body)

    # -- serialization failures ------------------------------------------------

    def test_a_serialization_failure_is_retried(self):
        start = self.world.scalar("SELECT nextval('public.transport_attempt')")
        if start % 2 == 1:
            start = self.world.scalar("SELECT nextval('public.transport_attempt')")
        body = message("tools/call", {"name": "transport_conflict_once", "arguments": {}})
        response = self.post(body, modern("tools/call", **{"Mcp-Name": "transport_conflict_once"}))
        after = self.world.scalar("SELECT nextval('public.transport_attempt')")
        self.assertEqual(after - start - 1, 2, "the first attempt must lose and the second run again")
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(response.json()["result"]["content"][0]["text"], "done")

    def test_exhausted_retries_answer_the_retryable_shape(self):
        before = self.world.scalar("SELECT nextval('public.transport_attempt')")
        body = message("tools/call", {"name": "transport_conflict_always", "arguments": {}})
        response = self.post(body, modern("tools/call", **{"Mcp-Name": "transport_conflict_always"}))
        after = self.world.scalar("SELECT nextval('public.transport_attempt')")
        self.assertEqual(after - before - 1, RETRY_ATTEMPTS, "each attempt runs the whole transaction again")
        self.assert_rpc_error(response, 200, -31000)
        self.assertEqual(response.header("Retry-After"), "1")
        self.assertEqual(response.json()["error"]["data"]["code"], "pgmi.transaction_retryable")
        self.assertEqual(response.json()["error"]["data"]["attempts"], RETRY_ATTEMPTS)

    # -- pool exhaustion ---------------------------------------------------------

    def test_an_exhausted_pool_is_503_unavailable(self):
        gateway = self.world.start_gateway(POOL_MAX_SIZE="1")
        slow = message("tools/call", {"name": "transport_slow", "arguments": {}})
        holder = threading.Thread(target=self.post, daemon=True,
                                  args=(slow, modern("tools/call", **{"Mcp-Name": "transport_slow"})),
                                  kwargs={"base": gateway.base})
        holder.start()
        time.sleep(1)
        response = self.post(message("tools/list"), modern("tools/list"), base=gateway.base)
        holder.join()
        self.assertEqual(response.status, 503, response.body)
        self.assertEqual(response.header("Retry-After"), "1")
        self.assertEqual(response.json()["id"], 7)
        self.assertEqual(response.json()["error"]["code"], -31000)
        self.assertEqual(response.json()["error"]["data"]["code"], "pgmi.unavailable")


if __name__ == "__main__":
    unittest.main()
