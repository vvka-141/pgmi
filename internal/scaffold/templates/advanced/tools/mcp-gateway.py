#!/usr/bin/env python3
"""
MCP HTTP Gateway - Bridges HTTP requests to PostgreSQL MCP handlers.

This gateway enables AI clients (Claude Desktop, VS Code Copilot, etc.) to
communicate with your PostgreSQL database via the Model Context Protocol (MCP).

Architecture:
    AI Client --> HTTP POST /mcp --> This Gateway --> PostgreSQL --> api.mcp_handle_request()

The gateway:
1. Receives JSON-RPC 2.0 requests on POST /mcp
2. Extracts authentication context from headers (X-User-Id, X-Tenant-Id)
3. Calls api.mcp_handle_request(request, context) in PostgreSQL
4. Returns the JSON-RPC 2.0 response envelope

Setup:
    1. Install dependencies:
       pip install -r requirements.txt

    2. Set environment variables:
       export DATABASE_URL="postgresql://user:pass@localhost:5432/mydb"

    3. Start the gateway:
       python mcp-gateway.py

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
                           Example: postgresql://postgres:secret@localhost:5432/mydb
    PORT                  - HTTP port (default: 8080)
    HOST                  - HTTP host (default: 127.0.0.1). Bind to 0.0.0.0 only
                           behind a trusted reverse proxy that authenticates.
    MCP_ALLOWED_ORIGINS   - Comma-separated Origin allowlist for browser clients
                           (default: http://localhost:PORT, http://127.0.0.1:PORT).
                           A request with an Origin header not on this list is
                           rejected (DNS-rebinding protection). Non-browser clients
                           that send no Origin are allowed.

Endpoints:
    POST /mcp     - MCP JSON-RPC endpoint. Honors the Accept header (returns
                    application/json; no SSE stream) and validates/echoes the
                    MCP-Protocol-Version header.
    GET  /mcp     - 405 Method Not Allowed (no server-initiated SSE stream)
    GET  /health  - Health check (for load balancers)

Example Usage:
    # Initialize handshake
    curl -X POST http://localhost:8080/mcp \\
        -H "Content-Type: application/json" \\
        -d '{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}'

    # List available tools
    curl -X POST http://localhost:8080/mcp \\
        -H "Content-Type: application/json" \\
        -d '{"jsonrpc":"2.0","id":"2","method":"tools/list"}'

    # Call a tool (with authentication)
    curl -X POST http://localhost:8080/mcp \\
        -H "Content-Type: application/json" \\
        -H "X-User-Id: auth0|12345" \\
        -d '{"jsonrpc":"2.0","id":"3","method":"tools/call","params":{"name":"database_info","arguments":{}}}'

Transaction Policy (resolve-then-open):
    Routes declare their transaction policy at registration: an isolation floor
    (minTransactionIsolation) and a read-only flag (readOnly). This gateway
    resolves that policy BEFORE opening the dispatch transaction by calling
    api.mcp_request_policy(request, requested_level) on a short autocommit
    connection, then opens the transaction with the returned characteristics:

        BEGIN TRANSACTION ISOLATION LEVEL <resolved> [READ ONLY [DEFERRABLE]];

    Routes therefore just work: a caller that sends nothing gets the level the
    route requires. X-PGMI-Transaction-Isolation (read-committed |
    repeatable-read | serializable, case- and separator-insensitive) is
    escalation, not obligation — the resolved level is max(route floor,
    requested), so a client may ask for more than the floor and can never get
    less. A read-only route runs READ ONLY (an accidental write in the handler
    becomes a hard error), and SERIALIZABLE READ ONLY opens DEFERRABLE: it
    waits for a conflict-free snapshot at BEGIN and then can never abort with
    40001, so it needs no retries at all.

    The database-side gateway keeps its own fail-closed checks — it cannot set
    the level or access mode itself, only reject a shortfall with
    pgmi.transaction_isolation_too_weak / pgmi.transaction_read_only_required.
    That is what a proxy that skips the lookup gets, not the path correct
    callers travel.

    REST/RPC have no pgmi-shipped client: api.rest_invoke / api.rpc_invoke are
    called by an external reverse proxy you supply. Implement the same
    resolve-then-open pattern there with the SQL-side resolvers:

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

    This gateway retries such a request up to MCP_MAX_RETRY_ATTEMPTS times
    (default 3) with exponential backoff plus jitter, opening a NEW transaction
    per attempt. When the attempts are exhausted it answers 409 with a
    Retry-After header and the machine code pgmi.transaction_retryable.

    The retry MUST be a new transaction. A savepoint rolls back writes but does
    not refresh the snapshot, so under repeatable read / serializable an
    in-transaction retry re-reads identical data and conflicts forever. Do not
    write a retry loop in PL/pgSQL: it cannot converge.

    Your own REST/RPC proxy owns BEGIN, so it owns the retry:

        for attempt in range(3):
            try:
                with connect() as conn:                  # fresh BEGIN each time
                    conn.isolation_level = resolved_level
                    return conn.execute("SELECT api.rest_invoke(...)")
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
    - This gateway trusts the X-User-Id header for authentication
    - In production, place behind a reverse proxy that validates JWTs
      and injects X-User-Id after verification (and set HOST=0.0.0.0 only then)
    - Never expose directly to the internet without authentication

For production deployments, consider:
    - Running several gateway processes behind the reverse proxy (this is a
      single-process stdlib http.server; there is no WSGI 'app' object to hand
      to gunicorn)
    - Connection pooling with PgBouncer
    - TLS termination at the load balancer
"""

import os
import re
import json
import random
import time
from http.server import HTTPServer, BaseHTTPRequestHandler
import psycopg


# Configuration from environment
DATABASE_URL = os.environ.get("DATABASE_URL")
HOST = os.environ.get("HOST", "127.0.0.1")
PORT = int(os.environ.get("PORT", "8080"))

# Serialization-failure retry contract (see lib/api/09-gateways.sql).
#
# Under repeatable read / serializable, PostgreSQL aborts conflicting
# transactions with 40001 (or 40P01 on deadlock) and the ONLY remedy is to retry
# the whole transaction from a fresh snapshot. The database deliberately
# propagates these with SQLSTATE intact instead of flattening them into a 500,
# because that is the one bit of information the client needs to know it should
# retry rather than give up.
#
# Each attempt below opens a NEW connection, therefore a new transaction,
# therefore a new snapshot. This is not incidental: a savepoint rolls back
# writes but cannot refresh the snapshot, so an in-transaction retry re-reads
# identical data, reaches an identical conclusion, and conflicts forever. Only
# ROLLBACK + a fresh BEGIN converges.
#
# CONSEQUENCE FOR HANDLERS: a handler on a route that can hit 40001 must be safe
# to run twice. The retry re-executes the entire handler, so any side effect that
# is not covered by the transaction — an outbound HTTP call, a queue publish
# outside the transaction, a non-idempotent external write — will happen again.
MAX_RETRY_ATTEMPTS = int(os.environ.get("MCP_MAX_RETRY_ATTEMPTS", "3"))
RETRY_BASE_DELAY = float(os.environ.get("MCP_RETRY_BASE_DELAY", "0.05"))
RETRY_MAX_DELAY = float(os.environ.get("MCP_RETRY_MAX_DELAY", "1.0"))
RETRY_AFTER_SECONDS = os.environ.get("MCP_RETRY_AFTER", "1")


def _retry_backoff(attempt):
    """Exponential backoff with full jitter, in seconds, for a 0-based attempt.

    Jitter matters under contention: unjittered backoff makes the losers of a
    race retry in lockstep and collide again.
    """
    ceiling = min(RETRY_MAX_DELAY, RETRY_BASE_DELAY * (2 ** attempt))
    return random.uniform(0, ceiling)


def _allowed_origins():
    """Origin allowlist for DNS-rebinding protection (browser clients)."""
    configured = os.environ.get("MCP_ALLOWED_ORIGINS")
    if configured:
        return {o.strip() for o in configured.split(",") if o.strip()}
    return {f"http://localhost:{PORT}", f"http://127.0.0.1:{PORT}"}


ALLOWED_ORIGINS = _allowed_origins()

# MCP revisions this transport accepts in the MCP-Protocol-Version header.
# Keep in sync with v_supported_versions in lib/api/10-mcp-protocol.sql.
SUPPORTED_PROTOCOL_VERSIONS = (
    "2024-11-05",
    "2025-03-26",
    "2025-06-18",
    "2025-11-25",
)
# Assumed when a client omits the header (Streamable HTTP backwards compat).
DEFAULT_PROTOCOL_VERSION = "2025-03-26"

# Transaction policy contract (see lib/api/00-transaction-isolation.sql).
# The CLIENT opens the transaction with the route's resolved characteristics
# BEFORE the first statement; the database gateway only validates them (it
# cannot change them — transaction control is forbidden inside functions).
# PostgreSQL folds "read uncommitted" onto "read committed"; so do we.
_CANONICAL_ISOLATION = {
    "read committed": "read committed",
    "read uncommitted": "read committed",
    "repeatable read": "repeatable read",
    "serializable": "serializable",
}

_ISOLATION_LEVELS = {
    "read committed": psycopg.IsolationLevel.READ_COMMITTED,
    "repeatable read": psycopg.IsolationLevel.REPEATABLE_READ,
    "serializable": psycopg.IsolationLevel.SERIALIZABLE,
}


def _normalize_isolation(value):
    """Canonicalize an X-PGMI-Transaction-Isolation value.

    Tolerates case and hyphen/underscore/space separators, mirroring
    internal.normalize_transaction_isolation in SQL. Returns the canonical
    level text, or None if the value is not a supported level.
    """
    canonical = re.sub(r"[\s_-]+", " ", value.strip().lower())
    return _CANONICAL_ISOLATION.get(canonical)


def _resolve_policy(request, requested):
    """Resolve the route's transaction policy before opening the dispatch
    transaction (resolve-then-open, two-phase lookup).

    Runs on a short autocommit connection because the whole point is knowing
    the characteristics BEFORE BEGIN. Costs one extra round trip per request;
    it cannot go stale because the registry only changes at deploy time. (A
    caching proxy would key a route->policy map on api.catalog_version().)

    Returns (isolation_text_or_None, read_only, deferrable). None isolation
    means "open at the connection default".
    """
    with psycopg.connect(DATABASE_URL, autocommit=True) as conn:
        return conn.execute(
            "SELECT transaction_isolation, transaction_read_only,"
            " transaction_deferrable FROM api.mcp_request_policy(%s, %s)",
            [json.dumps(request), requested],
        ).fetchone()


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
    """True if an Accept header admits application/json (or a wildcard).

    Mirrors api.accept_matches: a media range with q=0 is an explicit refusal
    (RFC 9110 12.5.1), not an acceptance. Dropping the parameters entirely made
    `Accept: application/json;q=0` read as acceptance of the one type the
    client had just refused.
    """
    for entry in accept_header.split(","):
        media_range, _, params = entry.partition(";")
        if media_range.strip().lower() not in ("application/json", "application/*", "*/*"):
            continue
        if _qvalue(params.split(";")) > 0:
            return True
    return False


class MCPHandler(BaseHTTPRequestHandler):
    """HTTP request handler for MCP JSON-RPC requests."""

    def do_POST(self):
        """Handle POST requests to /mcp endpoint."""
        if self.path != "/mcp":
            self.send_error(404, "Not Found")
            return

        # DNS-rebinding protection: a browser client sends Origin; reject any
        # value not on the allowlist. Non-browser clients send no Origin.
        origin = self.headers.get("Origin")
        if origin is not None and origin not in ALLOWED_ORIGINS:
            self.send_error(403, "Forbidden: Origin not allowed")
            return

        # Content negotiation: this endpoint only emits application/json (no
        # SSE stream). Honor a present Accept; a client that accepts neither
        # JSON nor a wildcard gets 406 rather than a mismatched body.
        accept = self.headers.get("Accept")
        if accept and not _accepts_json(accept):
            self.send_error(406, "Not Acceptable: endpoint returns application/json")
            return

        # MCP-Protocol-Version is a transport-level header sent after
        # initialization. Absent → assume the default; present-but-unsupported
        # → 400. The negotiated value is echoed on every response below.
        pv = self.headers.get("MCP-Protocol-Version") or DEFAULT_PROTOCOL_VERSION
        if pv not in SUPPORTED_PROTOCOL_VERSIONS:
            self.send_json_response(400, {
                "jsonrpc": "2.0",
                "id": None,
                "error": {
                    "code": -32600,
                    "message": f"Unsupported MCP-Protocol-Version: {pv}"
                }
            })
            return
        # NOT self.protocol_version: that is a reserved http.server attribute
        # (default "HTTP/1.0") which BaseHTTPRequestHandler interpolates into
        # the status line, so assigning it emits "2025-03-26 400 Bad Request"
        # and no HTTP client can parse the response.
        self.mcp_protocol_version = pv

        # Read and parse request body
        content_length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(content_length)

        try:
            request = json.loads(body)
        except json.JSONDecodeError:
            self.send_json_response(400, {
                "jsonrpc": "2.0",
                "id": None,
                "error": {"code": -32700, "message": "Parse error"}
            })
            return

        # Build authentication context from headers
        # In production, these should come from a validated JWT
        context = {}
        if self.headers.get("X-User-Id"):
            context["user_id"] = self.headers.get("X-User-Id")
        if self.headers.get("X-Tenant-Id"):
            context["tenant_id"] = self.headers.get("X-Tenant-Id")

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
                    "id": request.get("id"),
                    "error": {
                        "code": -32600,
                        "message": f"Unsupported X-PGMI-Transaction-Isolation: {iso_header}",
                        "data": {"code": "pgmi.transaction_isolation_unsupported"},
                    },
                })
                return

        # Resolve-then-open: fetch the route's transaction policy first, then
        # open the dispatch transaction with the resolved characteristics.
        # Routes just work — the caller does not have to know the floor.
        try:
            isolation, read_only, deferrable = _resolve_policy(request, requested)
        except Exception as e:
            self.log_message("policy resolution error: " + str(e))
            self.send_json_response(500, {
                "jsonrpc": "2.0",
                "id": request.get("id"),
                "error": {"code": -32603, "message": "Internal error"}
            })
            return

        # Call PostgreSQL MCP dispatcher, retrying the whole transaction on a
        # serialization failure or deadlock. Each attempt is a fresh connection
        # and therefore a fresh snapshot — see the retry contract at the top of
        # this file for why nothing less can converge. (A SERIALIZABLE READ
        # ONLY DEFERRABLE transaction cannot hit 40001 at all; the loop is
        # simply never entered twice for those.)
        envelope = None
        for attempt in range(MAX_RETRY_ATTEMPTS):
            try:
                with psycopg.connect(DATABASE_URL) as conn:
                    # Set BEFORE the first statement so it applies to this
                    # transaction. A fresh connection is opened per attempt, so
                    # there is no cross-request state to reset; if a pool is
                    # introduced, reset these on return to avoid leaking them.
                    if isolation is not None:
                        conn.isolation_level = _ISOLATION_LEVELS[isolation]
                    if read_only:
                        conn.read_only = True
                    if deferrable:
                        conn.deferrable = True
                    result = conn.execute(
                        "SELECT (api.mcp_handle_request(%s, %s)).envelope",
                        [json.dumps(request), json.dumps(context) if context else None]
                    ).fetchone()
                    envelope = result[0] if result else None
                break
            except (psycopg.errors.SerializationFailure,
                    psycopg.errors.DeadlockDetected) as e:
                # The transaction is gone. Nothing in it survived, so there is
                # nothing to undo — just run it again from a clean start.
                if attempt == MAX_RETRY_ATTEMPTS - 1:
                    self.log_message(
                        "serialization failure, retries exhausted (%d attempts): %s"
                        % (MAX_RETRY_ATTEMPTS, e.sqlstate)
                    )
                    self.send_json_response(409, {
                        "jsonrpc": "2.0",
                        "id": request.get("id"),
                        "error": {
                            "code": -32603,
                            "message": "Transaction conflict; retry the request",
                            "data": {
                                "code": "pgmi.transaction_retryable",
                                "sqlstate": e.sqlstate,
                                "attempts": MAX_RETRY_ATTEMPTS,
                            },
                        },
                    }, extra_headers={"Retry-After": RETRY_AFTER_SECONDS})
                    return
                time.sleep(_retry_backoff(attempt))
            except Exception as e:
                # Log the detail server-side; return a sanitized message so raw
                # exception text / tracebacks are not exposed to the client.
                self.log_message("dispatch error: " + str(e))
                self.send_json_response(500, {
                    "jsonrpc": "2.0",
                    "id": request.get("id"),
                    "error": {"code": -32603, "message": "Internal error"}
                })
                return

        # A NULL envelope means a JSON-RPC notification (no response). Per MCP
        # Streamable HTTP, accepted notifications return 202 with no body.
        if envelope is None:
            self.send_response(202)
            if getattr(self, "mcp_protocol_version", None):
                self.send_header("MCP-Protocol-Version", self.mcp_protocol_version)
            self.end_headers()
            return

        self.send_json_response(200, envelope)

    def do_GET(self):
        """Handle GET requests."""
        if self.path == "/health":
            self.send_json_response(200, {"status": "healthy"})
            return
        if self.path == "/mcp":
            # Streamable HTTP: this gateway offers no server-initiated SSE
            # stream, so answer GET on the MCP endpoint with 405 (not 404) and
            # advertise the supported method.
            self.send_response(405)
            self.send_header("Allow", "POST")
            self.end_headers()
            return
        self.send_error(404, "Not Found")

    def send_json_response(self, status, data, extra_headers=None):
        """Send a JSON response with proper headers."""
        body = json.dumps(data).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        if getattr(self, "mcp_protocol_version", None):
            self.send_header("MCP-Protocol-Version", self.mcp_protocol_version)
        for name, value in (extra_headers or {}).items():
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        """Custom log format."""
        print(f"[MCP] {format % args if args else format}")


def main():
    """Start the MCP HTTP gateway."""
    if not DATABASE_URL:
        print("Error: DATABASE_URL environment variable is required")
        print("")
        print("Example:")
        print("  export DATABASE_URL='postgresql://postgres:postgres@localhost:5432/mydb'")
        print("  python mcp-gateway.py")
        exit(1)

    # Mask password in log output
    display_url = DATABASE_URL.split('@')[-1] if '@' in DATABASE_URL else 'configured'

    print(f"MCP HTTP Gateway")
    print(f"================")
    print(f"Database: {display_url}")
    print(f"Listening: http://{HOST}:{PORT}")
    print(f"")
    print(f"Endpoints:")
    print(f"  POST /mcp    - MCP JSON-RPC endpoint")
    print(f"  GET  /health - Health check")
    print(f"")
    print(f"Press Ctrl+C to stop")

    server = HTTPServer((HOST, PORT), MCPHandler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nShutting down...")
        server.shutdown()


if __name__ == "__main__":
    main()
