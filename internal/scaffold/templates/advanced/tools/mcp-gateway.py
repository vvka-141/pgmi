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

Security Notes:
    - Binds to 127.0.0.1 by default; validates the Origin header against an
      allowlist (DNS-rebinding protection) and returns sanitized errors.
    - This gateway trusts the X-User-Id header for authentication
    - In production, place behind a reverse proxy that validates JWTs
      and injects X-User-Id after verification (and set HOST=0.0.0.0 only then)
    - Never expose directly to the internet without authentication

For production deployments, consider:
    - Running with gunicorn: gunicorn -w 4 -b 0.0.0.0:8080 'mcp-gateway:app'
    - Connection pooling with PgBouncer
    - TLS termination at the load balancer
"""

import os
import json
from http.server import HTTPServer, BaseHTTPRequestHandler
import psycopg


# Configuration from environment
DATABASE_URL = os.environ.get("DATABASE_URL")
HOST = os.environ.get("HOST", "127.0.0.1")
PORT = int(os.environ.get("PORT", "8080"))


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


def _accepts_json(accept_header):
    """True if an Accept header admits application/json (or a wildcard)."""
    types = [t.split(";")[0].strip() for t in accept_header.split(",")]
    return any(t in ("application/json", "application/*", "*/*") for t in types)


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
        self.protocol_version = pv

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

        # Call PostgreSQL MCP dispatcher
        try:
            with psycopg.connect(DATABASE_URL) as conn:
                result = conn.execute(
                    "SELECT (api.mcp_handle_request(%s, %s)).envelope",
                    [json.dumps(request), json.dumps(context) if context else None]
                ).fetchone()
                envelope = result[0] if result else None
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
            if getattr(self, "protocol_version", None):
                self.send_header("MCP-Protocol-Version", self.protocol_version)
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

    def send_json_response(self, status, data):
        """Send a JSON response with proper headers."""
        body = json.dumps(data).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        if getattr(self, "protocol_version", None):
            self.send_header("MCP-Protocol-Version", self.protocol_version)
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
