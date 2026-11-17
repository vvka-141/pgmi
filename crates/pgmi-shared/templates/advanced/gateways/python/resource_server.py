"""
The OAuth 2.1 resource server in front of /mcp, and the rate limits.

Demo code from pgmi. Do not copy this file into the project as is. Read it,
then write your own gateway that fits this project. Namespaces, modules and
package names are examples: rename them to match the project and add its
specifics freely.

An MCP client signs in at the identity provider and sends
`Authorization: Bearer <access token>` on every request. This module checks
the token here, against the provider's published keys, and hands the
database only what it proved: the issuer, the subject and the client id
(../PROTOCOL.md, "MCP clients with OAuth"). The token itself is never
forwarded and never logged. The database decides who that is and whether
the client may act yet.

    GET /.well-known/oauth-protected-resource[/mcp]   protected-resource
        metadata (RFC 9728): where to get a token for this server
    POST /mcp without a valid token                    401 with
        WWW-Authenticate: Bearer resource_metadata="..."

Environment:
    MCP_RESOURCE               the canonical URI of /mcp, as clients reach it
                               (https://mcp.example.com/mcp; http only on a
                               loopback host). Set, /mcp takes a bearer token
                               or a connection handle and nothing else.
    MCP_AUTHORIZATION_SERVERS  comma-separated issuer URLs whose tokens are
                               taken; each must also be a web sign-in issuer
                               (the oidc_issuers deploy parameter)
    MCP_SCOPES                 space-separated scopes_supported (openid)
    RATE_LIMIT_PER_IP          /mcp, /auth/login and /auth/callback requests
                               a minute per client address, one limit,
                               counted before anything else (600; 0: off)
    RATE_LIMIT_PER_CLIENT      /mcp requests a minute per (issuer, subject,
                               client id), after the token check (300; 0: off)
"""

import collections
import ipaddress
import json
import math
import os
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

import jwt

# Standard OAuth/OIDC token verification, not pgmi-specific: replace it with
# your own stack's verifier. The same three algorithms in every gateway.
TOKEN_ALGORITHMS = ["RS256", "PS256", "ES256"]
CLOCK_SKEW_SECONDS = 60
MAX_TOKEN_LENGTH = 8192
PROVIDER_TIMEOUT_SECONDS = 5
# Unlike the login transport, these tokens come from any client: a token
# naming an unknown key refetches the key set at most once per this many
# seconds per issuer, so a flood of forged key ids cannot flood the provider.
# A rotated key is picked up within this long.
UNKNOWN_KEY_COOLDOWN_SECONDS = 10


def verification_key(jwk, alg):
    """The public key that verifies a token signed with alg, or None. A JWKS
    RSA key often has no "alg", and PyJWT then binds it to RS256 alone,
    refusing a PS256 token; an RSA key verifies either. An EC key verifies
    ES256 only on P-256."""
    if jwk.key_type == "RSA" and alg in ("RS256", "PS256"):
        return jwk.key
    if jwk.key_type == "EC" and alg == "ES256" and jwk.algorithm_name == "ES256":
        return jwk.key
    return None


def is_numeric_date(value):
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


class TokenRejected(Exception):
    """401. error is "invalid_token", or None when no token was sent: RFC
    6750 §3.1 sends no error code to a request without credentials."""

    def __init__(self, error, reason):
        super().__init__(reason)
        self.error = error


class ProviderUnavailable(Exception):
    """503: the identity provider's discovery or keys could not be read."""


class Identity:
    __slots__ = ("issuer", "subject", "client_id")

    def __init__(self, issuer, subject, client_id):
        self.issuer, self.subject, self.client_id = issuer, subject, client_id

    def context(self):
        """The MCP context api.mcp_handle_request resolves (and nothing else)."""
        return {"client": {"issuer": self.issuer, "subject": self.subject, "clientId": self.client_id}}


def _is_loopback(host):
    if host == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def _checked_url(value, name):
    parts = urllib.parse.urlsplit(value)
    if parts.scheme != "https" and not (parts.scheme == "http" and _is_loopback(parts.hostname or "")):
        raise ValueError(f"{name} must be https (http only on a loopback host): {value}")
    if parts.fragment or parts.query:
        raise ValueError(f"{name} must have no query and no fragment: {value}")
    return value


def canonical(uri):
    """Scheme and host lower-cased, the rest as written: the MCP spec asks
    servers to accept an upper-case scheme or host in an audience."""
    parts = urllib.parse.urlsplit(uri)
    return urllib.parse.urlunsplit(parts._replace(scheme=parts.scheme.lower(), netloc=parts.netloc.lower()))


class _Issuer:
    """One authorization server: discovery once, then its keys, cached."""

    def __init__(self, issuer):
        self.issuer = issuer
        self._keys = None
        self._lock = threading.Lock()

    def keys(self):
        with self._lock:
            if self._keys is None:
                url = self.issuer.rstrip("/") + "/.well-known/openid-configuration"
                try:
                    with urllib.request.urlopen(url, timeout=PROVIDER_TIMEOUT_SECONDS) as response:
                        metadata = json.load(response)
                except (urllib.error.URLError, TimeoutError, ValueError) as e:
                    raise ProviderUnavailable("discovery failed") from e
                if not isinstance(metadata, dict) or metadata.get("issuer") != self.issuer \
                        or not isinstance(metadata.get("jwks_uri"), str):
                    raise ProviderUnavailable("discovery names another issuer or no jwks_uri")
                self._keys = jwt.PyJWKClient(metadata["jwks_uri"], cache_keys=False, lifespan=300,
                                             timeout=PROVIDER_TIMEOUT_SECONDS,
                                             cooldown_duration=UNKNOWN_KEY_COOLDOWN_SECONDS)
            return self._keys


class ResourceServer:
    def __init__(self, resource, authorization_servers, scopes, login_client_id=None):
        self.resource = _checked_url(resource, "MCP_RESOURCE")
        if not authorization_servers:
            raise ValueError("MCP_RESOURCE is set but MCP_AUTHORIZATION_SERVERS is not")
        self._issuers = {s: _Issuer(_checked_url(s, "MCP_AUTHORIZATION_SERVERS")) for s in authorization_servers}
        self.scopes = scopes
        # ADAPT: every client id that is not an MCP client. A token issued to
        # the web sign-in is a person's browser session, never an agent.
        self._refused_clients = {login_client_id} if login_client_id else set()
        parts = urllib.parse.urlsplit(self.resource)
        origin = f"{parts.scheme}://{parts.netloc}"
        self.metadata_paths = {"/.well-known/oauth-protected-resource",
                               "/.well-known/oauth-protected-resource" + parts.path.rstrip("/")}
        self.metadata_url = origin + "/.well-known/oauth-protected-resource" + parts.path.rstrip("/")

    @classmethod
    def from_environment(cls, login_client_id=None):
        """None when MCP_RESOURCE is unset: /mcp then takes connection handles only."""
        resource = os.environ.get("MCP_RESOURCE")
        if not resource:
            return None
        servers = [s.strip() for s in os.environ.get("MCP_AUTHORIZATION_SERVERS", "").split(",") if s.strip()]
        scopes = os.environ.get("MCP_SCOPES", "openid").split()
        return cls(resource, servers, scopes, login_client_id)

    def metadata(self):
        """RFC 9728 §2. The MCP spec requires authorization_servers."""
        return {
            "resource": self.resource,
            "authorization_servers": list(self._issuers),
            "bearer_methods_supported": ["header"],
            "scopes_supported": self.scopes,
        }

    def challenge(self, error=None):
        value = f'Bearer resource_metadata="{self.metadata_url}"'
        if self.scopes:
            value += f', scope="{" ".join(self.scopes)}"'
        if error:
            value += f', error="{error}"'
        return value

    def verify(self, authorization):
        """The identity a valid access token proves, or TokenRejected /
        ProviderUnavailable. Cheap checks first: a token that is not a JWT,
        names an issuer not configured or another audience is refused before
        any key is fetched."""
        if authorization is None:
            raise TokenRejected(None, "no token")
        scheme, _, token = authorization.partition(" ")
        token = token.strip()
        if scheme.lower() != "bearer" or not token or len(token) > MAX_TOKEN_LENGTH:
            raise TokenRejected("invalid_token", "not a bearer token")
        try:
            header = jwt.get_unverified_header(token)
            unverified = jwt.decode(token, options={"verify_signature": False})
        except jwt.PyJWTError as e:
            raise TokenRejected("invalid_token", "not a JWT") from e
        issuer = self._issuers.get(unverified.get("iss")) if isinstance(unverified.get("iss"), str) else None
        if issuer is None:
            raise TokenRejected("invalid_token", "issuer not accepted")
        if header.get("alg") not in TOKEN_ALGORITHMS or not isinstance(header.get("kid"), str):
            raise TokenRejected("invalid_token", "algorithm or key id not accepted")
        if not self._is_audience(unverified.get("aud")):
            raise TokenRejected("invalid_token", "audience is not this resource")

        try:
            key = issuer.keys().get_signing_key(header["kid"])
        except jwt.PyJWKClientConnectionError as e:
            raise ProviderUnavailable("key set unavailable") from e
        except jwt.PyJWKClientError as e:
            raise TokenRejected("invalid_token", "unknown key") from e
        key = verification_key(key, header["alg"])
        if key is None:
            raise TokenRejected("invalid_token", "algorithm does not match the key")
        try:
            claims = jwt.decode(token, key, algorithms=[header["alg"]], issuer=issuer.issuer,
                                leeway=CLOCK_SKEW_SECONDS,
                                options={"verify_aud": False, "require": ["iss", "sub", "aud", "exp", "iat"]})
        except jwt.PyJWTError as e:
            raise TokenRejected("invalid_token", type(e).__name__) from e
        if any(not is_numeric_date(claims[name]) for name in ("exp", "iat", "nbf") if name in claims):
            raise TokenRejected("invalid_token", "a time claim is not a number")
        # RFC 8707: the token was issued for this resource and only this one.
        # A token that also names another audience could be replayed here by
        # that other server.
        if not self._is_audience(claims["aud"]):
            raise TokenRejected("invalid_token", "audience is not this resource")
        client_id = claims["client_id"] if "client_id" in claims else claims.get("azp")
        if not isinstance(client_id, str) or not client_id or client_id in self._refused_clients:
            raise TokenRejected("invalid_token", "no MCP client id")
        if not isinstance(claims["sub"], str) or not claims["sub"]:
            raise TokenRejected("invalid_token", "sub is not a string")
        return Identity(issuer.issuer, claims["sub"], client_id)

    def _is_audience(self, audience):
        if isinstance(audience, list) and len(audience) == 1:
            audience = audience[0]
        return isinstance(audience, str) and canonical(audience) == canonical(self.resource)


class RateLimiter:
    """A token bucket per key, refilled continuously to per_minute a minute.

    Standard web plumbing, not pgmi-specific: replace it with your framework's own.

    Per process: N gateway processes allow N times the limit. Thread-safe
    behind one lock; memory is bounded by max_keys, the least recently seen
    key going first (a key that comes back starts full, which only an
    attacker holding more than max_keys addresses gains from).

    ADAPT: behind a reverse proxy the client address is the proxy's; key
    on the address your proxy forwards, from a header only it can set, or
    limit at the proxy and set RATE_LIMIT_PER_IP=0 here."""

    def __init__(self, per_minute, max_keys=10000, clock=time.monotonic):
        self.per_minute = per_minute
        self._rate = per_minute / 60.0
        self._max_keys = max_keys
        self._clock = clock
        self._buckets = collections.OrderedDict()
        self._lock = threading.Lock()

    def allow(self, key):
        """(True, 0), or (False, seconds until the next request is allowed)."""
        if self.per_minute <= 0:
            return True, 0
        now = self._clock()
        with self._lock:
            tokens, seen = self._buckets.pop(key, (float(self.per_minute), now))
            tokens = min(float(self.per_minute), tokens + (now - seen) * self._rate)
            allowed = tokens >= 1.0
            if allowed:
                tokens -= 1.0
            self._buckets[key] = (tokens, now)
            while len(self._buckets) > self._max_keys:
                self._buckets.popitem(last=False)
        return (True, 0) if allowed else (False, max(1, math.ceil((1.0 - tokens) / self._rate)))

    @classmethod
    def from_environment(cls, name, default):
        return cls(int(os.environ.get(name, str(default))))
