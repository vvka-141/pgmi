"""
Reference login transport: OIDC sign-in in front of the database.

Demo code from pgmi. Do not copy this file into the project as is. Read it,
then write your own gateway that fits this project. Namespaces, modules and
package names are examples: rename them to match the project and add its
specifics freely.

Implements the login transport contract (docs/advanced/LOGIN-TRANSPORT.md) for
gateway.py, which imports it. The gateway owns HTTP and the database
connection; this module owns the sign-in rules:

    GET  /auth/login?returnTo=/path   start the authorization code flow (PKCE)
    GET  /auth/callback               check state, exchange the code, verify the
                                      ID token, call api.open_login_session
    POST /auth/logout                 api.close_login_session, clear the cookies
    POST /auth/dev-login              DEV_LOGIN=1 only: api.open_dev_login_session

Only the opaque login-session handle reaches the browser, in an HttpOnly cookie.
The database decides who the caller is; nothing here names a person.

Environment:
    OIDC_ISSUER         issuer URL, exactly as listed in the oidc_issuers deploy
                        parameter (https, or http on a loopback host)
    OIDC_CLIENT_ID      the client registered with the issuer
    OIDC_CLIENT_SECRET  confidential clients only (client_secret_basic)
    OIDC_REDIRECT_URI   this gateway's /auth/callback, as registered
    OIDC_SCOPES         default "openid email profile"
    LOGIN_CSRF_SECRET   key for CSRF tokens; set it when more than one gateway
                        process serves the same users (default: random per
                        process)
    DEV_LOGIN           "1": development sign-in without a provider
"""

import base64
import collections
import hashlib
import hmac
import ipaddress
import json
import os
import secrets
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.cookies import CookieError, SimpleCookie

import jwt

from resource_server import is_numeric_date, verification_key

# ADAPT: cookie names and lifetimes.
SESSION_COOKIE = "__Host-session"
CSRF_COOKIE = "__Host-csrf"
LOGIN_COOKIE = "__Host-login"
CSRF_HEADER = "x-csrf-token"

SESSION_MAX_AGE = 7 * 24 * 3600
LOGIN_MAX_AGE = 600
# Standard OAuth/OIDC token verification, not pgmi-specific: replace it with
# your own stack's verifier. The same three algorithms in every gateway.
ID_TOKEN_ALGORITHMS = ["RS256", "PS256", "ES256"]
# ADAPT: the development issuer seeded by membership/dev/01-issuers.sql.
DEV_ISSUER = "dev"
CLOCK_SKEW_SECONDS = 60

SAFE_METHODS = frozenset({"GET", "HEAD", "OPTIONS"})

# Session and connection handles the transport did not issue, the cookie that
# carries the one it did, and hop-by-hop headers.
NOT_FORWARDED = frozenset({
    "x-login-session", "x-connection", "cookie", CSRF_HEADER,
    "connection", "keep-alive", "proxy-connection", "te", "trailer",
    "transfer-encoding", "upgrade",
})


SINGLE_VALUED = frozenset({
    "authorization", "content-length", "content-type", "host", "origin",
    CSRF_HEADER, "x-organization-session", "x-pgmi-transaction-isolation",
})


class HeaderCollision(ValueError):
    pass


class LoginFailed(Exception):
    def __init__(self, status, code, detail):
        super().__init__(detail)
        self.status = status
        self.code = code
        self.detail = detail


def problem(status, title, code, detail=None):
    body = {"type": "about:blank", "title": title, "status": status, "code": code}
    if detail:
        body["detail"] = detail
    return body


def _b64url(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


def pkce_pair():
    verifier = secrets.token_urlsafe(48)
    return verifier, _b64url(hashlib.sha256(verifier.encode("ascii")).digest())


# Standard web plumbing, not pgmi-specific: replace it with your framework's own.
# Session, CSRF and sign-in cookies: read, set and cleared here only.
def read_cookies(headers):
    jar = SimpleCookie()
    for raw in headers.get_all("Cookie") or []:
        try:
            jar.load(raw)
        except CookieError:
            continue
    return {name: morsel.value for name, morsel in jar.items()}


def set_cookie(name, value, max_age, http_only=True):
    # SameSite=Strict keeps the session off every cross-site request, a link
    # included. The sign-in cookie is Lax: the provider's redirect back to
    # /auth/callback is a cross-site navigation that must carry it.
    same_site = "Lax" if name == LOGIN_COOKIE else "Strict"
    cookie = f"{name}={value}; Path=/; Max-Age={max_age}; Secure; SameSite={same_site}"
    return cookie + "; HttpOnly" if http_only else cookie


def clear_cookie(name):
    return set_cookie(name, "", 0, http_only=name != CSRF_COOKIE)


def incoming_headers(items):
    """Every header the client sent: lower-cased, each name once, a repeated
    name joined (RFC 9110 §5.3). Interceptors see this, credentials included."""
    out = {}
    for name, value in items:
        key = name.lower()
        if key in out:
            out[key] += ("; " if key == "cookie" else ", ") + value
        else:
            out[key] = value
    return out


def forwarded_headers(items, login_session):
    """The headers the database sees: the incoming headers without the handles
    and cookie the client sent. A repeat of a SINGLE_VALUED name, in any letter
    case, is refused rather than guessed at. The login session comes from the
    transport's cookie only.

    pgmi-specific: the header rules of what may be forwarded; see
    ../PROTOCOL.md "Identity".
    """
    items = list(items)
    seen = set()
    for name, _ in items:
        key = name.lower()
        if key in seen and key in SINGLE_VALUED:
            raise HeaderCollision(key)
        seen.add(key)
    out = incoming_headers(items)
    for key in NOT_FORWARDED:
        out.pop(key, None)
    if login_session:
        out["x-login-session"] = login_session
    return out


# ADAPT: an application points this at its own start page. The API explorer
# shows the signed-in routes, so it confirms the sign-in.
DEFAULT_RETURN_TO = "/api/docs"


def safe_return_to(value):
    if (not value or not value.startswith("/") or value.startswith("//") or "\\" in value
            or any(not "!" <= c <= "~" for c in value)):
        return DEFAULT_RETURN_TO
    return value


def _is_loopback(host):
    if host == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def login_arguments(claims):
    """api.open_login_session arguments from verified ID token claims.

    email_verified and amr go through as the provider sent them. A value of the
    wrong JSON type becomes NULL, which the database reads as unverified and
    as no second factor.
    """
    verified = claims.get("email_verified")
    amr = claims.get("amr")
    return (
        claims["iss"],
        claims["sub"],
        claims.get("email"),
        claims.get("name"),
        verified if isinstance(verified, bool) else None,
        amr if isinstance(amr, list) and all(isinstance(a, str) for a in amr) else None,
    )


class PendingLogins:
    """Sign-ins between /auth/login and /auth/callback, each usable once, for
    at most ttl seconds, at most limit of them (the oldest goes first).

    Insertion order is time order, so expiry and eviction take from the
    front and never scan. An OrderedDict, not a dict: a dict skips the slots
    of entries deleted from its front on every iteration. The per-IP limit
    on /auth keeps one address from filling it.

    ADAPT: a single-process demo. Behind a load balancer, pin /auth/* to one
    process, keep this in a shared store, or drop the store: carry the
    pending sign-in in a signed, short-lived __Host-login cookie instead."""

    def __init__(self, ttl=LOGIN_MAX_AGE, limit=10000, clock=time.monotonic):
        self._ttl = ttl
        self._limit = limit
        self._clock = clock
        self._items = collections.OrderedDict()
        self._lock = threading.Lock()

    def put(self, attempt):
        key = secrets.token_urlsafe(32)
        now = self._clock()
        with self._lock:
            while self._items:
                at, _ = next(iter(self._items.values()))
                if now - at <= self._ttl and len(self._items) < self._limit:
                    break
                self._items.popitem(last=False)
            self._items[key] = (now, attempt)
        return key

    def take(self, key):
        with self._lock:
            entry = self._items.pop(key, None) if key else None
        if entry is None or self._clock() - entry[0] > self._ttl:
            return None
        return entry[1]


class Provider:
    """One OIDC issuer: discovery, the code exchange and ID token checks."""

    def __init__(self, issuer, client_id, client_secret, redirect_uri, scopes):
        parsed = urllib.parse.urlsplit(issuer)
        if parsed.scheme != "https" and not (parsed.scheme == "http" and _is_loopback(parsed.hostname or "")):
            raise ValueError("OIDC_ISSUER must be https (http only on a loopback host)")
        self.issuer = issuer
        self.client_id = client_id
        self.client_secret = client_secret
        self.redirect_uri = redirect_uri
        self.scopes = scopes
        self._metadata = None
        self._jwks = None
        self._lock = threading.Lock()

    def _get_json(self, url, data=None, headers=None):
        request = urllib.request.Request(url, data=data, headers={"Accept": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return json.load(response)
        except urllib.error.HTTPError as e:
            raise LoginFailed(401, "pgmi.login.provider_refused", f"the provider answered {e.code}") from e
        except (urllib.error.URLError, TimeoutError, ValueError) as e:
            raise LoginFailed(502, "pgmi.login.provider_unavailable", "the provider could not be reached") from e

    def metadata(self):
        with self._lock:
            if self._metadata is None:
                url = self.issuer.rstrip("/") + "/.well-known/openid-configuration"
                metadata = self._get_json(url)
                if metadata.get("issuer") != self.issuer:
                    raise LoginFailed(502, "pgmi.login.provider_unavailable", "discovery names another issuer")
                # An unknown kid refetches the key set at once: after a key
                # rotation the next sign-in must not wait out PyJWT's default
                # 30 s cooldown. Safe here, because ID tokens reach this
                # gateway only from the token endpoint, never from a client.
                self._jwks = jwt.PyJWKClient(metadata["jwks_uri"], cache_keys=True, lifespan=300,
                                             cooldown_duration=0)
                self._metadata = metadata
            return self._metadata

    def authorization_url(self, state, nonce, challenge):
        query = urllib.parse.urlencode({
            "response_type": "code",
            "client_id": self.client_id,
            "redirect_uri": self.redirect_uri,
            "scope": self.scopes,
            "state": state,
            "nonce": nonce,
            "code_challenge": challenge,
            "code_challenge_method": "S256",
        })
        return self.metadata()["authorization_endpoint"] + "?" + query

    def exchange(self, code, verifier):
        form = {
            "grant_type": "authorization_code",
            "code": code,
            "redirect_uri": self.redirect_uri,
            "code_verifier": verifier,
        }
        headers = {"Content-Type": "application/x-www-form-urlencoded"}
        if self.client_secret:
            pair = urllib.parse.quote(self.client_id, safe="") + ":" + urllib.parse.quote(self.client_secret, safe="")
            headers["Authorization"] = "Basic " + base64.b64encode(pair.encode()).decode("ascii")
        else:
            form["client_id"] = self.client_id
        tokens = self._get_json(self.metadata()["token_endpoint"],
                                urllib.parse.urlencode(form).encode("ascii"), headers)
        id_token = tokens.get("id_token") if isinstance(tokens, dict) else None
        if not isinstance(id_token, str):
            raise LoginFailed(401, "pgmi.login.token_invalid", "the provider returned no ID token")
        return id_token

    def verify(self, id_token, nonce):
        self.metadata()
        try:
            alg = jwt.get_unverified_header(id_token).get("alg")
            if alg not in ID_TOKEN_ALGORITHMS:
                raise LoginFailed(401, "pgmi.login.token_invalid", "algorithm not accepted")
            key = verification_key(self._jwks.get_signing_key_from_jwt(id_token), alg)
            if key is None:
                raise LoginFailed(401, "pgmi.login.token_invalid", "algorithm does not match the key")
            claims = jwt.decode(
                id_token, key, algorithms=[alg],
                audience=self.client_id, issuer=self.issuer, leeway=CLOCK_SKEW_SECONDS,
                options={"require": ["iss", "sub", "aud", "exp", "iat", "nonce"]})
        except jwt.PyJWKClientConnectionError as e:
            raise LoginFailed(502, "pgmi.login.provider_unavailable", "the provider's keys could not be fetched") from e
        except jwt.PyJWTError as e:
            raise LoginFailed(401, "pgmi.login.token_invalid", str(e)) from e
        if any(not is_numeric_date(claims[name]) for name in ("exp", "iat", "nbf") if name in claims):
            raise LoginFailed(401, "pgmi.login.token_invalid", "a time claim is not a number")
        audience = claims["aud"]
        if isinstance(audience, list) and len(audience) > 1 and claims.get("azp") != self.client_id:
            raise LoginFailed(401, "pgmi.login.token_invalid", "azp does not name this client")
        if not isinstance(claims["nonce"], str) or not hmac.compare_digest(claims["nonce"], nonce):
            raise LoginFailed(401, "pgmi.login.token_invalid", "nonce mismatch")
        if not isinstance(claims["sub"], str):
            raise LoginFailed(401, "pgmi.login.token_invalid", "sub is not a string")
        return claims


class LoginTransport:
    def __init__(self, provider, csrf_secret, dev_login=False):
        self.provider = provider
        self.dev_login = dev_login
        self._csrf_secret = csrf_secret
        self._pending = PendingLogins()

    @classmethod
    def from_environment(cls):
        """None when neither OIDC_ISSUER nor DEV_LOGIN is set: /api is then
        anonymous. With DEV_LOGIN alone there is no provider."""
        issuer = os.environ.get("OIDC_ISSUER")
        dev_login = os.environ.get("DEV_LOGIN") == "1"
        if not issuer and not dev_login:
            return None
        provider = None
        if issuer:
            missing = [n for n in ("OIDC_CLIENT_ID", "OIDC_REDIRECT_URI") if not os.environ.get(n)]
            if missing:
                raise ValueError("OIDC_ISSUER is set but " + ", ".join(missing) + " is not")
            provider = Provider(issuer, os.environ["OIDC_CLIENT_ID"], os.environ.get("OIDC_CLIENT_SECRET"),
                                os.environ["OIDC_REDIRECT_URI"], os.environ.get("OIDC_SCOPES", "openid email profile"))
        secret = os.environ.get("LOGIN_CSRF_SECRET")
        return cls(provider, secret.encode() if secret else secrets.token_bytes(32), dev_login)

    # Standard web plumbing, not pgmi-specific: replace it with your framework's own.
    def csrf_token(self, login_session):
        return _b64url(hmac.new(self._csrf_secret, b"csrf\0" + login_session.encode(), hashlib.sha256).digest())

    def csrf_ok(self, method, login_session, presented):
        """A state-changing request riding on the session cookie must carry the
        session's CSRF token, which a cross-site page cannot read."""
        if method in SAFE_METHODS or not login_session:
            return True
        return bool(presented) and hmac.compare_digest(self.csrf_token(login_session), presented)

    def begin(self, return_to, cookies):
        """Start a sign-in. The handle the browser holds now is kept with it:
        the callback is the provider's cross-site redirect, which does not
        carry the SameSite=Strict session cookie."""
        verifier, challenge = pkce_pair()
        state = secrets.token_urlsafe(32)
        nonce = secrets.token_urlsafe(32)
        location = self.provider.authorization_url(state, nonce, challenge)
        key = self._pending.put({"state": state, "nonce": nonce, "verifier": verifier,
                                 "return_to": safe_return_to(return_to),
                                 "previous": cookies.get(SESSION_COOKIE)})
        return location, [set_cookie(LOGIN_COOKIE, key, LOGIN_MAX_AGE)]

    def complete(self, cookies, query, connect):
        """Finish a sign-in. Returns (location, cookies) or raises LoginFailed.

        The pending sign-in is consumed first, so a callback works once. The
        handle the browser held at /auth/login is closed: each sign-in gets a
        new one.
        """
        attempt = self._pending.take(cookies.get(LOGIN_COOKIE))
        state = query.get("state", [None])[0]
        if attempt is None or not state or not hmac.compare_digest(state, attempt["state"]):
            raise LoginFailed(400, "pgmi.login.state_invalid", "unknown, expired or replayed sign-in")
        if "error" in query:
            raise LoginFailed(400, "pgmi.login.provider_error", query["error"][0][:100])
        code = query.get("code", [None])[0]
        if not code:
            raise LoginFailed(400, "pgmi.login.state_invalid", "the callback carries no code")

        claims = self.provider.verify(self.provider.exchange(code, attempt["verifier"]), attempt["nonce"])
        # pgmi-specific: the database decides; see ../PROTOCOL.md "Sign-in".
        with connect() as conn:
            token, refused = conn.execute(
                "SELECT token, problem FROM api.open_login_session(%s, %s, %s, %s, %s, %s)",
                login_arguments(claims)).fetchone()
            if token is None:
                raise LoginFailed(403, refused, "the database refused the sign-in")
            self._close_previous(conn, attempt["previous"])
        return attempt["return_to"], self._session_cookies(token) + [clear_cookie(LOGIN_COOKIE)]

    def dev_sign_in(self, cookies, subject, email, display_name, connect):
        """Sign in as a development issuer's login. NULL from the database
        means the issuer is not a development one."""
        with connect() as conn:
            token = conn.execute("SELECT api.open_dev_login_session(%s, %s, %s, %s)",
                                 [DEV_ISSUER, subject, email, display_name]).fetchone()[0]
            if token is None:
                raise LoginFailed(403, "membership.login.refused", "the database refused the sign-in")
            self._close_previous(conn, cookies.get(SESSION_COOKIE))
        return self._session_cookies(token)

    def _close_previous(self, conn, previous):
        if previous:
            conn.execute("SELECT api.close_login_session(%s)", [previous])

    def _session_cookies(self, token):
        return [
            set_cookie(SESSION_COOKIE, token, SESSION_MAX_AGE),
            set_cookie(CSRF_COOKIE, self.csrf_token(token), SESSION_MAX_AGE, http_only=False),
        ]

    def logout(self, cookies, connect):
        login_session = cookies.get(SESSION_COOKIE)
        if login_session:
            with connect() as conn:
                conn.execute("SELECT api.close_login_session(%s)", [login_session])
        return [clear_cookie(SESSION_COOKIE), clear_cookie(CSRF_COOKIE)]
