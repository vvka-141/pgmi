"""OIDC sign-in against the mock provider, driven the way a browser does:
the happy path and every refusal of the login transport contract."""

import hashlib
import time
import unittest
import urllib.parse
import uuid

from harness import OPERATOR_EMAIL, OPERATOR_SUBJECT, Browser, free_port, request, world
from mock_idp import DROP, MockProvider, b64url


def with_query(url, **changes):
    parts = urllib.parse.urlsplit(url)
    query = dict(urllib.parse.parse_qsl(parts.query))
    query.update(changes)
    query = {k: v for k, v in query.items() if v is not None}
    return urllib.parse.urlunsplit(parts._replace(query=urllib.parse.urlencode(query)))


class LoginTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.world = world()
        cls.idp = cls.world.idp
        cls.base = cls.world.base

    # -- helpers -------------------------------------------------------------

    def start(self, browser, return_to="/app"):
        response = browser.request("GET", self.base + "/auth/login?" + urllib.parse.urlencode({"returnTo": return_to}))
        self.assertEqual(response.status, 302, response.body)
        return response

    def authorize(self, browser, subject, **claims):
        self.idp.next_login = {"sub": subject, "email": f"{subject}@example.com", **claims}
        location = self.start(browser).header("Location")
        response = browser.request("GET", location)
        self.assertEqual(response.status, 302, response.body)
        return response.header("Location")

    def sign_in(self, browser, subject, **claims):
        return browser.request("GET", self.authorize(browser, subject, **claims), cross_site=True)

    def signed_in(self, subject=None, **claims):
        browser = Browser()
        response = self.sign_in(browser, subject or "u-" + uuid.uuid4().hex[:12], **claims)
        self.assertEqual(response.status, 303, response.body)
        return browser

    def me(self, browser):
        return browser.request("GET", self.base + "/api/me")

    def assert_refused(self, response, status, code):
        self.assertEqual(response.status, status, response.body)
        self.assertEqual(response.json()["code"], code)

    def assert_no_session(self, browser):
        self.assertNotIn("__Host-session", browser.cookies)

    # -- sign-in -------------------------------------------------------------

    def test_happy_path_signs_in_and_reaches_me(self):
        browser = Browser()
        subject = "happy-" + uuid.uuid4().hex[:8]
        response = self.sign_in(browser, subject, name="Happy Person")
        self.assertEqual(response.status, 303, response.body)
        self.assertEqual(response.header("Location"), "/app")
        self.assertTrue(browser.cookies["__Host-session"].startswith("ls_"))

        me = self.me(browser)
        self.assertEqual(me.status, 200, me.body)
        self.assertEqual(me.json()["email"], f"{subject}@example.com")
        self.assertTrue(me.json()["emailVerified"])

    def test_return_to_stays_on_this_site(self):
        hostile = ["//evil.example/steal", "/\\evil.example", "https://evil.example/",
                   "/\r\nSet-Cookie: pwned=1", "/\t/evil.example", "/\n/evil.example", "/\x00x", "/caf€"]
        for return_to, expected in [(value, "/api/docs") for value in hostile] + [("/app?tab=2", "/app?tab=2")]:
            with self.subTest(return_to=return_to):
                browser = Browser()
                subject = "return-" + uuid.uuid4().hex[:8]
                self.idp.next_login = {"sub": subject, "email": f"{subject}@example.com"}
                location = self.start(browser, return_to).header("Location")
                callback = browser.request("GET", location).header("Location")
                response = browser.request("GET", callback)
                self.assertEqual(response.status, 303, response.body)
                self.assertEqual(response.header("Location"), expected)
                self.assertNotIn("pwned", browser.cookies)

    def test_session_cookie_flags(self):
        # The session and CSRF cookies are SameSite=Strict: a browser sends
        # them on no cross-site request, so a link on another site cannot
        # make a signed-in GET. The sign-in cookie stays Lax: the provider's
        # redirect back to /auth/callback is itself a cross-site navigation.
        def attributes(response, name):
            cookie = next(c for c in response.set_cookies() if c.startswith(name + "="))
            return [a.strip().lower() for a in cookie.split(";")[1:]]

        browser = Browser()
        self.assertIn("samesite=lax", attributes(self.start(browser), "__Host-login"))
        response = self.sign_in(browser, "cookie-" + uuid.uuid4().hex[:8])
        session = attributes(response, "__Host-session")
        self.assertIn("httponly", session)
        self.assertIn("secure", session)
        self.assertIn("path=/", session)
        self.assertIn("samesite=strict", session)
        self.assertFalse(any(a.startswith("domain") for a in session), session)
        csrf = attributes(response, "__Host-csrf")
        self.assertIn("secure", csrf)
        self.assertIn("samesite=strict", csrf)

    def test_sign_in_issues_a_new_handle_and_closes_the_old_one(self):
        planted = Browser()
        planted.cookies["__Host-session"] = "ls_planted_by_someone_else"
        planted.strict.add("__Host-session")
        subject = "fix-" + uuid.uuid4().hex[:8]
        self.assertEqual(self.sign_in(planted, subject).status, 303)
        first = planted.cookies["__Host-session"]
        self.assertNotEqual(first, "ls_planted_by_someone_else")

        self.assertEqual(self.sign_in(planted, subject).status, 303)
        second = planted.cookies["__Host-session"]
        self.assertNotEqual(second, first)

        holder = Browser()
        holder.cookies["__Host-session"] = first
        self.assertEqual(self.me(holder).status, 401)
        self.assertEqual(self.me(planted).status, 200)

    # -- state, PKCE, replay, the callback ---------------------------------------

    def test_wrong_state_is_refused(self):
        browser = Browser()
        callback = self.authorize(browser, "state-" + uuid.uuid4().hex[:8])
        self.assert_refused(browser.request("GET", with_query(callback, state="forged")), 400, "pgmi.login.state_invalid")
        self.assert_no_session(browser)

    def test_missing_state_is_refused(self):
        browser = Browser()
        callback = self.authorize(browser, "nostate-" + uuid.uuid4().hex[:8])
        self.assert_refused(browser.request("GET", with_query(callback, state=None)), 400, "pgmi.login.state_invalid")
        self.assert_no_session(browser)

    def test_callback_in_another_browser_is_refused(self):
        callback = self.authorize(Browser(), "csrf-login-" + uuid.uuid4().hex[:8])
        victim = Browser()
        self.assert_refused(victim.request("GET", callback), 400, "pgmi.login.state_invalid")
        self.assert_no_session(victim)

    def test_pkce_verifier_is_sent_and_callback_cannot_be_replayed(self):
        browser = Browser()
        callback = self.authorize(browser, "pkce-" + uuid.uuid4().hex[:8])
        login_cookie = browser.cookies["__Host-login"]
        challenge = self.idp.authorize_requests[-1]["code_challenge"]
        self.assertEqual(self.idp.authorize_requests[-1]["code_challenge_method"], "S256")
        self.assertEqual(browser.request("GET", callback).status, 303)
        verifier = self.idp.token_requests[-1]["code_verifier"]
        self.assertEqual(b64url(hashlib.sha256(verifier.encode()).digest()), challenge)

        replay = Browser()
        replay.cookies["__Host-login"] = login_cookie
        self.assert_refused(replay.request("GET", callback), 400, "pgmi.login.state_invalid")
        self.assert_no_session(replay)

    def test_an_error_from_the_provider_is_refused(self):
        browser = Browser()
        authorize = self.start(browser).header("Location")
        state = dict(urllib.parse.parse_qsl(urllib.parse.urlsplit(authorize).query))["state"]
        callback = self.base + "/auth/callback?" + urllib.parse.urlencode({"state": state, "error": "access_denied"})
        self.assert_refused(browser.request("GET", callback), 400, "pgmi.login.provider_error")
        self.assert_no_session(browser)

    # -- the ID token ----------------------------------------------------------

    def refused_token(self, **claims):
        browser = Browser()
        response = self.sign_in(browser, "token-" + uuid.uuid4().hex[:8], **claims)
        self.assert_refused(response, 401, "pgmi.login.token_invalid")
        self.assert_no_session(browser)

    def test_wrong_nonce_is_refused(self):
        self.refused_token(nonce="not-the-nonce")

    def test_missing_nonce_is_refused(self):
        self.refused_token(nonce=DROP)

    def test_wrong_audience_is_refused(self):
        self.refused_token(aud="another-client")

    def test_several_audiences_need_azp_naming_this_client(self):
        self.refused_token(aud=["pgmi-gateway", "another-client"])
        self.refused_token(aud=["pgmi-gateway", "another-client"], azp="another-client")
        self.signed_in(aud=["pgmi-gateway", "another-client"], azp="pgmi-gateway")

    def test_expired_token_is_refused(self):
        self.refused_token(exp=int(time.time()) - 3600, iat=int(time.time()) - 7200)

    def test_missing_iat_is_refused(self):
        self.refused_token(iat=DROP)

    def test_a_burst_of_anonymous_sign_ins_from_one_address_is_429(self):
        gateway = self.world.start_gateway(RATE_LIMIT_PER_IP="5")
        statuses = [request(gateway.base, "GET", "/auth/login").status for _ in range(10)]
        self.assertIn(429, statuses, statuses)
        refused = request(gateway.base, "GET", "/auth/login")
        self.assertEqual(refused.status, 429, refused.body)
        self.assertEqual(refused.json()["code"], "pgmi.rate_limited")
        self.assertGreaterEqual(int(refused.header("Retry-After")), 1)
        self.assertEqual(request(gateway.base, "GET", "/auth/callback?code=x&state=y").status, 429,
                         "the callback shares the limit")

    def test_wrong_issuer_is_refused(self):
        self.refused_token(iss="https://idp.evil.example")

    def test_bad_signature_is_refused(self):
        self.refused_token(_rogue_key=True)

    def test_alg_none_is_refused(self):
        self.refused_token(_alg="none")

    def test_hs256_keyed_with_the_public_key_is_refused(self):
        self.refused_token(_alg="HS256-public")

    def test_a_rotated_key_is_fetched_and_accepted(self):
        self.signed_in()
        fetched = self.idp.jwks_requests
        self.idp.rotate()
        self.signed_in()
        self.assertGreater(self.idp.jwks_requests, fetched, "an unknown kid must refetch the JWKS")
        self.refused_token(_rogue_key=True)

    # -- the provider ------------------------------------------------------------

    def test_a_provider_that_cannot_be_reached_is_502(self):
        browser = Browser()
        callback = self.authorize(browser, "down-" + uuid.uuid4().hex[:8])
        self.idp.stop()
        try:
            response = browser.request("GET", callback)
        finally:
            self.idp.start()
        self.assert_refused(response, 502, "pgmi.login.provider_unavailable")
        self.assert_no_session(browser)

    def test_keys_that_cannot_be_fetched_are_502(self):
        self.signed_in()
        self.idp.rotate()
        self.idp.jwks_down = True
        browser = Browser()
        try:
            response = browser.request("GET", self.authorize(browser, "keys-" + uuid.uuid4().hex[:8]))
        finally:
            self.idp.jwks_down = False
        self.assert_refused(response, 502, "pgmi.login.provider_unavailable")
        self.assert_no_session(browser)

    def test_discovery_naming_another_issuer_is_refused(self):
        """Refused at startup or at /auth/login with 502; never trusted."""
        impostor = MockProvider(free_port(), discovery_issuer="https://idp.elsewhere.example")
        try:
            gateway = self.world.start_gateway(issuer=impostor.issuer, must_start=False)
            if gateway is None:
                return
            response = Browser().request("GET", gateway.base + "/auth/login?returnTo=/")
            self.assert_refused(response, 502, "pgmi.login.provider_unavailable")
        finally:
            impostor.stop()

    # -- claims the database decides on ----------------------------------------

    def test_unverified_email_does_not_link_to_an_existing_person(self):
        email = "link-" + uuid.uuid4().hex[:8] + "@example.com"
        self.signed_in(email=email)
        for verified in (False, "true", DROP):
            with self.subTest(email_verified=verified):
                browser = Browser()
                response = self.sign_in(browser, "u-" + uuid.uuid4().hex[:12], email=email, email_verified=verified)
                self.assert_refused(response, 403, "membership.login.email_unverified")
                self.assert_no_session(browser)

    def test_platform_permission_needs_mfa_in_amr(self):
        for amr, expected in ((["pwd", "mfa"], 200), (["pwd"], 403), (DROP, 403), ("mfa", 403)):
            with self.subTest(amr=amr):
                browser = self.signed_in(OPERATOR_SUBJECT, email=OPERATOR_EMAIL, amr=amr)
                response = browser.request("GET", self.base + "/api/admin/dashboard")
                self.assertEqual(response.status, expected, response.body)


if __name__ == "__main__":
    unittest.main()
