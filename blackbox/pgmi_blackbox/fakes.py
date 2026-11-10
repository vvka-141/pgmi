import json
import socket
import ssl
import struct
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

AZURE_TOKEN = "fake-azure-access-token-0123456789"


class _Http(BaseHTTPRequestHandler):
    log_path: Path

    def _answer(self) -> None:
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length).decode(errors="replace") if length else ""
        with self.log_path.open("a") as log:
            log.write(f"{self.command} {self.path} {json.dumps(dict(self.headers))} {body}\n")
        host = f"https://{self.headers.get('Host')}"
        if "openid-configuration" in self.path:
            tenant = self.path.strip("/").split("/")[0]
            doc = {
                "token_endpoint": f"{host}/{tenant}/oauth2/v2.0/token",
                "authorization_endpoint": f"{host}/{tenant}/oauth2/v2.0/authorize",
                "issuer": f"{host}/{tenant}/v2.0",
            }
        elif "discovery/instance" in self.path:
            doc = {"tenant_discovery_endpoint": f"{host}/common/v2.0/.well-known/openid-configuration", "metadata": []}
        elif "/oauth2/" in self.path:
            doc = {"token_type": "Bearer", "expires_in": 3600, "ext_expires_in": 3600, "access_token": AZURE_TOKEN}
        else:
            doc = {"access_token": AZURE_TOKEN, "expires_on": "4102444800", "resource": "x", "token_type": "Bearer"}
        payload = json.dumps(doc).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    do_GET = _answer
    do_POST = _answer

    def log_message(self, *args) -> None:
        pass


def _serve_http(log_path: Path, context: ssl.SSLContext | None) -> int:
    handler = type("Handler", (_Http,), {"log_path": log_path})
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    if context:
        server.socket = context.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server.server_address[1]


def _recv_exact(conn, n: int) -> bytes:
    data = b""
    while len(data) < n:
        chunk = conn.recv(n - len(data))
        if not chunk:
            raise ConnectionError("closed")
        data += chunk
    return data


def _recorder_client(conn: socket.socket, context: ssl.SSLContext, log_path: Path) -> None:
    try:
        length, code = struct.unpack("!ii", _recv_exact(conn, 8))
        if code == 80877103:
            conn.sendall(b"S")
            conn = context.wrap_socket(conn, server_side=True)
            length, code = struct.unpack("!ii", _recv_exact(conn, 8))
        params = _recv_exact(conn, length - 8).split(b"\0")
        startup = {params[i].decode(): params[i + 1].decode() for i in range(0, len(params) - 1, 2) if params[i]}
        conn.sendall(b"R" + struct.pack("!ii", 8, 3))
        kind = _recv_exact(conn, 1)
        size = struct.unpack("!i", _recv_exact(conn, 4))[0]
        password = _recv_exact(conn, size - 4).rstrip(b"\0").decode()
        with log_path.open("a") as log:
            log.write(json.dumps({"kind": kind.decode(), "user": startup.get("user"), "password": password}) + "\n")
        fields = b"SFATAL\0C28P01\0Mrecorded by the black-box recorder\0\0"
        conn.sendall(b"E" + struct.pack("!i", len(fields) + 4) + fields)
    except (OSError, ConnectionError, ssl.SSLError, struct.error):
        pass
    finally:
        conn.close()


def _serve_recorder(log_path: Path, context: ssl.SSLContext) -> int:
    listener = socket.create_server(("127.0.0.1", 0))

    def accept() -> None:
        while True:
            conn, _ = listener.accept()
            threading.Thread(target=_recorder_client, args=(conn, context, log_path), daemon=True).start()

    threading.Thread(target=accept, daemon=True).start()
    return listener.getsockname()[1]


def start_fakes(root: Path, certs: Path) -> dict[str, str]:
    root.mkdir(parents=True, exist_ok=True)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(certs / "server.crt", certs / "server.key")
    http_log, https_log, pg_log = root / "http.log", root / "https.log", root / "recorder.log"
    for log in (http_log, https_log, pg_log):
        log.touch()
    return {
        "{fake_http}": f"http://127.0.0.1:{_serve_http(http_log, None)}",
        "{fake_https}": f"https://localhost:{_serve_http(https_log, context)}",
        "{recorder_port}": str(_serve_recorder(pg_log, context)),
        "{fake_http_log}": str(http_log),
        "{fake_https_log}": str(https_log),
        "{recorder_log}": str(pg_log),
        "{azure_token}": AZURE_TOKEN,
    }
