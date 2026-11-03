"""A minimal OIDC provider for the login suite: discovery, JWKS, authorize
and token (PKCE, client_secret_basic), with knobs for the refusal cases."""

import base64
import hashlib
import hmac
import json
import threading
import time
import urllib.parse
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import jwt
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa

CLIENT_ID = "pgmi-gateway"
CLIENT_SECRET = "client-secret-for-tests"
DROP = object()


def b64url(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


def new_key():
    return rsa.generate_private_key(public_exponent=65537, key_size=2048)


class MockProvider:
    """next_login holds the claims of the next sign-in, plus these knobs:
    _rogue_key (sign with a key not in the JWKS), _alg "none" (unsigned) or
    "HS256-public" (HMAC keyed with the public key's PEM). discovery_issuer
    makes discovery name another issuer."""

    def __init__(self, port, discovery_issuer=None):
        self.port = port
        self.issuer = f"http://127.0.0.1:{port}"
        self.discovery_issuer = discovery_issuer or self.issuer
        self.key = new_key()
        self.kid = "k1"
        self.rogue_key = new_key()
        self.next_login = {}
        self.codes = {}
        self.authorize_requests = []
        self.token_requests = []
        self.jwks_requests = 0
        self.jwks_down = False
        self.server = None
        self.start()

    def start(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", self.port), self._handler())
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def stop(self):
        self.server.shutdown()
        self.server.server_close()

    def rotate(self):
        self.key = new_key()
        self.kid = "k" + uuid.uuid4().hex[:8]

    def _handler(self):
        provider = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def reply(self, status, body=None, headers=()):
                data = json.dumps(body).encode() if body is not None else b""
                self.send_response(status)
                for name, value in headers:
                    self.send_header(name, value)
                if body is not None:
                    self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self):
                parts = urllib.parse.urlsplit(self.path)
                if parts.path == "/.well-known/openid-configuration":
                    self.reply(200, {
                        "issuer": provider.discovery_issuer,
                        "authorization_endpoint": provider.issuer + "/authorize",
                        "token_endpoint": provider.issuer + "/token",
                        "jwks_uri": provider.issuer + "/jwks",
                        "id_token_signing_alg_values_supported": ["RS256"],
                    })
                elif parts.path == "/jwks":
                    provider.jwks_requests += 1
                    if provider.jwks_down:
                        self.reply(503, {})
                        return
                    jwk = json.loads(jwt.algorithms.RSAAlgorithm.to_jwk(provider.key.public_key()))
                    self.reply(200, {"keys": [{**jwk, "kid": provider.kid, "alg": "RS256", "use": "sig"}]})
                elif parts.path == "/authorize":
                    q = dict(urllib.parse.parse_qsl(parts.query))
                    provider.authorize_requests.append(q)
                    if q.get("client_id") != CLIENT_ID or q.get("response_type") != "code" \
                            or q.get("code_challenge_method") != "S256" or not q.get("code_challenge"):
                        self.reply(400, {"error": "invalid_request"})
                        return
                    code = uuid.uuid4().hex
                    provider.codes[code] = (q, dict(provider.next_login))
                    target = q["redirect_uri"] + "?" + urllib.parse.urlencode({"code": code, "state": q["state"]})
                    self.reply(302, headers=[("Location", target)])
                else:
                    self.reply(404, {})

            def do_POST(self):
                form = dict(urllib.parse.parse_qsl(self.rfile.read(int(self.headers["Content-Length"])).decode()))
                provider.token_requests.append(form)
                expected = "Basic " + base64.b64encode(f"{CLIENT_ID}:{CLIENT_SECRET}".encode()).decode()
                if self.headers.get("Authorization") != expected:
                    self.reply(401, {"error": "invalid_client"})
                    return
                entry = provider.codes.pop(form.get("code"), None)
                if entry is None:
                    self.reply(400, {"error": "invalid_grant"})
                    return
                q, login = entry
                verifier = form.get("code_verifier", "")
                if b64url(hashlib.sha256(verifier.encode()).digest()) != q["code_challenge"] \
                        or form.get("redirect_uri") != q["redirect_uri"]:
                    self.reply(400, {"error": "invalid_grant"})
                    return
                self.reply(200, {"access_token": "unused", "token_type": "Bearer",
                                 "id_token": provider.id_token(q["nonce"], login)})

        return Handler

    def id_token(self, nonce, login):
        now = int(time.time())
        claims = {"iss": self.issuer, "aud": CLIENT_ID, "iat": now, "exp": now + 300, "nonce": nonce,
                  "email_verified": True, "amr": ["pwd"]}
        rogue = login.pop("_rogue_key", False)
        alg = login.pop("_alg", "RS256")
        claims.update(login)
        claims = {k: v for k, v in claims.items() if v is not DROP}
        if alg == "none":
            return self._compact({"alg": "none", "typ": "JWT", "kid": self.kid}, claims, lambda _: b"")
        if alg == "HS256-public":
            pem = self.key.public_key().public_bytes(serialization.Encoding.PEM,
                                                     serialization.PublicFormat.SubjectPublicKeyInfo)
            return self._compact({"alg": "HS256", "typ": "JWT", "kid": self.kid}, claims,
                                 lambda signing_input: hmac.new(pem, signing_input, hashlib.sha256).digest())
        return jwt.encode(claims, self.rogue_key if rogue else self.key, algorithm="RS256", headers={"kid": self.kid})

    def access_token(self, subject, client_id, audience, issuer=None, kid=None, rogue=False, **claims):
        """An access token as the provider issues one to an MCP client for
        the resource `audience` (RFC 8707). Pass DROP to leave a claim out;
        kid names a key the JWKS does not have, rogue signs with one."""
        now = int(time.time())
        body = {"iss": issuer or self.issuer, "sub": subject, "aud": audience, "client_id": client_id,
                "iat": now, "exp": now + 300, "scope": "openid"}
        body.update(claims)
        body = {k: v for k, v in body.items() if v is not DROP}
        return jwt.encode(body, self.rogue_key if rogue else self.key, algorithm="RS256",
                          headers={"kid": kid or self.kid})

    @staticmethod
    def _compact(header, claims, sign):
        signing_input = (b64url(json.dumps(header).encode()) + "." + b64url(json.dumps(claims).encode())).encode()
        return signing_input.decode() + "." + b64url(sign(signing_input))
