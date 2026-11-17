"""The gateways/PROTOCOL.md wire rules on /api, signed in through
POST /auth/dev-login; every reference gateway passes this suite."""

import re
import socket
import time
import unittest
import uuid

import psycopg

from harness import Browser, free_port, request, world

LIMIT = 1024 * 1024


class RestTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.world = world()
        cls.base = cls.world.base

    def get(self, path, headers=()):
        return request(self.base, "GET", path, headers)

    def signed_in(self):
        browser = Browser()
        subject = "e2e-" + uuid.uuid4().hex[:12]
        response = browser.request("POST", self.base + "/auth/dev-login",
                                   body={"subject": subject, "email": subject + "@example.com",
                                         "displayName": subject})
        self.assertEqual(response.status, 204, response.body)
        self.assertIn("__Host-session", browser.cookies)
        self.assertIn("__Host-csrf", browser.cookies)
        return browser

    def assert_problem(self, response, status, code):
        self.assertEqual(response.status, status, response.body)
        self.assertTrue((response.header("Content-Type") or "").startswith("application/problem+json"),
                        response.header("Content-Type"))
        self.assertEqual(response.json()["code"], code)

    # -- the gateway's own paths -------------------------------------------------

    def test_health(self):
        response = self.get("/health")
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(response.json()["status"], "healthy")

    def test_a_connection_setting_the_driver_cannot_honour_never_connects(self):
        # channel_binding=require over a server without TLS cannot be met. A
        # gateway refuses it at startup, or its driver applies it and fails to
        # connect; it never connects without it.
        url = self.world.database_url + ("&" if "?" in self.world.database_url else "?") + "channel_binding=require"
        gateway = self.world.start_gateway(must_start=False, DATABASE_URL=url)
        if gateway is not None:
            docs = request(gateway.base, "GET", "/api/docs", ())
            self.assertGreaterEqual(docs.status, 500, docs.body)

    def test_an_unreachable_database_is_503_with_retry_after(self):
        unreachable = re.sub(r":\d+/", f":{free_port()}/", self.world.database_url, count=1)
        gateway = self.world.start_gateway(DATABASE_URL=unreachable)
        response = request(gateway.base, "GET", "/api/hello", ())
        self.assert_problem(response, 503, "pgmi.unavailable")
        self.assertIsNotNone(response.header("Retry-After"))

    def test_an_sslmode_that_cannot_be_met_stops_the_gateway_at_startup(self):
        for setting in ("sslmode=verify-full", "sslmode=bogus"):
            with self.subTest(setting=setting):
                url = self.world.database_url + ("&" if "?" in self.world.database_url else "?") + setting
                self.assertIsNone(self.world.start_gateway(must_start=False, DATABASE_URL=url))
                self.assertEqual(self.world.instances[-1].process.returncode, 1)

    def test_an_unknown_path_is_404_not_found(self):
        for method, path in [("GET", "/nowhere"), ("POST", "/nowhere"), ("GET", "/API/hello"),
                             ("GET", "/apix"), ("DELETE", "/auth/nowhere")]:
            with self.subTest(method=method, path=path):
                response = request(self.base, method, path, body=b"" if method != "GET" else None)
                self.assert_problem(response, 404, "pgmi.not_found")

    def test_the_path_reaches_the_database_as_sent(self):
        for path in ("/api//hello", "/api/./hello", "/api/x/../hello", "/api/%2e%2e/hello"):
            with self.subTest(path=path):
                response = self.get(path)
                self.assertNotIn(response.status, (301, 302, 307, 308), response.body)
                self.assertIsNone(response.header("Location"))
                # Every answer the database gives carries the catalog version;
                # a gateway's own fallback does not.
                self.assertIsNotNone(response.header("x-pgmi-catalog-version"),
                                     f"{path} was answered by the gateway, not the database")

    def test_the_trace_names_the_path_never_the_query(self):
        token = "tok" + uuid.uuid4().hex
        before = len(self.world.gateway.output())
        response = self.get("/api/hello?token=" + token)
        self.assertEqual(response.status, 200, response.body)
        logged = ""
        for _ in range(40):
            logged = self.world.gateway.output()[before:]
            if "/hello" in logged:
                break
            time.sleep(0.05)
        self.assertIn("/hello", logged)
        self.assertNotIn(token, logged)

    # -- bodies and lengths -----------------------------------------------------

    def test_head_content_length_is_the_get_length(self):
        get = self.get("/api/openapi.json")
        self.assertEqual(get.status, 200, get.body)
        head = request(self.base, "HEAD", "/api/openapi.json")
        self.assertEqual(head.status, 200)
        self.assertEqual(head.header("Content-Length"), str(len(get.body)))
        self.assertEqual(head.body, b"")

    def test_the_spec_docs_read_is_served_and_names_the_mount(self):
        docs = self.get("/api/docs")
        self.assertEqual(docs.status, 200, docs.body)
        match = re.search(rb'data-url="([^"]*)"', docs.body)
        self.assertIsNotNone(match, docs.body[:500])
        url = match.group(1).decode()
        spec = self.get(url if url.startswith("/") else "/api/" + url)
        self.assertEqual(spec.status, 200, spec.body)
        self.assertEqual(spec.json()["servers"][0]["url"], "/api")

    def test_vary_names_the_session_cookie_and_the_spec_matches_the_gateway(self):
        for response in (self.get("/api/hello"), self.signed_in().request("GET", self.base + "/api/me")):
            with self.subTest(status=response.status):
                vary = [v.strip().lower() for v in (response.header("Vary") or "").split(",")]
                self.assertIn("cookie", vary, vary)
                self.assertNotIn("x-login-session", vary, vary)

        browser = self.signed_in()
        spec = browser.request("GET", self.base + "/api/openapi.json").json()
        self.assertEqual(spec["components"]["securitySchemes"]["loginSession"],
                         spec["components"]["securitySchemes"]["loginSession"] | {"in": "cookie", "name": "__Host-session"})
        csrf = [p for p in spec["paths"]["/organization-sessions"]["post"]["parameters"] if p["name"] == "X-CSRF-Token"]
        self.assertEqual([(p["in"], p["required"]) for p in csrf], [("header", True)])

    def test_no_content_length_on_204_or_304(self):
        preflight = request(self.base, "OPTIONS", "/api/hello")
        self.assertEqual(preflight.status, 204, preflight.body)
        spec = self.get("/api/openapi.json")
        self.assertEqual(spec.status, 200, spec.body)
        unchanged = self.get("/api/openapi.json", [("If-None-Match", spec.header("ETag"))])
        self.assertEqual(unchanged.status, 304, unchanged.body)
        browser = Browser()
        signed_in = browser.request("POST", self.base + "/auth/dev-login",
                                    body={"subject": "len-" + uuid.uuid4().hex[:8], "email": "len@example.com"})
        self.assertEqual(signed_in.status, 204, signed_in.body)
        for response in (preflight, unchanged, signed_in):
            with self.subTest(status=response.status):
                self.assertIsNone(response.header("Content-Length"), response.headers)
                self.assertEqual(response.body, b"")

    def test_a_chunked_body_is_read_or_refused_411_never_run_empty(self):
        ping = b'{"jsonrpc": "2.0", "id": 1, "method": "ping"}'
        chunked = b"%x\r\n%s\r\n0\r\n\r\n" % (len(ping), ping)
        response = request(self.base, "POST", "/mcp",
                           [("Content-Type", "application/json"), ("Accept", "application/json, text/event-stream"),
                            ("Transfer-Encoding", "chunked")], chunked)
        if response.status == 411:
            self.assertEqual(response.json()["code"], "pgmi.length_required")
        else:
            self.assertEqual(response.status, 200, response.body)
            self.assertIsInstance(response.json().get("result"), dict, response.body)
            self.assertNotIn("error", response.json())

    def test_a_body_over_the_limit_is_413(self):
        response = request(self.base, "POST", "/api/echo", [("Content-Type", "application/json")],
                           b"a" * (2 * LIMIT))
        self.assert_problem(response, 413, "pgmi.body_too_large")

    def test_a_negative_content_length_is_400_at_once(self):
        response = request(self.base, "POST", "/api/echo",
                           [("Content-Type", "application/json"), ("Content-Length", "-1")], timeout=5)
        self.assertEqual(response.status, 400, response.body)

    # -- headers ----------------------------------------------------------------

    def test_request_headers_over_64_kib_are_431_and_never_logged(self):
        marker = "trace_" + uuid.uuid4().hex
        response = request(self.base, "GET", "/api/hello", [("X-Trace", marker), ("X-Big", "a" * 100_000)])
        self.assertEqual(response.status, 431, response.body)
        with psycopg.connect(self.world.admin) as conn:
            logged = conn.execute("SELECT count(*) FROM api.rest_exchange WHERE (request).headers->'x-trace' = %s",
                                  [marker]).fetchone()[0]
        self.assertEqual(logged, 0)

    def test_a_repeated_list_header_is_joined(self):
        response = self.get("/api/hello", [("Accept", "application/json"), ("Accept", "*/*")])
        self.assertEqual(response.status, 200, response.body)
        browser = self.signed_in()
        response = browser.request("GET", self.base + "/api/me", [("Accept", "application/json"), ("accept", "*/*")])
        self.assertEqual(response.status, 200, response.body)

    def test_a_repeated_single_valued_header_is_refused(self):
        browser = self.signed_in()
        for name in ("X-Organization-Session", "X-PGMI-Transaction-Isolation"):
            for second in (name, name.lower()):
                with self.subTest(name=name, second=second):
                    response = browser.request("GET", self.base + "/api/me", [(name, "a"), (second, "b")])
                    self.assert_problem(response, 400, "pgmi.header_collision")

    def test_an_unknown_isolation_level_is_400(self):
        response = self.get("/api/hello", [("X-PGMI-Transaction-Isolation", "snapshot")])
        self.assert_problem(response, 400, "pgmi.transaction_isolation_unsupported")

    def test_a_stalled_body_is_408_request_timeout(self):
        host, port = self.base.removeprefix("http://").split(":")
        with socket.create_connection((host, int(port)), timeout=45) as sock:
            sock.sendall(b"POST /api/echo HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n"
                         b"Content-Length: 100\r\n\r\n{")
            answer = b""
            while b"\r\n\r\n" not in answer or not answer.rstrip().endswith(b"}"):
                chunk = sock.recv(4096)
                if not chunk:
                    break
                answer += chunk
        status = answer.split(b" ", 2)[1] if answer.startswith(b"HTTP/") else b""
        self.assertEqual(status, b"408", answer[:300])
        self.assertIn(b'"pgmi.request_timeout"', answer)
        self.assertIn(b"application/problem+json", answer.lower())

    def test_a_handler_header_with_cr_lf_never_splits_into_headers(self):
        response = self.get("/api/transport/header-injection")
        for name, value in response.headers:
            self.assertNotEqual(name.lower(), "x-split", response.headers)
            if name.lower() == "set-cookie":
                self.assertNotIn("injected", value, response.headers)

    def test_client_sent_session_and_connection_headers_are_ignored(self):
        handle = self.signed_in().cookies["__Host-session"]
        holder = Browser()
        holder.cookies["__Host-session"] = handle
        self.assertEqual(holder.request("GET", self.base + "/api/me").status, 200)

        for name in ("X-Login-Session", "x-login-session", "X-LOGIN-SESSION", "X-Connection"):
            with self.subTest(name=name):
                response = Browser().request("GET", self.base + "/api/me", [(name, handle)])
                self.assertEqual(response.status, 401, response.body)

        marker = uuid.uuid4().hex
        response = holder.request("GET", self.base + "/api/me",
                                  [("X-Connection", "cn_" + marker), ("X-Trace", "trace_" + marker)])
        self.assertEqual(response.status, 200, response.body)
        with psycopg.connect(self.world.admin) as conn:
            logged = conn.execute(
                "SELECT (request).headers->'x-trace', (request).headers ? 'x-connection' FROM api.rest_exchange"
                " WHERE (request).headers->'x-trace' = %s", ["trace_" + marker]).fetchall()
        self.assertEqual(logged, [("trace_" + marker, False)])

    # -- sign-in, CSRF, Origin, logout ---------------------------------------------

    def test_dev_sign_in_refuses_a_malformed_body(self):
        for body in ({}, {"subject": 1, "email": "a@example.com"}, {"subject": "s"}):
            with self.subTest(body=body):
                response = Browser().request("POST", self.base + "/auth/dev-login", body=body)
                self.assert_problem(response, 400, "pgmi.login.request_invalid")

    def test_state_changing_request_needs_the_csrf_token(self):
        browser = self.signed_in()
        token = browser.cookies["__Host-csrf"]
        url = self.base + "/api/organization-sessions"
        body = {"organizationId": str(uuid.uuid4())}

        self.assert_problem(browser.request("POST", url, body=body), 403, "pgmi.csrf_refused")
        self.assert_problem(browser.request("POST", url, [("X-CSRF-Token", "forged")], body), 403, "pgmi.csrf_refused")
        other = self.signed_in()
        self.assert_problem(browser.request("POST", url, [("X-CSRF-Token", other.cookies["__Host-csrf"])], body),
                            403, "pgmi.csrf_refused")
        self.assert_problem(browser.request("POST", url, [("X-CSRF-Token", token), ("Origin", "https://evil.example")],
                                            body), 403, "pgmi.origin_refused")

        reached = browser.request("POST", url, [("X-CSRF-Token", token), ("Origin", self.base)], body)
        self.assertEqual(reached.status, 422, reached.body)
        self.assertEqual(reached.json()["code"], "membership.organization_session.not_member")

    def test_a_created_resource_answers_a_location_the_gateway_serves(self):
        browser = self.signed_in()
        headers = [("X-CSRF-Token", browser.cookies["__Host-csrf"]), ("Origin", self.base)]
        slug = "e2e-" + uuid.uuid4().hex[:12]
        created = browser.request("POST", self.base + "/api/organizations", headers, {"name": slug, "slug": slug})
        self.assertEqual(created.status, 201, created.body)
        self.assertEqual(created.header("Location"), "/api/organizations/" + created.json()["id"])
        reached = browser.request("GET", self.base + created.header("Location"))
        self.assertEqual(reached.status, 200, reached.body)
        self.assertEqual(reached.json(), created.json())

    def test_logout_needs_the_csrf_token(self):
        browser = self.signed_in()
        self.assert_problem(browser.request("POST", self.base + "/auth/logout"), 403, "pgmi.csrf_refused")
        self.assertEqual(browser.request("GET", self.base + "/api/me").status, 200)

    def test_logout_revokes_in_the_database_and_clears_the_cookies(self):
        browser = self.signed_in()
        handle = browser.cookies["__Host-session"]
        response = browser.request("POST", self.base + "/auth/logout",
                                   [("X-CSRF-Token", browser.cookies["__Host-csrf"])])
        self.assertEqual(response.status, 204, response.body)
        cleared = [c for c in response.set_cookies() if c.startswith("__Host-session=;")]
        self.assertEqual(len(cleared), 1, response.set_cookies())
        self.assertIn("max-age=0", cleared[0].lower().replace(" ", ""))
        self.assertNotIn("__Host-session", browser.cookies)

        reason = self.world.scalar("SELECT revoke_reason FROM membership.login_session"
                                   " WHERE token_hash = membership.fn_session_token_hash(%s)", [handle])
        self.assertEqual(reason, "logout")

        replay = Browser()
        replay.cookies["__Host-session"] = handle
        self.assertEqual(replay.request("GET", self.base + "/api/me").status, 401)


if __name__ == "__main__":
    unittest.main()
