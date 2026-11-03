"""/mcp as an OAuth resource server: the protected-resource metadata, the 401
challenge, the strict audience, pairing an unknown client and approving it
on the web, pause, connection handles (X-Connection or Authorization: Bearer
cn_...) with and without OAuth, and the rate limits."""

import json
import unittest
import uuid

from harness import Browser, free_port, request, world
from mock_idp import CLIENT_ID, DROP

MODERN = "2026-07-28"
META = {"io.modelcontextprotocol/protocolVersion": MODERN, "io.modelcontextprotocol/clientCapabilities": {}}
WHOAMI = {"jsonrpc": "2.0", "id": 9, "method": "tools/call",
          "params": {"name": "transport_whoami", "arguments": {}, "_meta": META}}
WHOAMI_HEADERS = {"MCP-Protocol-Version": MODERN, "Mcp-Method": "tools/call", "Mcp-Name": "transport_whoami"}


def oauth_env(**extra):
    return {"MCP_RESOURCE": "{base}/mcp", "MCP_AUTHORIZATION_SERVERS": world().idp.issuer, **extra}


class OAuthTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.world = world()
        cls.idp = cls.world.idp
        cls.gateway = cls.world.start_gateway(**oauth_env())
        cls.base = cls.gateway.base
        cls.resource = cls.base + "/mcp"

    # -- helpers ---------------------------------------------------------------

    def token(self, subject="nobody", client_id="desktop-agent", audience=None, **claims):
        return self.idp.access_token(subject, client_id, audience or self.resource, **claims)

    def call(self, token=None, base=None, headers=None, body=WHOAMI):
        sent = [("Content-Type", "application/json")] + list(WHOAMI_HEADERS.items()) + list((headers or {}).items())
        if token is not None:
            sent.append(("Authorization", "Bearer " + token))
        return request(base or self.base, "POST", "/mcp", sent, json.dumps(body).encode())

    def refusal(self, response):
        self.assertEqual(response.status, 200, response.body)
        error = response.json()["error"]
        self.assertEqual(error["code"], -31001, error)
        self.assertEqual(response.json()["id"], 9)
        return error["data"]

    def assert_challenge(self, response, error=None):
        self.assertEqual(response.status, 401, response.body)
        challenge = response.header("WWW-Authenticate") or ""
        self.assertTrue(challenge.startswith("Bearer "), challenge)
        self.assertIn(f'resource_metadata="{self.base}/.well-known/oauth-protected-resource/mcp"', challenge)
        if error:
            self.assertIn(f'error="{error}"', challenge)
        else:
            self.assertNotIn("error=", challenge)

    def signed_in(self, subject):
        browser = Browser()
        self.idp.next_login = {"sub": subject, "email": f"{subject}@example.com"}
        location = browser.request("GET", self.base + "/auth/login?returnTo=/").header("Location")
        callback = browser.request("GET", location).header("Location")
        self.assertEqual(browser.request("GET", callback).status, 303)
        return browser

    def organization(self, subject):
        person = self.world.scalar("SELECT user_principal_object_id FROM membership.external_identity "
                                   "WHERE issuer_id = %s AND subject = %s", (self.idp.issuer, subject))
        slug = "oauth-" + uuid.uuid4().hex[:10]
        org = self.world.scalar("SELECT (membership.fn_create_organization_with_owner(%s, %s, %s)).object_id",
                                ("OAuth " + slug, slug, person))
        return person, org

    def web(self, browser, method, path, body=None):
        return browser.request(method, self.base + "/api" + path,
                               [("X-CSRF-Token", browser.cookies["__Host-csrf"])], body)

    def expected_permissions(self, person, org, role):
        return self.world.scalar(
            "SELECT coalesce(jsonb_agg(rp.permission_id ORDER BY rp.permission_id), '[]') FROM membership.role_permission rp "
            "WHERE rp.role_id = %s AND rp.permission_id = ANY (membership.fn_effective_permissions(%s, %s, false))",
            (role, person, org))

    # -- metadata and the challenge -------------------------------------------------

    def test_protected_resource_metadata(self):
        for path in ("/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"):
            with self.subTest(path):
                response = request(self.base, "GET", path)
                self.assertEqual(response.status, 200, response.body)
                metadata = response.json()
                self.assertEqual(metadata["resource"], self.resource)
                self.assertEqual(metadata["authorization_servers"], [self.idp.issuer])
                self.assertEqual(metadata["bearer_methods_supported"], ["header"])
                self.assertIsInstance(metadata["scopes_supported"], list)
        self.assertEqual(request(self.world.base, "GET", "/.well-known/oauth-protected-resource").status, 404,
                         "without MCP_RESOURCE there is no resource metadata")

    def test_no_token_is_401_with_the_metadata_url(self):
        self.assert_challenge(self.call())

    def test_a_token_for_another_audience_is_401(self):
        for audience in (self.base + "/other", "https://elsewhere.example.com/mcp", [self.resource, "https://other.example"]):
            with self.subTest(audience=audience):
                self.assert_challenge(self.call(self.token(audience=audience)), "invalid_token")

    def test_the_web_login_clients_token_is_401(self):
        self.assert_challenge(self.call(self.token(client_id=CLIENT_ID)), "invalid_token")
        self.assert_challenge(self.call(self.token(client_id=DROP)), "invalid_token")

    def test_forged_expired_and_malformed_tokens_are_401(self):
        for label, token in [("forged", self.token(rogue=True)), ("expired", self.token(iat=1, exp=2)),
                             ("not a JWT", "not-a-jwt"), ("another issuer", self.token(issuer="http://127.0.0.1:9"))]:
            with self.subTest(label):
                self.assert_challenge(self.call(token), "invalid_token")

    def test_a_token_never_reaches_the_log(self):
        token = self.token(subject="log-probe")
        self.call(token)
        self.assertNotIn(token.split(".")[2], self.gateway.output())

    # -- who the client is ------------------------------------------------------------

    def test_a_person_not_signed_in_on_the_web_is_not_admitted(self):
        self.assertEqual(self.refusal(self.call(self.token(subject="never-" + uuid.uuid4().hex[:8])))["code"],
                         "membership.connection.not_admitted")

    def test_pair_approve_act_pause(self):
        subject = "pair-" + uuid.uuid4().hex[:8]
        browser = self.signed_in(subject)
        person, org = self.organization(subject)
        token = self.token(subject=subject)

        data = self.refusal(self.call(token))
        self.assertEqual(data["code"], "membership.connection.pending")
        code = data["pairingCode"]
        self.assertRegex(code, r"^[2-9A-HJKMNP-TV-Z]{6}$")
        self.assertEqual(self.refusal(self.call(token))["pairingCode"], code, "a repeated call shows the same code")

        approved = self.web(browser, "POST", "/connections/approve",
                            {"pairingCode": code, "organizationId": str(org), "roleId": "agent.reader"})
        self.assertEqual(approved.status, 201, approved.body)
        self.assertEqual(approved.json()["clientId"], "desktop-agent")

        response = self.call(token)
        self.assertEqual(response.status, 200, response.body)
        who = response.json()["result"]["structuredContent"]
        agent = self.world.scalar("SELECT agent_principal_object_id::text FROM membership.connection "
                                  "WHERE object_id = %s AND client_id = 'desktop-agent' AND token_hash IS NULL",
                                  (approved.json()["connectionId"],))
        self.assertEqual((who["principalId"], who["actualPrincipalId"], who["actorKind"], who["organizationId"]),
                         (str(person), agent, "agent", str(org)))
        self.assertEqual(sorted(who["permissions"]), self.expected_permissions(person, org, "agent.reader"))
        self.assertEqual(self.world.scalar(
            "SELECT context->>'actorKind' FROM api.mcp_exchange WHERE mcp_name = 'transport_whoami' "
            "AND context->>'actualPrincipalId' = %s ORDER BY sequence_number DESC LIMIT 1", (agent,)), "agent")

        paused = self.web(browser, "POST", f"/connections/{approved.json()['connectionId']}/pause")
        self.assertEqual(paused.status, 200, paused.body)
        data = self.refusal(self.call(token))
        self.assertEqual(data["code"], "membership.connection.paused")
        self.assertNotIn("pairingCode", data)

    # -- connection handles ---------------------------------------------------------

    def handle(self):
        """A person signed in on the web, a connection approved for them, and its handle."""
        subject = "handle-" + uuid.uuid4().hex[:8]
        browser = self.signed_in(subject)
        person, org = self.organization(subject)
        approved = self.web(browser, "POST", "/connections",
                            {"clientName": "Script", "organizationId": str(org), "roleId": "agent.contributor"})
        self.assertEqual(approved.status, 201, approved.body)
        return browser, person, approved.json()["connectionId"], approved.json()["connection"]

    def with_handle(self, handle):
        """Both ways to send a handle, on the OAuth gateway and on one without OAuth."""
        for label, headers in (("X-Connection", {"X-Connection": handle}),
                               ("Bearer", {"Authorization": "Bearer " + handle})):
            for gateway, base in (("oauth", self.base), ("plain", self.world.base)):
                yield f"{label} on {gateway}", headers, base

    def assert_handle_refused(self, response, base):
        self.assertEqual(response.status, 401, response.body)
        self.assertEqual(response.body, b"")
        challenge = response.header("WWW-Authenticate") or ""
        self.assertTrue(challenge.startswith("Bearer"), challenge)
        if base == self.base:
            self.assert_challenge(response, "invalid_token")
        else:
            self.assertNotIn("resource_metadata", challenge)

    def test_a_handle_acts_from_either_header(self):
        _, person, _, handle = self.handle()
        for label, headers, base in self.with_handle(handle):
            with self.subTest(label):
                response = self.call(headers=headers, base=base)
                self.assertEqual(response.status, 200, response.body)
                who = response.json()["result"]["structuredContent"]
                self.assertEqual((who["principalId"], who["actorKind"]), (str(person), "agent"))

    def test_an_unknown_revoked_or_expired_handle_is_401(self):
        browser, _, revoked_id, revoked = self.handle()
        self.assertEqual(self.web(browser, "DELETE", f"/connections/{revoked_id}").status, 200)
        _, _, expired_id, expired = self.handle()
        self.world.scalar("UPDATE membership.connection SET approved_at = now() - interval '2 days', "
                          "expires_at = now() - interval '1 second' WHERE object_id = %s RETURNING 1", (expired_id,))
        for kind, handle in (("unknown", "cn_" + uuid.uuid4().hex), ("revoked", revoked), ("expired", expired)):
            for label, headers, base in self.with_handle(handle):
                with self.subTest(f"{kind}, {label}"):
                    self.assert_handle_refused(self.call(headers=headers, base=base), base)

    def test_a_paused_handle_cannot_act_yet(self):
        browser, _, connection_id, handle = self.handle()
        self.assertEqual(self.web(browser, "POST", f"/connections/{connection_id}/pause").status, 200)
        for label, headers, base in self.with_handle(handle):
            with self.subTest(label):
                self.assertEqual(self.refusal(self.call(headers=headers, base=base))["code"],
                                 "membership.connection.paused")

    # -- the limits and the provider ----------------------------------------------------

    def test_a_flood_of_bad_tokens_is_429_before_any_key_fetch(self):
        gateway = self.world.start_gateway(**oauth_env(RATE_LIMIT_PER_IP="5"))
        resource = gateway.base + "/mcp"
        for _ in range(5):
            self.assertEqual(self.call(base=gateway.base).status, 401)
        fetched = self.idp.jwks_requests
        flood = [self.idp.access_token("flood", "desktop-agent", resource, kid="k-" + uuid.uuid4().hex) for _ in range(15)]
        for token in flood + [self.idp.access_token("flood", "desktop-agent", resource)]:
            response = self.call(token, base=gateway.base)
            self.assertEqual(response.status, 429, response.body)
            self.assertGreaterEqual(int(response.header("Retry-After")), 1)
            self.assertEqual(response.json()["code"], "pgmi.rate_limited")
        self.assertEqual(self.idp.jwks_requests, fetched,
                         "this gateway never checked a token, so any key fetch came from a refused request")

    def test_each_client_has_its_own_limit(self):
        gateway = self.world.start_gateway(**oauth_env(RATE_LIMIT_PER_CLIENT="2"))
        resource = gateway.base + "/mcp"
        token = self.idp.access_token("limited", "desktop-agent", resource)
        for _ in range(2):
            self.assertEqual(self.call(token, base=gateway.base).status, 200)
        response = self.call(token, base=gateway.base)
        self.assertEqual(response.status, 429, response.body)
        self.assertGreaterEqual(int(response.header("Retry-After")), 1)
        other = self.idp.access_token("limited", "cli-agent", resource)
        self.assertEqual(self.call(other, base=gateway.base).status, 200)

    def test_an_unreachable_provider_is_503(self):
        dead = f"http://127.0.0.1:{free_port()}"
        gateway = self.world.start_gateway(MCP_RESOURCE="{base}/mcp", MCP_AUTHORIZATION_SERVERS=dead)
        response = self.call(self.idp.access_token("x", "desktop-agent", gateway.base + "/mcp", issuer=dead),
                             base=gateway.base)
        self.assertEqual(response.status, 503, response.body)
        self.assertIsNotNone(response.header("Retry-After"))
        self.assertEqual(response.json()["error"]["data"]["code"], "pgmi.identity_provider_unavailable")


if __name__ == "__main__":
    unittest.main()
