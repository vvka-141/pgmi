"""pgmi's JSON-RPC endpoint, POST /rpc: the organization.transfer_ownership
action, signed in through POST /auth/dev-login. The database answers the
status and the envelope; the gateway copies them."""

import unittest
import uuid

from harness import Browser, world


class RpcTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.world = world()
        cls.base = cls.world.base

    def signed_in(self, email=None):
        browser = Browser()
        subject = "e2e-" + uuid.uuid4().hex[:12]
        response = browser.request("POST", self.base + "/auth/dev-login",
                                   body={"subject": subject, "email": email or subject + "@example.com",
                                         "displayName": subject})
        self.assertEqual(response.status, 204, response.body)
        return browser

    def post(self, browser, path, body, organization_session=None):
        headers = [("X-CSRF-Token", browser.cookies["__Host-csrf"]), ("Origin", self.base)]
        if organization_session:
            headers.append(("X-Organization-Session", organization_session))
        return browser.request("POST", self.base + path, headers, body)

    def transfer(self, browser, session, new_owner):
        return self.post(browser, "/rpc", {"jsonrpc": "2.0", "id": "t1", "method": "organization.transfer_ownership",
                                           "params": {"newOwnerId": new_owner}}, session)

    def test_the_owner_transfers_ownership_and_an_outsider_is_refused(self):
        owner = self.signed_in()
        slug = "e2e-" + uuid.uuid4().hex[:12]
        created = self.post(owner, "/api/organizations", {"name": slug, "slug": slug})
        self.assertEqual(created.status, 201, created.body)
        opened = self.post(owner, "/api/organization-sessions", {"organizationId": created.json()["id"]})
        self.assertEqual(opened.status, 200, opened.body)
        session = opened.json()["organizationSession"]

        refused = self.transfer(owner, session, str(uuid.uuid4()))
        self.assertEqual(refused.status, 422, refused.body)
        self.assertEqual(refused.json()["id"], "t1")
        self.assertEqual(refused.json()["error"]["code"], -32011)
        self.assertEqual(refused.json()["error"]["data"]["invalid-params"], [{"name": "newOwnerId", "reason": "not_found"}])

        email = "e2e-" + uuid.uuid4().hex[:12] + "@example.com"
        invited = self.post(owner, "/api/invitations", {"email": email, "roleId": "org.member"}, session)
        self.assertEqual(invited.status, 201, invited.body)
        self.signed_in(email)
        members = owner.request("GET", self.base + "/api/members", [("X-Organization-Session", session)])
        self.assertEqual(members.status, 200, members.body)
        new_owner = next(m["principalId"] for m in members.json()["items"] if m.get("email") == email)

        moved = self.transfer(owner, session, new_owner)
        self.assertEqual(moved.status, 200, moved.body)
        self.assertEqual(moved.json()["result"]["ownerId"], new_owner)
        again = self.transfer(owner, session, new_owner)
        self.assertEqual(again.status, 409, again.body)
        self.assertEqual(again.json()["error"]["data"]["code"], "membership.ownership.not_owner")

    def test_an_unknown_method_is_404(self):
        browser = self.signed_in()
        unknown = self.post(browser, "/rpc", {"jsonrpc": "2.0", "id": 1, "method": "no.such.method"})
        self.assertEqual(unknown.status, 404, unknown.body)
        self.assertEqual(unknown.json()["error"]["code"], -32601)

    def test_a_malformed_envelope_is_400(self):
        browser = self.signed_in()
        for body in ([{"jsonrpc": "2.0", "id": 1, "method": "organization.transfer_ownership"}],
                     {"jsonrpc": "2.0", "id": 1}):
            refused = self.post(browser, "/rpc", body)
            self.assertEqual(refused.status, 400, refused.body)
            self.assertEqual(refused.json()["error"]["code"], -32600, refused.body)
        nul = self.post(browser, "/rpc", {"jsonrpc": "2.0", "id": 1, "method": "organization.\u0000transfer"})
        self.assertEqual(nul.status, 400, nul.body)
        self.assertEqual(nul.json()["error"]["code"], -32700, nul.body)
        empty = browser.request("POST", self.base + "/rpc", [("X-CSRF-Token", browser.cookies["__Host-csrf"]),
                                                            ("Origin", self.base), ("Content-Type", "application/json")])
        self.assertEqual(empty.status, 400, empty.body)
        self.assertEqual(empty.json()["error"]["code"], -32700, empty.body)


if __name__ == "__main__":
    unittest.main()
