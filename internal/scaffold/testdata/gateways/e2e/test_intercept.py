"""The two extension seams of gateways/PROTOCOL.md ("Extension seams"), seen
from outside: without interceptors the bytes pass untouched; with the example
interceptors (INTERCEPTORS=embed,request-id) a body gains the fake embedder's
vector for each x-embed field and every answer an X-Request-Id; both failure
policies; concurrent load with and without interceptors; and with
INTERCEPTORS=header-names, that interceptors see every header the client
sent, credentials included, while identity stays the gateway's."""

import hashlib
import json
import statistics
import time
import unittest
import uuid
from concurrent.futures import ThreadPoolExecutor

from harness import Browser, request, world

MODERN = "2026-07-28"
META = {"io.modelcontextprotocol/protocolVersion": MODERN, "io.modelcontextprotocol/clientCapabilities": {}}
ECHO_HANDLER = "f4500000-0000-4000-8000-0000000000f1"
LIMIT = 2000
ODD_BYTES = '{ "text" :"h\\u00e9llo wörld",\n\t"n": 1.50, "z":[ ] }'.encode()


def fake_embedding(text):
    """The example embedder every gateway ships: the first four bytes of the
    SHA-256 of the UTF-8 text, each divided by 255, rounded to 4 places."""
    return [round(b / 255, 4) for b in hashlib.sha256(text.encode()).digest()[:4]]


def post(base, path, body, headers=()):
    return request(base, "POST", path, [("Content-Type", "application/json"), *headers], body)


def call_tool(base, arguments, headers=()):
    body = {"jsonrpc": "2.0", "id": 9, "method": "tools/call",
            "params": {"name": "intercept_echo", "arguments": arguments, "_meta": META}}
    return post(base, "/mcp", json.dumps(body).encode(),
                [("MCP-Protocol-Version", MODERN), ("Mcp-Method", "tools/call"), ("Mcp-Name", "intercept_echo"),
                 *headers])


class InterceptTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.world = world()
        cls.plain = cls.world.base
        cls.intercepting = cls.world.start_gateway(INTERCEPTORS="embed,request-id").base
        cls.degrading = cls.world.start_gateway(INTERCEPTORS="embed", EMBED_FAILURE_POLICY="degrade").base
        cls.naming = cls.world.start_gateway(INTERCEPTORS="header-names").base

    def echo_calls(self):
        return self.world.scalar("SELECT count(*) FROM api.rest_exchange WHERE handler_object_id = %s",
                                 [ECHO_HANDLER])

    def assert_problem(self, response, status, code):
        self.assertEqual(response.status, status, response.body)
        self.assertTrue((response.header("Content-Type") or "").startswith("application/problem+json"))
        self.assertEqual(response.json()["code"], code)

    # -- zero cost ---------------------------------------------------------------

    def test_without_interceptors_the_bytes_pass_untouched(self):
        response = post(self.plain, "/api/intercept/echo", ODD_BYTES)
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(response.body, ODD_BYTES)
        self.assertIsNone(response.header("X-Request-Id"))

    def test_a_route_the_embedder_does_not_select_passes_untouched(self):
        response = post(self.intercepting, "/api/intercept/plain", ODD_BYTES)
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(response.body, ODD_BYTES)
        self.assertTrue(response.header("X-Request-Id"))

    # -- the examples -------------------------------------------------------------

    def test_rest_gains_a_vector_for_each_x_embed_field(self):
        response = post(self.intercepting, "/api/intercept/echo", ODD_BYTES)
        self.assertEqual(response.status, 200, response.body)
        body = response.json()
        self.assertEqual(body["text"], "héllo wörld")
        self.assertEqual(body["n"], 1.5)
        self.assertEqual(body["text_embedding"], fake_embedding("héllo wörld"))
        self.assertTrue(response.header("X-Request-Id"))

    def test_a_vector_the_client_sent_is_kept(self):
        response = post(self.intercepting, "/api/intercept/echo", b'{"text": "a", "text_embedding": [1]}')
        self.assertEqual(response.json()["text_embedding"], [1], response.body)

    def test_mcp_arguments_gain_the_vector(self):
        response = call_tool(self.intercepting, {"text": "hello"})
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(response.json()["result"]["structuredContent"],
                         {"text": "hello", "text_embedding": fake_embedding("hello")})
        self.assertTrue(response.header("X-Request-Id"))

    # -- what an interceptor sees --------------------------------------------------

    def signed_in(self, base):
        browser = Browser()
        subject = "names-" + uuid.uuid4().hex[:12]
        response = browser.request("POST", base + "/auth/dev-login",
                                   body={"subject": subject, "email": subject + "@example.com", "displayName": subject})
        self.assertEqual(response.status, 204, response.body)
        return browser

    def connection_handle(self, base):
        browser = self.signed_in(base)
        csrf = [("X-CSRF-Token", browser.cookies["__Host-csrf"])]
        slug = "names-" + uuid.uuid4().hex[:12]
        org = browser.request("POST", base + "/api/organizations", csrf, {"name": "Names " + slug, "slug": slug})
        self.assertEqual(org.status, 201, org.body)
        approved = browser.request("POST", base + "/api/connections", csrf,
                                   {"clientName": "Probe", "organizationId": org.json()["id"],
                                    "roleId": "agent.reader", "validDays": 1})
        self.assertEqual(approved.status, 201, approved.body)
        return approved.json()["connection"]

    def test_interceptors_see_every_header_credentials_included_names_only(self):
        secret = "secret" + uuid.uuid4().hex
        want = {"authorization", "cookie", "x-connection", "x-login-session", "x-probe", "content-type"}
        rest = post(self.naming, "/api/intercept/echo", b'{"text": "a"}',
                    [("Authorization", "Bearer " + secret), ("Cookie", "a=" + secret),
                     ("X-Connection", "cn_" + secret), ("X-Login-Session", "ls_" + secret), ("X-Probe", secret)])
        self.assertEqual(rest.status, 200, rest.body)
        self.assertLessEqual(want, set(rest.json()["headerNames"]), rest.body)
        self.assertNotIn(secret.encode(), rest.body)

        handle = self.connection_handle(self.naming)
        mcp = call_tool(self.naming, {"text": "a"},
                        [("Authorization", "Bearer " + handle), ("Cookie", "a=" + secret), ("X-Connection", handle),
                         ("X-Login-Session", "ls_" + secret), ("X-Probe", secret)])
        self.assertEqual(mcp.status, 200, mcp.body)
        self.assertLessEqual(want, set(mcp.json()["result"]["structuredContent"]["headerNames"]), mcp.body)
        self.assertNotIn(secret.encode(), mcp.body)

    def test_credentials_an_interceptor_sees_do_not_change_identity(self):
        browser = self.signed_in(self.naming)
        me = browser.request("GET", self.naming + "/api/me")
        self.assertEqual(me.status, 200, me.body)
        forged = browser.request("GET", self.naming + "/api/me",
                                 [("X-Login-Session", "ls_forged"), ("X-Connection", "cn_forged"),
                                  ("Authorization", "Bearer cn_forged")])
        self.assertEqual(forged.status, 200, forged.body)
        self.assertEqual(forged.json(), me.json())

    # -- failure policies ------------------------------------------------------------

    def test_fail_answers_502_and_never_reaches_the_database(self):
        before = self.echo_calls()
        response = post(self.intercepting, "/api/intercept/echo", json.dumps({"text": "x" * (LIMIT + 1)}).encode())
        self.assert_problem(response, 502, "pgmi.interceptor_failed")
        self.assertEqual(self.echo_calls(), before)

        response = call_tool(self.intercepting, {"text": "x" * (LIMIT + 1)})
        self.assertEqual(response.status, 502, response.body)
        self.assertEqual(response.json()["id"], 9)
        self.assertEqual(response.json()["error"]["data"]["code"], "pgmi.interceptor_failed")

    def test_degrade_goes_on_with_the_request_as_it_was(self):
        response = post(self.degrading, "/api/intercept/echo", b'{"text": "short"}')
        self.assertEqual(response.json().get("text_embedding"), fake_embedding("short"), response.body)

        sent = json.dumps({"text": "x" * (LIMIT + 1)}).encode()
        response = post(self.degrading, "/api/intercept/echo", sent)
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(response.body, sent)

        response = call_tool(self.degrading, {"text": "x" * (LIMIT + 1)})
        self.assertEqual(response.status, 200, response.body)
        self.assertNotIn("text_embedding", response.json()["result"]["structuredContent"])

    # -- load -----------------------------------------------------------------------

    def test_concurrent_load_with_and_without_interceptors(self):
        threads, per_thread = 8, 25
        scenarios = [
            ("no interceptors, declared route", self.plain, "/api/intercept/echo", None),
            ("interceptors, undeclared route", self.intercepting, "/api/intercept/plain", None),
            ("interceptors, declared route", self.intercepting, "/api/intercept/echo", "text_embedding"),
        ]
        report = []
        for label, base, path, added in scenarios:
            def one(i):
                text = "load %d" % i
                sent = json.dumps({"text": text}).encode()
                started = time.monotonic()
                response = post(base, path, sent)
                elapsed = time.monotonic() - started
                if response.status != 200:
                    return elapsed, "status %d: %r" % (response.status, response.body)
                if added is None and response.body != sent:
                    return elapsed, "changed: %r" % response.body
                if added is not None and response.json().get(added) != fake_embedding(text):
                    return elapsed, "wrong vector: %r" % response.body
                return elapsed, None

            started = time.monotonic()
            with ThreadPoolExecutor(threads) as pool:
                results = list(pool.map(one, range(threads * per_thread)))
            wall = time.monotonic() - started
            errors = [e for _, e in results if e]
            self.assertEqual(errors, [], label)
            latencies = sorted(t for t, _ in results)
            report.append("%-34s %4d req %6.0f req/s  p50 %5.1f ms  p95 %5.1f ms" % (
                label, len(results), len(results) / wall, statistics.median(latencies) * 1000,
                latencies[int(len(latencies) * 0.95)] * 1000))
        print("\n" + "\n".join(report))
