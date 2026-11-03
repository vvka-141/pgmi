"""The gateways/PROTOCOL.md wire rules on /api, signed in through
POST /auth/dev-login; every reference gateway passes this suite."""

import re
import unittest
import uuid

import psycopg

from harness import Browser, request, world

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

    def test_a_body_over_the_limit_is_413(self):
        response = request(self.base, "POST", "/api/echo", [("Content-Type", "application/json")],
                           b"a" * (2 * LIMIT))
        self.assert_problem(response, 413, "pgmi.body_too_large")

    def test_a_negative_content_length_is_400_at_once(self):
        response = request(self.base, "POST", "/api/echo",
                           [("Content-Type", "application/json"), ("Content-Length", "-1")], timeout=5)
        self.assertEqual(response.status, 400, response.body)

    # -- headers ----------------------------------------------------------------

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
                self.assert_problem(response, 400, "login.request_invalid")

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
        self.assertEqual(reached.status, 404, reached.body)
        self.assertEqual(reached.json()["code"], "not_a_member")

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
