import json
import random
import socket
import ssl
import struct
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from pgmi_blackbox.certs import make_certs, openssl

AZURE_TOKEN = "fake-azure-access-token-0123456789"
GOOGLE_TOKEN = "fake-google-access-token-0123456789"
GOOGLE_PROJECT, GOOGLE_REGION, GOOGLE_NAME = "fake-project", "fake-region", "fake-instance"
CLOUD_SQL_PORT = 3307


class CloudSql:
    def __init__(self, root: Path, ca: Path, ip: str) -> None:
        self.root, self.ca, self.ip = root, ca, ip

    def settings(self, path: str) -> dict | None:
        if f"/projects/{GOOGLE_PROJECT}/instances/{GOOGLE_NAME}" not in path:
            return None
        if path.endswith("/connectSettings"):
            return {
                "kind": "sql#connectSettings",
                "ipAddresses": [{"type": "PRIMARY", "ipAddress": self.ip}],
                "serverCaCert": {"cert": (self.ca / "ca.crt").read_text()},
                "serverCaMode": "GOOGLE_MANAGED_INTERNAL_CA",
                "databaseVersion": "POSTGRES_18",
                "region": GOOGLE_REGION,
                "backendType": "SECOND_GEN",
            }
        return None

    def sign(self, body: str) -> dict | None:
        try:
            public_key = json.loads(body)["public_key"]
        except (ValueError, KeyError, TypeError):
            return None
        with tempfile.TemporaryDirectory(dir=self.root) as d:
            work = Path(d)
            (work / "client.pub").write_text(public_key)
            (work / "ext").write_text("basicConstraints=CA:FALSE\nkeyUsage=digitalSignature\n")
            ca = self.ca
            try:
                openssl(
                    "x509", "-new", "-force_pubkey", "client.pub", "-subj", "/CN=ephemeral",
                    "-CA", str(ca / "ca.crt"), "-CAkey", str(ca / "ca.key"), "-days", "1",
                    "-extfile", "ext", "-out", "client.crt", cwd=work,
                )
            except subprocess.CalledProcessError:
                return None
            return {"ephemeralCert": {"cert": (work / "client.crt").read_text()}}


def _cloud_sql_ca(d: Path) -> None:
    d.mkdir(parents=True, exist_ok=True)
    openssl(
        "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-subj", "/CN=fake-cloud-sql-ca",
        "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-keyout", "ca.key", "-out", "ca.crt", cwd=d,
    )
    openssl(
        "req", "-newkey", "rsa:2048", "-nodes", "-subj", f"/CN={GOOGLE_PROJECT}:{GOOGLE_NAME}",
        "-keyout", "server.key", "-out", "server.csr", cwd=d,
    )
    (d / "server.ext").write_text("basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\n")
    openssl(
        "x509", "-req", "-in", "server.csr", "-CA", "ca.crt", "-CAkey", "ca.key", "-CAcreateserial",
        "-days", "2", "-extfile", "server.ext", "-out", "server.crt", cwd=d,
    )


class _Http(BaseHTTPRequestHandler):
    log_path: Path
    cloud_sql: CloudSql | None = None

    def _answer(self) -> None:
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length).decode(errors="replace") if length else ""
        with self.log_path.open("a") as log:
            log.write(f"{self.command} {self.path} {json.dumps(dict(self.headers))} {body}\n")
        host = f"https://{self.headers.get('Host')}"
        code = 200
        if self.path.startswith("/google/token"):
            doc = {"access_token": GOOGLE_TOKEN, "expires_in": 3600, "token_type": "Bearer"}
        elif self.path.startswith("/sql/v1beta4/"):
            found = None
            if self.cloud_sql and self.command == "GET":
                found = self.cloud_sql.settings(self.path)
            elif self.cloud_sql and self.path.endswith(":generateEphemeralCert"):
                found = self.cloud_sql.settings(self.path.removesuffix(":generateEphemeralCert") + "/connectSettings")
                found = found and self.cloud_sql.sign(body)
            code = 200 if found else 404
            doc = found or {"error": {"code": 404, "message": "not found in the fake Cloud SQL Admin API"}}
        elif "openid-configuration" in self.path:
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
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    do_GET = _answer
    do_POST = _answer

    def log_message(self, *args) -> None:
        pass


def _serve_http(log_path: Path, context: ssl.SSLContext | None, cloud_sql: CloudSql | None = None) -> int:
    handler = type("Handler", (_Http,), {"log_path": log_path, "cloud_sql": cloud_sql})
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


def _recorder_client(conn: socket.socket, context: ssl.SSLContext, log_path: Path, direct: bool = False) -> None:
    try:
        if direct:
            conn = context.wrap_socket(conn, server_side=True)
        length, code = struct.unpack("!ii", _recv_exact(conn, 8))
        if code == 80877103 and not direct:
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


def _serve_recorder(log_path: Path, context: ssl.SSLContext, listener: socket.socket | None = None) -> int:
    direct = listener is not None
    listener = listener or socket.create_server(("127.0.0.1", 0))

    def accept() -> None:
        while True:
            conn, _ = listener.accept()
            threading.Thread(target=_recorder_client, args=(conn, context, log_path, direct), daemon=True).start()

    threading.Thread(target=accept, daemon=True).start()
    return listener.getsockname()[1]


def _cloud_sql_listener() -> socket.socket | None:
    for last in random.sample(range(2, 255), 40):
        try:
            return socket.create_server((f"127.0.0.{last}", CLOUD_SQL_PORT))
        except OSError:
            continue
    return None


def _start_cloud_sql(root: Path, log_path: Path) -> tuple[CloudSql | None, dict[str, str]]:
    listener = _cloud_sql_listener()
    if listener is None:
        return None, {}
    ca = root / "cloudsql"
    _cloud_sql_ca(ca)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.load_cert_chain(ca / "server.crt", ca / "server.key")
    context.load_verify_locations(ca / "ca.crt")
    context.verify_mode = ssl.CERT_REQUIRED
    _serve_recorder(log_path, context, listener)
    return CloudSql(root, ca, listener.getsockname()[0]), {
        "{google_instance}": f"{GOOGLE_PROJECT}:{GOOGLE_REGION}:{GOOGLE_NAME}",
        "{google_token}": GOOGLE_TOKEN,
        "{cloud_sql_ca}": str(ca / "ca.crt"),
    }


def start_fakes(root: Path, certs: Path) -> dict[str, str]:
    root.mkdir(parents=True, exist_ok=True)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(certs / "server.crt", certs / "server.key")
    http_log, https_log, pg_log = root / "http.log", root / "https.log", root / "recorder.log"
    for log in (http_log, https_log, pg_log):
        log.touch()
    cloud_sql, google = _start_cloud_sql(root, pg_log)
    fake_http = f"http://127.0.0.1:{_serve_http(http_log, None, cloud_sql)}"
    if cloud_sql:
        adc = root / "google-adc.json"
        adc.write_text(
            json.dumps(
                {
                    "type": "authorized_user",
                    "client_id": "fake-client-id",
                    "client_secret": "fake-client-secret",
                    "refresh_token": "fake-refresh-token",
                    "token_uri": f"{fake_http}/google/token",
                }
            )
        )
        google["{google_credentials}"] = str(adc)
    return google | {
        "{fake_http}": fake_http,
        "{fake_https}": f"https://localhost:{_serve_http(https_log, context)}",
        "{recorder_port}": str(_serve_recorder(pg_log, context)),
        "{fake_http_log}": str(http_log),
        "{fake_https_log}": str(https_log),
        "{recorder_log}": str(pg_log),
        "{azure_token}": AZURE_TOKEN,
    }


def main() -> None:
    root = Path(sys.argv[1])
    make_certs(root / "certs")
    print(json.dumps(start_fakes(root / "fakes", root / "certs")), flush=True)
    sys.stdin.read()


if __name__ == "__main__":
    main()
