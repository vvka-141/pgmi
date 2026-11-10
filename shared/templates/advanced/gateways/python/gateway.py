#!/usr/bin/env python3
"""
HTTP Gateway - Bridges HTTP requests to the PostgreSQL gateway functions.

Demo code from pgmi. Do not copy this file into the project as is. Read it,
then write your own gateway that fits this project. Namespaces, modules and
package names are examples: rename them to match the project and add its
specifics freely. The wire rules it keeps are in ../PROTOCOL.md.

This gateway enables AI clients (Claude Desktop, VS Code Copilot, etc.) to
communicate with your PostgreSQL database via the Model Context Protocol (MCP),
and, with OIDC configured, signs people in and serves the REST API to their
browsers (the reference login transport, login_transport.py).

Architecture:
    AI Client --> HTTP POST /mcp   --> This Gateway --> api.mcp_handle_request()
    Browser   --> HTTP /api/<path> --> This Gateway --> api.rest_invoke()
    Browser   --> /auth/*          --> This Gateway --> OIDC provider, api.open_login_session()

The gateway:
1. Receives JSON-RPC 2.0 requests on POST /mcp
2. Checks the caller's OAuth access token (resource_server.py), or passes
   the connection handle from Authorization: Bearer cn_... or X-Connection
3. Calls api.mcp_handle_request(request, context) in PostgreSQL
4. Returns the JSON-RPC 2.0 response envelope

Setup:
    1. Install dependencies:
       pip install -r requirements.txt

    2. Set environment variables:
       export DATABASE_URL="postgresql://mydb_gateway:secret@localhost:5432/mydb"

    3. Start the gateway:
       python gateway.py

    4. (Optional) Configure Claude Desktop (~/.config/claude/claude_desktop_config.json):
       {
         "mcpServers": {
           "my-database": {
             "url": "http://localhost:8080/mcp"
           }
         }
       }

Environment Variables:
    DATABASE_URL          - PostgreSQL connection string (required)
                           Example: postgresql://mydb_gateway:secret@localhost:5432/mydb
                           (a login in the api role, never a superuser)
    PORT                  - HTTP port (default: 8080)
    HOST                  - HTTP host (default: 127.0.0.1). Bind to 0.0.0.0 only
                           behind a trusted reverse proxy that authenticates.
    ALLOWED_ORIGINS       - Comma-separated Origin allowlist for browser clients
                           (default: http://localhost:PORT, http://127.0.0.1:PORT).
                           A request with an Origin header not on this list is
                           rejected (DNS-rebinding protection), on every endpoint.
                           Non-browser clients that send no Origin are allowed.
    OIDC_*, LOGIN_CSRF_SECRET - sign-in; see login_transport.py. Unset, the
                           /auth endpoints answer 404 and /api is anonymous.
    DEV_LOGIN             - "1" serves POST /auth/dev-login, which signs in
                           through api.open_dev_login_session. That function
                           exists only in an env=dev deploy; never set this in
                           production.
    POOL_MAX_SIZE         - most database connections held (default: 10)
    RETRY_MAX_ATTEMPTS    - attempts at a transaction that loses a
                           serialization race (default: 3)
    RETRY_BASE_DELAY      - seconds; first backoff ceiling, doubled per
                           attempt, capped at 1 s (default: 0.05)
    REQUEST_DEADLINE_SECONDS - seconds a request may take, the database's
                           statement_timeout included (default: 30)
    MCP_RESOURCE, MCP_AUTHORIZATION_SERVERS, MCP_SCOPES - the OAuth resource
                           server on /mcp; see resource_server.py. Unset, /mcp
                           takes X-Connection handles and anonymous calls.
    RATE_LIMIT_PER_IP     - /mcp, /auth/login and /auth/callback requests a
                           minute per client address, one limit (600; 0 off)
    RATE_LIMIT_PER_CLIENT - /mcp requests a minute per OAuth client (300; 0
                           off); both in resource_server.py
    INTERCEPTORS          - comma-separated example interceptors to run, in
                           order: embed, request-id, header-names (default: none)
    EMBED_FAILURE_POLICY  - fail (default) or degrade, for embed

Endpoints:
    POST /mcp     - MCP JSON-RPC endpoint, both eras (see "MCP transport" below)
    any other method on /mcp - 405 with Allow: POST (no SSE stream, no session)
    GET  /health  - 200 {"status": "healthy"} once the process serves requests
    GET  /.well-known/oauth-protected-resource[/mcp] - the resource metadata,
                    with MCP_RESOURCE set
    anything else - 404 problem pgmi.not_found
    ANY  /api/... - REST: api.rest_invoke with the path after /api, the login
                    session taken from the session cookie only
    GET  /auth/login, GET /auth/callback, POST /auth/logout - sign-in (OIDC)
    POST /auth/dev-login - {"subject", "email", "displayName"}: development
                    sign-in, with DEV_LOGIN=1 only

Example Usage:
    # MCP 2026-07-28: discover the server
    curl -X POST http://localhost:8080/mcp \\
        -H "Content-Type: application/json" \\
        -H "MCP-Protocol-Version: 2026-07-28" -H "Mcp-Method: server/discover" \\
        -d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}'

    # MCP 2026-07-28: call a tool as the person who approved the connection
    curl -X POST http://localhost:8080/mcp \\
        -H "Content-Type: application/json" \\
        -H "MCP-Protocol-Version: 2026-07-28" -H "Mcp-Method: tools/call" -H "Mcp-Name: list_tasks" \\
        -H "X-Connection: cn_..." \\
        -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_tasks","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'

    # Legacy clients: initialize, then requests without _meta
    curl -X POST http://localhost:8080/mcp \\
        -H "Content-Type: application/json" \\
        -d '{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2025-11-25"}}'

MCP transport (Streamable HTTP, JSON answers only):
    POST only; a body that is not Content-Type application/json is 415, one
    that does not parse is 400 with -32700. An Origin not on the allowlist is
    403. Everything else about the protocol is the database's: this gateway
    forwards the body and the MCP-Protocol-Version, Mcp-Method and Mcp-Name
    headers, as sent, to api.mcp_request_policy. Its refusal (-32020 when a
    header disagrees with the body, -32022 for an unknown version, -32602
    for a missing clientCapabilities) is the whole answer and the request is
    never dispatched, so it logs no exchange, and it is always 400. The
    status of a dispatched answer follows the table in ../PROTOCOL.md ("MCP
    status codes"), the one piece of protocol knowledge kept here. A
    notification is 202 with no body. Mcp-Session-Id and Last-Event-ID are
    ignored: there is no session.

Extension seams (../PROTOCOL.md, "Extension seams"):
    channel.py      every database call behind one interface, decorated at
                    startup: retries and tracing are decorators, so are yours
    interceptors.py request interceptors after the policy call and before the
                    transaction, response interceptors after the commit;
                    selected by what the policy returns about the handler

Transaction Policy (resolve-then-open):
    Routes declare their transaction policy at registration: an isolation floor
    (minTransactionIsolation); MCP handlers cannot be readOnly, since every MCP
    exchange is logged. This gateway resolves that policy BEFORE opening the
    dispatch transaction by calling api.mcp_request_policy(request,
    requested_level, <the three MCP headers>) in a short transaction of its
    own, then opens the dispatch transaction with the returned
    characteristics:

        BEGIN TRANSACTION ISOLATION LEVEL <resolved> [READ ONLY [DEFERRABLE]];

    Routes therefore just work: a caller that sends nothing gets the level the
    route requires. X-PGMI-Transaction-Isolation (read-committed |
    repeatable-read | serializable, case- and separator-insensitive) is
    escalation, not obligation — the resolved level is max(route floor,
    requested), so a client may ask for more than the floor and can never get
    less.

    The database-side gateway keeps its own fail-closed checks — it cannot set
    the level itself, only reject a shortfall with
    pgmi.transaction_isolation_too_weak.
    That is what a proxy that skips the lookup gets, not the path correct
    callers travel.

    REST under /api follows the same pattern through api.rest_route_policy.
    RPC has no pgmi-shipped client: api.rpc_invoke is called by a proxy you
    supply. Implement the same resolve-then-open pattern there with the
    SQL-side resolvers:

        -- outside the dispatch transaction (autocommit):
        SELECT * FROM api.rest_route_policy('GET', '/report', <requested-or-NULL>);
        -- returns (transaction_isolation, transaction_read_only, transaction_deferrable)

        BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE READ ONLY DEFERRABLE;
        SELECT api.rest_invoke('GET', '/report', ...);
        COMMIT;

    (api.rpc_route_policy(method_name, requested) is the JSON-RPC equivalent;
    zero rows means "no such route" — open at the requested level or default
    and let the gateway answer 404.) A proxy that skips the lookup and opens at
    the connection default (read committed, read-write) gets 428 Precondition
    Required from any route with a stronger policy.

Serialization-Failure Retry (40001 / 40P01):
    Declaring a stronger isolation level buys you a stronger guarantee at the
    price of transient aborts: PostgreSQL cancels conflicting transactions with
    40001 (serialization failure) or 40P01 (deadlock), and the ONLY remedy is to
    retry the whole transaction from a fresh snapshot.

    The database gateway therefore lets these two SQLSTATEs propagate untouched
    instead of sanitizing them into a 500 — a 500 tells the client nothing, and
    the client cannot distinguish "your transaction lost a race, try again" from
    "this handler is broken". Every other SQLSTATE is still sanitized.

    This gateway retries such a request up to RETRY_MAX_ATTEMPTS times
    (default 3) with exponential backoff plus jitter, opening a NEW transaction
    per attempt. When the attempts are exhausted, MCP answers the JSON-RPC
    error -31000 with data.code pgmi.transaction_retryable and a Retry-After
    header (the agent repeats the call unchanged); REST answers 409 with the
    same code.

    The retry MUST be a new transaction. A savepoint rolls back writes but does
    not refresh the snapshot, so under repeatable read / serializable an
    in-transaction retry re-reads identical data and conflicts forever. Do not
    write a retry loop in PL/pgSQL: it cannot converge.

    Your own RPC proxy owns BEGIN, so it owns the retry:

        for attempt in range(3):
            try:
                with connect() as conn:                  # fresh BEGIN each time
                    conn.isolation_level = resolved_level
                    return conn.execute("SELECT api.rpc_invoke(...)")
            except (SerializationFailure, DeadlockDetected):
                sleep(backoff * 2**attempt + jitter)
        return http_409_with_retry_after()

    HANDLER REQUIREMENT: a handler on a route that can hit 40001 must be safe to
    execute twice. A retry re-runs the entire handler, so any side effect not
    covered by the transaction — an outbound HTTP call, a queue publish outside
    the transaction, a non-idempotent external write — happens again.

Security Notes:
    - Binds to 127.0.0.1 by default; validates the Origin header against an
      allowlist (DNS-rebinding protection) and returns sanitized errors.
    - On /mcp the caller is an OAuth client whose access token this gateway
      verified (issuer, subject and client id go to the database, never the
      token), or the handle of a connection a person approved on the web
      (POST /connections, signed in, naming the organization and an agent
      role), sent as Authorization: Bearer cn_... or X-Connection. The
      database resolves either into that person's agent in that
      organization; a handle it does not know is 401. No other header names
      an identity or an organization, and nobody is provisioned on /mcp
    - /mcp is rate limited per client address before any token check, so a
      flood of forged tokens never reaches the identity provider, and per
      OAuth client after it
    - REST requests carry the login session only from the HttpOnly session
      cookie; a client-sent X-Login-Session or X-Connection is dropped, a
      repeated single-valued header (X-Organization-Session, ...) is a 400,
      and a state-changing request with the cookie needs the X-CSRF-Token
    - Request bodies over 1 MiB are 413; a stalled client times out
    - Set HOST=0.0.0.0 only behind a reverse proxy that terminates TLS
    - Never expose directly to the internet without authentication

For production deployments, consider:
    - Running several gateway processes behind the reverse proxy (this is a
      threaded stdlib http.server; there is no WSGI 'app' object to hand
      to gunicorn)
    - Keeping POOL_MAX_SIZE times the number of processes below the server's
      max_connections, or PgBouncer in transaction mode in between
    - TLS termination at the load balancer
"""

import os
import re
import json
import select
import socket
import socketserver
import threading
import time
import urllib.parse
import uuid
from dataclasses import replace
from http.server import HTTPServer, BaseHTTPRequestHandler
import psycopg
from psycopg_pool import ConnectionPool, PoolTimeout

import channel
import interceptors
import login_transport
import resource_server


# Configuration from environment
DATABASE_URL = os.environ.get("DATABASE_URL")
HOST = os.environ.get("HOST", "127.0.0.1")
PORT = int(os.environ.get("PORT", "8080"))

# ADAPT: the name operators see in pg_stat_activity and the server log.
APPLICATION_NAME = "pgmi-gateway-python"
# ADAPT: bounded; the server's max_connections is shared by every process.
POOL_MAX_SIZE = int(os.environ.get("POOL_MAX_SIZE", "10"))
POOL_TIMEOUT_SECONDS = 5
MAX_REQUEST_BODY = 1024 * 1024
CLIENT_TIMEOUT_SECONDS = 30
# Whole-request limits, as the other gateways set them: the request line and
# headers within HEADER_READ_SECONDS of the previous response (or the
# connection), the body within BODY_READ_SECONDS. CLIENT_TIMEOUT_SECONDS is
# per recv only: a client trickling a byte at a time would never meet it.
HEADER_READ_SECONDS = 10
BODY_READ_SECONDS = 30
# Opened by main(): one pool per process, never a connection per request.
POOL = None

# Serialization-failure retry: channel.SerializationRetry, which says why only
# a new transaction converges and why a handler that can hit 40001 must be
# safe to run twice.
MAX_RETRY_ATTEMPTS = int(os.environ.get("RETRY_MAX_ATTEMPTS", "3"))
RETRY_BASE_DELAY = float(os.environ.get("RETRY_BASE_DELAY", "0.05"))
RETRY_AFTER_SECONDS = "1"

# The two extension seams (../PROTOCOL.md, "Extension seams"): the database
# channel, decorated at startup, and the interceptor pipeline. main() builds
# both once; request threads only read them.
CHANNEL = None
PIPELINE = interceptors.Pipeline()
# A request is abandoned after this; interceptors read what is left of it from
# their context, and it is the database's statement_timeout. A client that
# hangs up sooner cancels its statement: watch_for_hangup.
REQUEST_DEADLINE_SECONDS = float(os.environ.get("REQUEST_DEADLINE_SECONDS", "30"))
HANGUP_POLL_SECONDS = 0.1


def watch_for_hangup(sock, hangup, log=print):
    """Watches a client socket while its request is in the database and calls
    hangup.happened() if the client goes away; returns the function that stops
    the watch.

    http.server reads a request and then never looks at the socket until it
    answers, so it cannot see a hang-up on its own. Once the body is read,
    the socket turns readable only when the client sends more: an empty peek
    is its FIN (or a reset), real bytes are a pipelined next request, which is
    left for http.server and ends the watch. A client that half-closes after
    sending counts as gone, as it does for Go's net/http."""
    stop = threading.Event()

    def watch():
        while not stop.is_set():
            try:
                readable, _, _ = select.select([sock], [], [], HANGUP_POLL_SECONDS)
                if not readable:
                    continue
                if sock.recv(1, socket.MSG_PEEK):
                    return
            except (OSError, ValueError):
                if stop.is_set():
                    return
            try:
                hangup.happened()
            except psycopg.Error as e:
                log("[gateway] cancel after a hang-up failed: %s" % e)
            return

    threading.Thread(target=watch, daemon=True, name="hangup-watch").start()
    return stop.set


def build_pipeline(names, embed_failure):
    """The example interceptors INTERCEPTORS names, in its order. Unset, none
    run and every body passes as bytes.

    ADAPT: build your own here, each with its long-lived dependencies: one
    shared, pooled HTTP client, a signing key from configuration."""
    requests, responses = [], []
    for name in names:
        if name == "embed":
            requests.append(interceptors.EmbedFields(interceptors.FakeEmbedder(), failure=embed_failure))
        elif name == "request-id":
            responses.append(interceptors.RequestIdHeader())
        elif name == "header-names":
            requests.append(interceptors.HeaderNames())
        else:
            raise ValueError("unknown interceptor %r; known: embed, request-id, header-names" % name)
    return interceptors.Pipeline(requests, responses)


def _allowed_origins():
    """Origin allowlist for DNS-rebinding protection (browser clients)."""
    configured = os.environ.get("ALLOWED_ORIGINS")
    if configured:
        return {o.strip() for o in configured.split(",") if o.strip()}
    return {f"http://localhost:{PORT}", f"http://127.0.0.1:{PORT}"}


ALLOWED_ORIGINS = _allowed_origins()

LOGIN = login_transport.LoginTransport.from_environment()
RESOURCE = resource_server.ResourceServer.from_environment(os.environ.get("OIDC_CLIENT_ID"))
PER_IP = resource_server.RateLimiter.from_environment("RATE_LIMIT_PER_IP", 600)
PER_CLIENT = resource_server.RateLimiter.from_environment("RATE_LIMIT_PER_CLIENT", 300)
REST_PREFIX = "/api"

# The MCP headers api.mcp_request_policy checks against the body. The gateway
# knows no protocol version: which ones exist, and whether a header matches,
# is the database's answer. ADAPT: forward them as your server hands them over.
MCP_HEADERS = ("MCP-Protocol-Version", "Mcp-Method", "Mcp-Name")

# Transaction policy contract (see lib/api/00-transaction-isolation.sql).
# The CLIENT opens the transaction with the route's resolved characteristics
# BEFORE the first statement (channel.PostgresChannel); the database gateway
# only validates them. PostgreSQL folds "read uncommitted" onto "read
# committed"; so do we.
_CANONICAL_ISOLATION = {
    "read committed": "read committed",
    "read uncommitted": "read committed",
    "repeatable read": "repeatable read",
    "serializable": "serializable",
}


def _normalize_isolation(value):
    """Canonicalize an X-PGMI-Transaction-Isolation value.

    Tolerates case and hyphen/underscore/space separators, mirroring
    internal.normalize_transaction_isolation in SQL. Returns the canonical
    level text, or None if the value is not a supported level.
    """
    canonical = re.sub(r"[\s_-]+", " ", value.strip().lower())
    return _CANONICAL_ISOLATION.get(canonical)


def _new_context(protocol, method, url):
    return channel.RequestContext(request_id=uuid.uuid4().hex, protocol=protocol, method=method, url=url,
                                  deadline=time.monotonic() + REQUEST_DEADLINE_SECONDS)


def _mcp_route(request):
    """What api.mcp_request_policy routes on: an interceptor that changes it
    sends the request to another handler, so the policy is resolved again."""
    params = request.get("params") if isinstance(request, dict) else None
    params = params if isinstance(params, dict) else {}
    return request.get("method") if isinstance(request, dict) else None, params.get("name"), params.get("uri")


def _restated(request, mcp_headers):
    """The Mcp-Method and Mcp-Name a rewritten request states, for the second
    policy call: the client's own were checked against the body it sent."""
    method, name, uri = _mcp_route(request)
    return mcp_headers[0], method, uri if method == "resources/read" else name


def _qvalue(params):
    """The q parameter of one media range, defaulting to 1 (RFC 9110 12.4.2)."""
    for param in params:
        name, _, value = param.partition("=")
        if name.strip() != "q":
            continue
        try:
            return float(value.strip())
        except ValueError:
            return 1.0
    return 1.0


def _accepts_json(accept_header):
    """True if an Accept header admits application/json.

    Mirrors api.accept_matches: the most specific matching range decides
    (application/json, then application/*, then */*), and q=0 there is an
    explicit refusal (RFC 9110 12.5.1), even next to a wildcard.
    """
    specificity = {"application/json": 3, "application/*": 2, "*/*": 1}
    decided = max(
        ((specificity[media_range.strip().lower()], _qvalue(params.split(";")))
         for media_range, _, params in (entry.partition(";") for entry in accept_header.split(","))
         if media_range.strip().lower() in specificity),
        default=(0, 0.0),
    )
    return decided[1] > 0


def _request_id(message):
    """The JSON-RPC id of a request object, or None."""
    if isinstance(message, dict):
        rid = message.get("id")
        if isinstance(rid, (str, int)) and not isinstance(rid, bool):
            return rid
    return None


def _envelope_status(envelope, modern):
    """The HTTP status of a dispatched MCP answer (../PROTOCOL.md, "MCP status
    codes"). A refusal never gets here: it is always 400.

    In the 2026-07-28 era (modern, from api.mcp_request_policy) -32601 is 404
    and -32022 is 400; every other answer is 200, a tool's -32602 included:
    the client reads JSON-RPC errors from the body. Every legacy answer is 200.
    """
    if not modern or not isinstance(envelope.get("error"), dict):
        return 200
    code = envelope["error"].get("code")
    return 404 if code == -32601 else 400 if code == -32022 else 200


def _bearer_handle(authorization):
    """A connection handle sent as Authorization: Bearer cn_..., or None.
    A cn_ handle is not an OAuth token and never reaches the verifier."""
    scheme, _, credentials = (authorization or "").partition(" ")
    credentials = credentials.strip()
    return credentials if scheme.lower() == "bearer" and credentials.startswith("cn_") else None


def _handle_context(headers, oauth=False):
    """The MCP context a connection handle gives: {"connection": handle}, or
    {} when none was sent. The handle comes as Authorization: Bearer cn_...
    or, unless another Authorization is to be verified (oauth), as
    X-Connection. The database resolves it into the person who approved the
    connection and the organization chosen then. Identity, email, tenant or
    session headers are never forwarded, so a client cannot name them.
    """
    authorization = headers.get("Authorization")
    handle = _bearer_handle(authorization)
    if handle is None and (not oauth or authorization is None):
        handle = headers.get("X-Connection") or None
    return {"connection": handle} if handle else {}


def _handle_unknown(envelope):
    """The database knows no live connection by the handle sent: the
    gateway answers 401, as for a token it refused."""
    error = envelope.get("error") if isinstance(envelope, dict) else None
    return (isinstance(error, dict) and error.get("code") == -31001
            and isinstance(error.get("data"), dict) and error["data"].get("code") == "pgmi.connection_unknown")


def unsafe_header(pairs):
    """The first header name whose name or value holds CR, LF or NUL, or None.
    send_header writes what it is given: a handler's value with a CR/LF would
    become a header line of its own (a Set-Cookie the database never meant)."""
    for name, value in pairs:
        if any(c in str(name) or c in str(value) for c in "\r\n\0"):
            return str(name).translate({13: None, 10: None, 0: None})
    return None


class MCPHandler(BaseHTTPRequestHandler):
    """Every route: /mcp, /api, /auth/*, /health and the resource metadata."""

    timeout = CLIENT_TIMEOUT_SECONDS
    headers_timed_out = False
    body_timed_out = False

    def _cut_off(self, how):
        try:
            self.connection.shutdown(how)
        except OSError:
            pass

    def _header_deadline(self):
        self.headers_timed_out = True
        self._cut_off(socket.SHUT_RDWR)

    def handle_one_request(self):
        # A watchdog per request: the header deadline closes the connection;
        # the body deadline ends the read, and _read_body answers 408.
        self.headers_timed_out = self.body_timed_out = False
        self._stop_watching = None
        self._watchdog = threading.Timer(HEADER_READ_SECONDS, self._header_deadline)
        self._watchdog.daemon = True
        self._watchdog.start()
        try:
            super().handle_one_request()
        finally:
            self._watchdog.cancel()
            if self._stop_watching is not None:
                self._stop_watching()

    def _watch_for_hangup(self, ctx):
        """From here to the answer, a client that hangs up cancels the
        request's statement. Only after the body is read: the body itself
        would read as the next request."""
        self._stop_watching = watch_for_hangup(self.connection, ctx.hangup)

    def _client_gone(self, ctx):
        self.log_message("client hung up (%s); statement cancelled" % ctx.request_id)
        self.close_connection = True

    def parse_request(self):
        parsed = super().parse_request()
        self._watchdog.cancel()
        if self.headers_timed_out:
            # The headers were cut off part way: never dispatch what arrived.
            self.close_connection = True
            return False
        return parsed

    def _body_deadline(self):
        self.body_timed_out = True
        self._cut_off(socket.SHUT_RD)

    def _read_body(self):
        """The request body, or None once a 400, 411 or 413 has been answered.

        ADAPT: this server reads a body by its Content-Length only; a chunked
        body is refused 411 rather than read as empty."""
        declared = self.headers.get("Content-Length")
        if declared is None and "Transfer-Encoding" in self.headers:
            self.send_problem(411, "Length Required", "pgmi.length_required",
                              "send the body with a Content-Length")
            self.close_connection = True
            return None
        if declared is None:
            return b""
        if not declared.isdigit():
            self.send_problem(400, "Bad Request", "pgmi.bad_request")
            return None
        if int(declared) > MAX_REQUEST_BODY:
            self.send_problem(413, "Content Too Large", "pgmi.body_too_large",
                              f"the limit is {MAX_REQUEST_BODY} bytes")
            self.close_connection = True
            return None
        self._watchdog = threading.Timer(BODY_READ_SECONDS, self._body_deadline)
        self._watchdog.daemon = True
        self._watchdog.start()
        body = self.rfile.read(int(declared))
        self._watchdog.cancel()
        if len(body) < int(declared):
            self.close_connection = True
            if self.body_timed_out:
                self.send_problem(408, "Request Timeout", "pgmi.request_timeout",
                                  f"the body did not arrive within {BODY_READ_SECONDS} s")
            return None
        return body

    def do_POST(self):
        if self._rest_url() is not None:
            self._rest()
            return
        path = urllib.parse.urlsplit(self.path).path
        if path == "/auth/logout":
            self._logout()
            return
        if path == "/auth/dev-login":
            self._dev_login()
            return
        if path != "/mcp":
            self.send_not_found()
            return

        # Rate limit first, before anything costs: a flood of forged tokens
        # stops here and never reaches the identity provider's key set.
        if not self._rate_allowed(PER_IP, self.client_address[0]):
            return

        # Standard web plumbing, not pgmi-specific: replace it with your framework's own.
        # DNS-rebinding protection: a browser client sends Origin; reject any
        # value not on the allowlist. Non-browser clients send no Origin.
        origin = self.headers.get("Origin")
        if origin is not None and origin not in ALLOWED_ORIGINS:
            self.send_problem(403, "Forbidden", "pgmi.origin_refused")
            return

        content_type = (self.headers.get("Content-Type") or "").split(";")[0].strip().lower()
        if content_type != "application/json":
            self.send_problem(415, "Unsupported Media Type", "pgmi.unsupported_media_type",
                              "MCP requires Content-Type: application/json")
            return

        # This endpoint only emits application/json (no SSE stream). A client
        # that accepts neither JSON nor a wildcard gets 406.
        accept = self.headers.get("Accept")
        if accept and not _accepts_json(accept):
            self.send_problem(406, "Not Acceptable", "pgmi.not_acceptable", "this endpoint answers application/json")
            return

        # Echoed on every answer. Not self.protocol_version: http.server writes
        # that into the status line, and no client could parse the answer.
        self.mcp_protocol_version = self.headers.get("MCP-Protocol-Version")

        context = self._mcp_caller()
        if context is None:
            return

        body = self._read_body()
        if body is None:
            return
        try:
            request = json.loads(body.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            self.send_json_response(400, {
                "jsonrpc": "2.0",
                "id": None,
                "error": {"code": -32700, "message": "Parse error"}
            })
            return
        request_id = _request_id(request)

        # X-PGMI-Transaction-Isolation is escalation, not obligation: the
        # resolved level below is max(route floor, this request). Absent →
        # the route's own policy. Unsupported → 400 (rejected before hitting
        # the database).
        requested = None
        iso_header = self.headers.get("X-PGMI-Transaction-Isolation")
        if iso_header:
            requested = _normalize_isolation(iso_header)
            if requested is None:
                self.send_json_response(400, {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "error": {
                        "code": -32600,
                        "message": f"Unsupported X-PGMI-Transaction-Isolation: {iso_header}",
                        "data": {"code": "pgmi.transaction_isolation_unsupported"},
                    },
                })
                return

        ctx = _new_context("mcp", str(request.get("method")) if isinstance(request, dict) else "", "/mcp")
        self._watch_for_hangup(ctx)
        mcp_headers = tuple(self.headers.get(h) for h in MCP_HEADERS)
        try:
            # pgmi-specific: the database decides; see ../PROTOCOL.md "MCP".
            # One call before the transaction: the protocol checks, the
            # route's transaction policy and the handler's metadata. A refusal
            # is the answer; nothing is dispatched, so nothing is logged.
            policy = CHANNEL.mcp_policy(ctx, request, requested, mcp_headers)
            if policy.refusal is None:
                ctx = channel.with_policy(ctx, policy)
                selected = PIPELINE.requests_for(ctx)
                if selected:
                    # The request is already parsed: interceptors share it.
                    # On /mcp only the body reaches the database.
                    message = interceptors.Request("POST", "/mcp",
                                                   login_transport.incoming_headers(self.headers.items()),
                                                   None, parsed=request)
                    route = _mcp_route(request)
                    PIPELINE.run_requests(selected, message, ctx)
                    request = message.json
                    if _mcp_route(request) != route:
                        policy = CHANNEL.mcp_policy(ctx, request, requested, _restated(request, mcp_headers))
                        if policy.refusal is None:
                            ctx = channel.with_policy(ctx, policy)
            if policy.refusal is not None:
                self.send_json_response(400, policy.refusal)
                return
            envelope = CHANNEL.mcp_request(ctx, policy, request, context)
            if _handle_unknown(envelope):
                self._unauthorized("invalid_token")
                return
            status = _envelope_status(envelope, policy.modern) if envelope is not None else 202
            headers = {}
            selected = PIPELINE.responses_for(ctx) if envelope is not None else ()
            if selected:
                answer = interceptors.Response(status, headers, None, parsed=envelope)
                PIPELINE.run_responses(selected, answer, ctx)
                status, headers, envelope = answer.status, answer.headers, answer.json
        except channel.RetriesExhausted as e:
            # The transaction lost a race every time it ran: nothing was
            # applied, and the same call may succeed when repeated.
            self.log_message("serialization failure, retries exhausted (%d attempts): %s"
                             % (e.attempts, e.sqlstate))
            self.send_json_response(200, {
                "jsonrpc": "2.0",
                "id": request_id,
                "error": {
                    "code": -31000,
                    "message": "The call conflicted with a concurrent change and was rolled back; repeat it unchanged",
                    "data": {
                        "code": "pgmi.transaction_retryable",
                        "sqlstate": e.sqlstate,
                        "attempts": e.attempts,
                    },
                },
            }, extra_headers=[("Retry-After", RETRY_AFTER_SECONDS)])
            return
        except interceptors.InterceptorFailed:
            self.send_json_response(502, {
                "jsonrpc": "2.0",
                "id": request_id,
                "error": {"code": -32603, "message": "The gateway could not complete the call",
                          "data": {"code": "pgmi.interceptor_failed", "requestId": ctx.request_id}},
            })
            return
        except channel.ClientGone:
            self._client_gone(ctx)
            return
        except PoolTimeout:
            self._mcp_unavailable(request_id)
            return
        except channel.DeadlineExceeded:
            self.log_message("deadline exceeded (%s) after %ss" % (ctx.request_id, REQUEST_DEADLINE_SECONDS))
            self.send_json_response(504, {
                "jsonrpc": "2.0",
                "id": request_id,
                "error": {"code": -32603, "message": "The call ran past its deadline and was cancelled",
                          "data": {"code": "pgmi.deadline_exceeded", "requestId": ctx.request_id}},
            })
            return
        except Exception as e:
            # Log the detail server-side; return a sanitized message so raw
            # exception text / tracebacks are not exposed to the client.
            self.log_message("dispatch error (%s): %s" % (ctx.request_id, e))
            self.send_json_response(500, {
                "jsonrpc": "2.0",
                "id": request_id,
                "error": {"code": -32603, "message": "Internal error"}
            })
            return

        # A NULL envelope means a JSON-RPC notification (no response). Per MCP
        # Streamable HTTP, accepted notifications return 202 with no body.
        if envelope is None:
            self.send_response(202)
            if self.mcp_protocol_version:
                self.send_header("MCP-Protocol-Version", self.mcp_protocol_version)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        self.send_json_response(status, envelope, extra_headers=headers.items())

    def do_GET(self):
        if self._rest_url() is not None:
            self._rest()
            return
        path = urllib.parse.urlsplit(self.path).path
        if path == "/auth/login":
            self._login()
            return
        if path == "/auth/callback":
            self._callback()
            return
        if path == "/health":
            self.send_json_response(200, {"status": "healthy"})
            return
        if RESOURCE is not None and path in RESOURCE.metadata_paths:
            self.send_json_response(200, RESOURCE.metadata())
            return
        if path == "/mcp":
            self._mcp_method_not_allowed()
            return
        self.send_not_found()

    def _mcp_caller(self):
        """The MCP context for the database, or None once the answer is sent.

        A connection handle goes on as it is (_handle_context). With
        MCP_RESOURCE set, any other bearer token is verified here and only
        the identity it proves goes on, and a request with neither is 401,
        so a client learns where to get a token.
        """
        context = _handle_context(self.headers, oauth=RESOURCE is not None)
        if context or RESOURCE is None:
            return context
        authorization = self.headers.get("Authorization")
        try:
            identity = RESOURCE.verify(authorization)
        except resource_server.TokenRejected as e:
            # The reason is logged, the token never is.
            self.log_message("token refused: %s" % e)
            self._unauthorized(e.error)
            return None
        except resource_server.ProviderUnavailable as e:
            self.log_message("identity provider unavailable: %s" % e)
            self.close_connection = True
            self.send_json_response(503, {
                "jsonrpc": "2.0", "id": None,
                "error": {"code": -31000, "message": "The identity provider is unavailable; repeat the call later",
                          "data": {"code": "pgmi.identity_provider_unavailable"}},
            }, extra_headers=[("Retry-After", "5")])
            return None
        if not self._rate_allowed(PER_CLIENT, (identity.issuer, identity.subject, identity.client_id)):
            return None
        return identity.context()

    def _unauthorized(self, error):
        """401 with the challenge that tells an MCP client where the
        protected-resource metadata is (RFC 9728 §5.1), when OAuth is on."""
        self.close_connection = True
        self.send_response(401)
        self.send_header("WWW-Authenticate",
                         RESOURCE.challenge(error) if RESOURCE is not None else f'Bearer error="{error}"')
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _rate_allowed(self, limiter, key):
        allowed, retry_after = limiter.allow(key)
        if not allowed:
            self.close_connection = True
            self.send_problem(429, "Too Many Requests", "pgmi.rate_limited",
                              headers=[("Retry-After", str(retry_after))])
        return allowed

    def _mcp_unavailable(self, request_id):
        """No pooled connection within POOL_TIMEOUT_SECONDS: 503 with
        Retry-After, as REST answers it, and data.code pgmi.unavailable."""
        self.log_message("no pooled connection within %ss" % POOL_TIMEOUT_SECONDS)
        self.send_json_response(503, {
            "jsonrpc": "2.0",
            "id": request_id,
            "error": {
                "code": -31000,
                "message": "The database is unavailable; repeat the call later",
                "data": {"code": "pgmi.unavailable"},
            },
        }, extra_headers=[("Retry-After", "1")])

    def _mcp_method_not_allowed(self):
        """JSON answers only: no SSE stream to open (GET), no session to end
        (DELETE), nothing else to do on the MCP endpoint."""
        self.send_problem(405, "Method Not Allowed", "pgmi.method_not_allowed", headers=[("Allow", "POST")])

    def _rest_only(self):
        if urllib.parse.urlsplit(self.path).path == "/mcp":
            self._mcp_method_not_allowed()
            return
        if self._rest_url() is None:
            self.send_not_found()
            return
        self._rest()

    do_HEAD = do_PUT = do_PATCH = do_DELETE = do_OPTIONS = _rest_only

    def _rest_url(self):
        """The REST URL under REST_PREFIX, query included, or None."""
        parts = urllib.parse.urlsplit(self.path)
        if parts.path != REST_PREFIX and not parts.path.startswith(REST_PREFIX + "/"):
            return None
        url = parts.path[len(REST_PREFIX):] or "/"
        return url + "?" + parts.query if parts.query else url

    def _login_session(self, cookies):
        return cookies.get(login_transport.SESSION_COOKIE) if LOGIN else None

    def _origin_allowed(self):
        origin = self.headers.get("Origin")
        if origin is not None and origin not in ALLOWED_ORIGINS:
            self.send_problem(403, "Forbidden", "pgmi.origin_refused")
            return False
        return True

    def _csrf_allowed(self, login_session):
        if LOGIN and not LOGIN.csrf_ok(self.command, login_session, self.headers.get(login_transport.CSRF_HEADER)):
            self.send_problem(403, "Forbidden", "pgmi.csrf_refused",
                              "send the " + login_transport.CSRF_COOKIE + " value in X-CSRF-Token")
            return False
        return True

    def _rest(self):
        """REST over api.rest_invoke, as the login transport contract says:
        the login session from the cookie, never from a client header."""
        if not self._origin_allowed():
            return
        cookies = login_transport.read_cookies(self.headers)
        login_session = self._login_session(cookies)
        if not self._csrf_allowed(login_session):
            return
        try:
            headers = login_transport.forwarded_headers(self.headers.items(), login_session)
        except login_transport.HeaderCollision as e:
            self.send_problem(400, "Bad Request", "pgmi.header_collision", f"header {e} is sent more than once")
            return

        requested = None
        iso_header = headers.get("x-pgmi-transaction-isolation")
        if iso_header:
            requested = _normalize_isolation(iso_header)
            if requested is None:
                self.send_problem(400, "Bad Request", "pgmi.transaction_isolation_unsupported")
                return

        url = self._rest_url()
        body = self._read_body()
        if body is None:
            return
        body = body or None
        ctx = _new_context("rest", self.command, url)
        self._watch_for_hangup(ctx)
        result = None
        try:
            # pgmi-specific: the database decides; see ../PROTOCOL.md "One
            # request, one transaction". The policy call comes before the
            # dispatch transaction opens.
            policy = CHANNEL.rest_policy(ctx, requested)
            ctx = channel.with_policy(ctx, policy)
            selected = PIPELINE.requests_for(ctx)
            if selected:
                # The policy call returned its connection: none is held while
                # an interceptor waits on another service.
                request = interceptors.Request(self.command, url,
                                               login_transport.incoming_headers(self.headers.items()), body)
                PIPELINE.run_requests(selected, request, ctx)
                # Identity is the gateway's: whatever an interceptor did to
                # the headers, the session is the cookie's again.
                headers = login_transport.forwarded_headers(request.headers.items(), login_session)
                body = request.body
                if (request.method, request.url) != (ctx.method, ctx.url):
                    ctx = replace(ctx, method=request.method, url=request.url)
                    policy = CHANNEL.rest_policy(ctx, requested)
                    ctx = channel.with_policy(ctx, policy)
            result = CHANNEL.rest_invoke(ctx, policy, headers, body)
            status, response_headers, content = result.status, result.headers, result.content
            selected = PIPELINE.responses_for(ctx)
            if selected:
                response = interceptors.Response(status, dict(response_headers), content)
                PIPELINE.run_responses(selected, response, ctx)
                status, response_headers, content = response.status, response.headers, response.body or b""
        except channel.RetriesExhausted as e:
            self.log_message("serialization failure, retries exhausted: %s" % e.sqlstate)
            self.send_problem(409, "Conflict", "pgmi.transaction_retryable",
                              headers=[("Retry-After", RETRY_AFTER_SECONDS)])
            return
        except interceptors.InterceptorFailed:
            # After the commit, the request was applied: say so, so the
            # client does not repeat a write that already happened.
            self.send_problem(502, "Bad Gateway", "pgmi.interceptor_failed",
                              "the request was not applied" if result is None
                              else "the request was applied; its answer could not be completed",
                              headers=[("X-Request-Id", ctx.request_id)])
            return
        except channel.ClientGone:
            self._client_gone(ctx)
            return
        except PoolTimeout:
            self.log_message("no pooled connection within %ss" % POOL_TIMEOUT_SECONDS)
            self.send_problem(503, "Service Unavailable", "pgmi.unavailable", headers=[("Retry-After", "1")])
            return
        except channel.DeadlineExceeded:
            self.log_message("deadline exceeded (%s) after %ss" % (ctx.request_id, REQUEST_DEADLINE_SECONDS))
            self.send_problem(504, "Gateway Timeout", "pgmi.deadline_exceeded",
                              headers=[("X-Request-Id", ctx.request_id)])
            return
        except Exception as e:
            self.log_message("rest dispatch error (%s): %s" % (ctx.request_id, e))
            self.send_problem(500, "Internal Server Error", "pgmi.internal_error")
            return

        # 1xx, 204 and 304 carry no content and no Content-Length (RFC 9110
        # §8.6); HEAD declares the length GET would have sent.
        bodiless = status < 200 or status in (204, 304)
        unsafe = unsafe_header(response_headers.items())
        if unsafe is not None:
            self.log_message("handler %s answered header %r with CR, LF or NUL; refused (%s)"
                             % (ctx.handler, unsafe, ctx.request_id))
            self.send_problem(500, "Internal Server Error", "pgmi.internal_error")
            return
        self.send_response(status)
        for name, value in response_headers.items():
            if name not in ("content-length", "transfer-encoding", "connection"):
                self.send_header(name, value)
        if bodiless:
            pass
        elif self.command != "HEAD":
            self.send_header("Content-Length", str(len(content)))
        elif "content-length" in response_headers:
            self.send_header("Content-Length", response_headers["content-length"])
        self.end_headers()
        if self.command != "HEAD" and not bodiless:
            self.wfile.write(content)

    def _login(self):
        if LOGIN is None or LOGIN.provider is None:
            self.send_not_found()
            return
        # One limit with /mcp per address: a flood of anonymous sign-ins
        # cannot push other people's pending sign-ins out of the store.
        if not self._rate_allowed(PER_IP, self.client_address[0]):
            return
        query = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
        try:
            location, cookies = LOGIN.begin(query.get("returnTo", [None])[0],
                                            login_transport.read_cookies(self.headers))
        except login_transport.LoginFailed as e:
            self.send_problem(e.status, "Sign-in failed", e.code)
            return
        self.send_redirect(302, location, cookies)

    def _callback(self):
        if LOGIN is None or LOGIN.provider is None:
            self.send_not_found()
            return
        if not self._rate_allowed(PER_IP, self.client_address[0]):
            return
        cookies = login_transport.read_cookies(self.headers)
        query = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
        clear = [("Set-Cookie", login_transport.clear_cookie(login_transport.LOGIN_COOKIE))]
        try:
            location, set_cookies = LOGIN.complete(cookies, query, POOL.connection)
        except login_transport.LoginFailed as e:
            self.log_message("sign-in refused: %s (%s)" % (e.code, e.detail))
            self.send_problem(e.status, "Sign-in failed", e.code, headers=clear)
            return
        except Exception as e:
            self.log_message("sign-in error: " + str(e))
            self.send_problem(500, "Internal Server Error", "pgmi.internal_error", headers=clear)
            return
        self.send_redirect(303, location, set_cookies)

    def _logout(self):
        if LOGIN is None:
            self.send_not_found()
            return
        if not self._origin_allowed():
            return
        cookies = login_transport.read_cookies(self.headers)
        if not self._csrf_allowed(self._login_session(cookies)):
            return
        try:
            set_cookies = LOGIN.logout(cookies, POOL.connection)
        except Exception as e:
            self.log_message("logout error: " + str(e))
            self.send_problem(500, "Internal Server Error", "pgmi.internal_error")
            return
        self.send_response(204)
        self.send_header("Cache-Control", "no-store")
        for cookie in set_cookies:
            self.send_header("Set-Cookie", cookie)
        self.end_headers()

    def _dev_login(self):
        """Development sign-in through api.open_dev_login_session, in place of
        an identity provider. 404 unless DEV_LOGIN=1; the database has the
        function only in an env=dev deploy, so both must agree."""
        if LOGIN is None or not LOGIN.dev_login:
            self.send_not_found()
            return
        if not self._origin_allowed():
            return
        raw = self._read_body()
        if raw is None:
            return
        try:
            body = json.loads(raw or b"{}")
            subject, email, name = body["subject"], body["email"], body.get("displayName")
            if not isinstance(subject, str) or not isinstance(email, str) or not isinstance(name, (str, type(None))):
                raise ValueError("subject and email must be strings")
        except (ValueError, KeyError, TypeError, UnicodeDecodeError):
            self.send_problem(400, "Bad Request", "pgmi.login.request_invalid",
                              "send {\"subject\", \"email\", \"displayName\"} as JSON strings")
            return
        try:
            set_cookies = LOGIN.dev_sign_in(login_transport.read_cookies(self.headers),
                                            subject, email, name, POOL.connection)
        except login_transport.LoginFailed as e:
            self.send_problem(e.status, "Sign-in failed", e.code)
            return
        except Exception as e:
            self.log_message("dev sign-in error: " + str(e))
            self.send_problem(500, "Internal Server Error", "pgmi.internal_error")
            return
        self.send_response(204)
        self.send_header("Cache-Control", "no-store")
        for cookie in set_cookies:
            self.send_header("Set-Cookie", cookie)
        self.end_headers()

    def send_redirect(self, status, location, cookies):
        self.send_response(status)
        self.send_header("Location", location)
        self.send_header("Cache-Control", "no-store")
        for cookie in cookies:
            self.send_header("Set-Cookie", cookie)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def send_not_found(self):
        self.send_problem(404, "Not Found", "pgmi.not_found")

    def send_problem(self, status, title, code, detail=None, headers=()):
        if unsafe_header(headers) is not None:
            self.log_message("refused header %r with CR, LF or NUL" % unsafe_header(headers))
            status, title, code, detail, headers = 500, "Internal Server Error", "pgmi.internal_error", None, ()
        body = json.dumps(login_transport.problem(status, title, code, detail)).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/problem+json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        for name, value in headers:
            self.send_header(name, value)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def send_json_response(self, status, data, extra_headers=()):
        extra_headers = list(extra_headers)
        if unsafe_header(extra_headers) is not None:
            self.log_message("refused header %r with CR, LF or NUL" % unsafe_header(extra_headers))
            self.send_problem(500, "Internal Server Error", "pgmi.internal_error")
            return
        body = json.dumps(data).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        if getattr(self, "mcp_protocol_version", None):
            self.send_header("MCP-Protocol-Version", self.mcp_protocol_version)
        for name, value in extra_headers:
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def log_request(self, code="-", size="-"):
        # The path only: a callback's query carries the authorization code.
        self.log_message('"%s %s" %s', self.command, urllib.parse.urlsplit(self.path).path, code)

    def log_message(self, format, *args):
        print(f"[gateway] {format % args if args else format}")


class _Gateway(socketserver.ThreadingMixIn, HTTPServer):
    """A thread per request, all sharing the one pool; and an HTTPServer that
    does not reverse-resolve its own bind address.

    http.server fills server_name by calling socket.getfqdn(host) inside
    server_bind(), and server_bind() runs BEFORE server_activate() calls
    listen(). On a host whose reverse DNS is slow or unanswered the socket
    therefore sits bound-but-not-listening for the whole lookup: the banner
    above says "Listening", and every client is refused until it finishes
    (more than 20 s on the macOS CI runner).

    server_name and server_port are only read to build CGI-style variables,
    which this gateway does not use, so the address itself is a truthful value.
    """

    daemon_threads = True

    def server_bind(self):
        socketserver.TCPServer.server_bind(self)
        self.server_name, self.server_port = self.server_address[:2]


def database_display(database_url):
    """Where, never who: host, port and database, for the startup line. No user,
    password or other parameter, in a URL's query or a key=value string."""
    try:
        where = psycopg.conninfo.conninfo_to_dict(database_url)
    except psycopg.ProgrammingError:
        return "configured"
    return f"{where.get('host', 'localhost')}:{where.get('port', '5432')}/{where.get('dbname', '')}"


SSL_MODES = ("disable", "allow", "prefer", "require", "verify-ca", "verify-full")


def database_url_problem(database_url, env=os.environ):
    """Why the gateway must not start on DATABASE_URL, or None, in words that
    never quote it. TLS settings are read as libpq reads them: the URL, then
    PGSSLMODE and PGSSLROOTCERT. With sslrootcert=system libpq's default
    sslmode is verify-full."""
    try:
        settings = psycopg.conninfo.conninfo_to_dict(database_url)
    except psycopg.ProgrammingError:
        # The parser's message quotes the URL, password included.
        return "DATABASE_URL is not a valid connection string"
    # libpq splits user info at the first @, so the rest of the password
    # becomes the host and would reach the startup line and pool errors.
    if "@" in settings.get("host", ""):
        return "DATABASE_URL has an unencoded @ in its user or password: percent-encode it as %40"
    root = settings.get("sslrootcert") or env.get("PGSSLROOTCERT")
    mode = settings.get("sslmode") or env.get("PGSSLMODE") or ("verify-full" if root == "system" else None)
    if mode is not None and mode not in SSL_MODES:
        return f"sslmode={mode} is not a libpq sslmode"
    if root == "system" and mode != "verify-full":
        return f"sslrootcert=system needs sslmode=verify-full, not {mode}"
    if not root and mode in ("verify-ca", "verify-full"):
        return f"sslmode={mode} needs sslrootcert in DATABASE_URL: the CA file to trust, or system"
    return None


def main():
    if not DATABASE_URL:
        print("Error: DATABASE_URL environment variable is required")
        print("")
        print("Example:")
        print("  export DATABASE_URL='postgresql://mydb_gateway:secret@localhost:5432/mydb'")
        print("  (a login in the api role: CREATE ROLE mydb_gateway LOGIN PASSWORD 'secret' IN ROLE mydb_api)")
        print("  python gateway.py")
        exit(1)

    problem = database_url_problem(DATABASE_URL)
    if problem:
        print(f"Error: {problem}")
        exit(1)

    display_url = database_display(DATABASE_URL)

    print(f"MCP HTTP Gateway")
    print(f"================")
    print(f"Database: {display_url}")
    print(f"Listening: http://{HOST}:{PORT}")
    print(f"")
    print(f"Endpoints:")
    print(f"  POST /mcp    - MCP JSON-RPC endpoint")
    print(f"  GET  /health - Health check")
    print(f"  ANY  /api/   - REST")
    if LOGIN and LOGIN.provider:
        print(f"  /auth/login, /auth/callback, /auth/logout - sign-in via {LOGIN.provider.issuer}")
    if LOGIN and LOGIN.dev_login:
        print(f"  POST /auth/dev-login - development sign-in (DEV_LOGIN=1)")
    if RESOURCE:
        print(f"  /mcp is an OAuth resource server for {RESOURCE.resource}; metadata at {RESOURCE.metadata_url}")
    print(f"")
    print(f"Press Ctrl+C to stop")

    global POOL, CHANNEL, PIPELINE
    names = [n.strip() for n in os.environ.get("INTERCEPTORS", "").split(",") if n.strip()]
    embed_failure = os.environ.get("EMBED_FAILURE_POLICY", interceptors.FAIL)
    try:
        if embed_failure not in (interceptors.FAIL, interceptors.DEGRADE):
            raise ValueError("EMBED_FAILURE_POLICY must be fail or degrade")
        PIPELINE = build_pipeline(names, embed_failure)
    except ValueError as e:
        print(f"Error: {e}")
        exit(1)
    if names:
        print(f"Interceptors: {', '.join(names)}")

    POOL = ConnectionPool(DATABASE_URL, min_size=1, max_size=POOL_MAX_SIZE, timeout=POOL_TIMEOUT_SECONDS,
                          kwargs={"application_name": APPLICATION_NAME, "connect_timeout": POOL_TIMEOUT_SECONDS},
                          check=ConnectionPool.check_connection)
    # ADAPT: the decorators your gateway needs, in one place. Both retries
    # wrap the tracing, so each attempt is a log line of its own.
    CHANNEL = channel.ConnectionRetry(
        channel.SerializationRetry(channel.TracingChannel(channel.PostgresChannel(POOL)),
                                   MAX_RETRY_ATTEMPTS, RETRY_BASE_DELAY))
    server = _Gateway((HOST, PORT), MCPHandler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nShutting down...")
        server.shutdown()
    finally:
        POOL.close()


if __name__ == "__main__":
    main()
