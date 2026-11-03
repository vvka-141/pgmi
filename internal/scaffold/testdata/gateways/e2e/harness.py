"""What the suites share: the deployed database, the mock identity provider,
the gateway under test as a child process, and an HTTP client that keeps
cookies the way a browser does."""

import http.client
import json
import os
import pathlib
import shlex
import socket
import subprocess
import tempfile
import time
import urllib.parse

import psycopg
from psycopg.conninfo import conninfo_to_dict, make_conninfo

from mock_idp import CLIENT_ID, CLIENT_SECRET, MockProvider

HERE = pathlib.Path(__file__).resolve().parent
DATABASE = "gateways_e2e"
GATEWAY_ROLE = "gateways_e2e_gateway"
OPERATOR_SUBJECT = "operator"
OPERATOR_EMAIL = "operator@example.com"
RETRY_ATTEMPTS = 3
OWNED_ENV = ("DATABASE_URL", "HOST", "PORT", "ALLOWED_ORIGINS", "DEV_LOGIN", "LOGIN_CSRF_SECRET",
             "POOL_MAX_SIZE", "RETRY_MAX_ATTEMPTS", "RETRY_BASE_DELAY", "INTERCEPTORS", "EMBED_FAILURE_POLICY",
             "OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URI", "OIDC_SCOPES",
             "MCP_RESOURCE", "MCP_AUTHORIZATION_SERVERS", "MCP_SCOPES", "RATE_LIMIT_PER_IP", "RATE_LIMIT_PER_CLIENT")

FIXTURE_SQL = """
CREATE SEQUENCE public.transport_attempt;
GRANT USAGE ON SEQUENCE public.transport_attempt TO PUBLIC;

SELECT api.create_or_replace_mcp_handler(
    '{"id": "f4500000-0000-4000-8000-0000000000e1", "type": "tool", "effect": "read", "name": "transport_echo",
      "description": "Echoes its arguments", "access": "public"}'::jsonb,
    $b$BEGIN RETURN api.mcp_tool_result('[]', (request).request_id, false, (request).arguments); END;$b$);

SELECT api.create_or_replace_mcp_handler(
    '{"id": "f4500000-0000-4000-8000-0000000000e2", "type": "tool", "effect": "write", "reversible": true,
      "name": "transport_conflict_once", "description": "Loses its first race", "access": "public"}'::jsonb,
    $b$BEGIN
        IF nextval('public.transport_attempt') % 2 = 1 THEN
            RAISE EXCEPTION 'conflict' USING ERRCODE = 'serialization_failure';
        END IF;
        RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('done')), (request).request_id);
    END;$b$);

SELECT api.create_or_replace_mcp_handler(
    '{"id": "f4500000-0000-4000-8000-0000000000e3", "type": "tool", "effect": "write", "reversible": true,
      "name": "transport_conflict_always", "description": "Loses every race", "access": "public"}'::jsonb,
    $b$BEGIN
        PERFORM nextval('public.transport_attempt');
        RAISE EXCEPTION 'conflict' USING ERRCODE = 'serialization_failure';
    END;$b$);

SELECT api.create_or_replace_mcp_handler(
    '{"id": "f4500000-0000-4000-8000-0000000000e5", "type": "tool", "effect": "read", "name": "transport_slow",
      "description": "Holds its connection for eight seconds", "access": "public"}'::jsonb,
    $b$BEGIN
        PERFORM pg_sleep(8);
        RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('slept')), (request).request_id);
    END;$b$);

SELECT api.create_or_replace_rest_handler(
    '{"id": "f4500000-0000-4000-8000-0000000000f1", "path": "/intercept/echo", "httpMethod": "^POST$",
      "name": "intercept_echo", "access": "public",
      "inputSchema": {"type": "object", "properties": {"text": {"type": "string", "x-embed": true},
                                                       "text_embedding": {"type": "array"}}}}'::jsonb,
    $b$BEGIN
        RETURN ROW(200, extensions.hstore('content-type', 'application/json'), (request).content)::api.http_response;
    END;$b$);

SELECT api.create_or_replace_rest_handler(
    '{"id": "f4500000-0000-4000-8000-0000000000f2", "path": "/intercept/plain", "httpMethod": "^POST$",
      "name": "intercept_plain", "access": "public"}'::jsonb,
    $b$BEGIN
        RETURN ROW(200, extensions.hstore('content-type', 'application/json'), (request).content)::api.http_response;
    END;$b$);

SELECT api.create_or_replace_mcp_handler(
    '{"id": "f4500000-0000-4000-8000-0000000000f3", "type": "tool", "effect": "read", "name": "intercept_echo",
      "description": "Echoes its arguments", "access": "public",
      "inputSchema": {"type": "object", "properties": {"text": {"type": "string", "x-embed": true},
                                                       "text_embedding": {"type": "array"}}}}'::jsonb,
    $b$BEGIN RETURN api.mcp_tool_result('[]', (request).request_id, false, (request).arguments); END;$b$);

SELECT api.create_or_replace_mcp_handler(
    '{"id": "f4500000-0000-4000-8000-0000000000e6", "type": "tool", "effect": "read", "name": "transport_whoami",
      "description": "Who the call acts as", "access": "authenticated"}'::jsonb,
    $b$BEGIN
        RETURN api.mcp_tool_result('[]', (request).request_id, false, jsonb_build_object(
            'principalId', api.current_principal_id(), 'actualPrincipalId', api.current_actual_principal_id(),
            'actorKind', api.current_actor_kind(), 'organizationId', api.current_organization_id(),
            'permissions', to_jsonb(api.current_permissions())));
    END;$b$);

SELECT api.create_or_replace_mcp_handler(
    '{"id": "f4500000-0000-4000-8000-0000000000e4", "type": "resource", "name": "transport_note",
      "description": "A note", "access": "public", "uriTemplate": "note://transport"}'::jsonb,
    $b$BEGIN
        RETURN api.mcp_resource_result(jsonb_build_array(jsonb_build_object('uri', (request).uri, 'text', 'n')),
                                       (request).request_id);
    END;$b$);
"""


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Response:
    def __init__(self, status, headers, body):
        self.status = status
        self.headers = headers
        self.body = body

    def header(self, name):
        return next((v for k, v in self.headers if k.lower() == name.lower()), None)

    def set_cookies(self):
        return [v for k, v in self.headers if k.lower() == "set-cookie"]

    def json(self):
        return json.loads(self.body)


def request(base, method, path, headers=(), body=None, timeout=30):
    """One request on its own connection. A body the server refuses part way
    (413) may break the send; the answer is still read."""
    parts = urllib.parse.urlsplit(base)
    conn = http.client.HTTPConnection(parts.hostname, parts.port, timeout=timeout)
    try:
        conn.putrequest(method, path, skip_accept_encoding=True)
        names = {name.lower() for name, _ in headers}
        for name, value in headers:
            conn.putheader(name, value)
        if body is not None and "content-length" not in names:
            conn.putheader("Content-Length", str(len(body)))
        conn.endheaders()
        if body:
            try:
                conn.send(body)
            except OSError:
                pass
        raw = conn.getresponse()
        return Response(raw.status, raw.getheaders(), raw.read())
    finally:
        conn.close()


class Browser:
    """Cookies by name, sent to every port of the host; Max-Age=0 deletes."""

    def __init__(self):
        self.cookies = {}

    def request(self, method, url, headers=(), body=None):
        parts = urllib.parse.urlsplit(url)
        headers = list(headers)
        if self.cookies:
            headers.insert(0, ("Cookie", "; ".join(f"{k}={v}" for k, v in self.cookies.items())))
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers.append(("Content-Type", "application/json"))
        elif method not in ("GET", "HEAD"):
            data = b""
        base = f"{parts.scheme}://{parts.netloc}"
        response = request(base, method, parts.path + ("?" + parts.query if parts.query else ""), headers, data)
        for cookie in response.set_cookies():
            pair, _, attributes = cookie.partition(";")
            name, _, value = pair.strip().partition("=")
            if "max-age=0" in attributes.lower().replace(" ", "") or value == "":
                self.cookies.pop(name, None)
            else:
                self.cookies[name] = value
        return response


class Gateway:
    """The gateway under test: GATEWAY_COMMAND (argv, shell-quoted) run in
    GATEWAY_DIR with the environment this harness owns. In GATEWAY_COMMAND and
    GATEWAY_STOP, {port} becomes this instance's port and {env_file} a file
    holding that environment as NAME=value lines, for docker run --env-file."""

    def __init__(self, env):
        self.port = int(env["PORT"])
        self.base = f"http://127.0.0.1:{self.port}"
        self.env_file = tempfile.NamedTemporaryFile("w", suffix=".env", delete=False)
        self.env_file.write("".join(f"{k}={v}\n" for k, v in env.items()))
        self.env_file.close()
        self.log = tempfile.TemporaryFile()
        process_env = {k: v for k, v in os.environ.items() if k not in OWNED_ENV}
        process_env.update(env, PYTHONDONTWRITEBYTECODE="1", PYTHONUNBUFFERED="1")
        self.process = subprocess.Popen(self._argv("GATEWAY_COMMAND"), cwd=os.environ["GATEWAY_DIR"],
                                        env=process_env, stdout=self.log, stderr=subprocess.STDOUT)

    def _argv(self, variable):
        return [part.replace("{port}", str(self.port)).replace("{env_file}", self.env_file.name)
                for part in shlex.split(os.environ[variable])]

    def wait_ready(self):
        """True once GET /health answers 200; False when the process exits first."""
        deadline = time.time() + float(os.environ.get("GATEWAY_READY_SECONDS", "120"))
        while time.time() < deadline:
            if self.process.poll() is not None:
                return False
            try:
                if request(self.base, "GET", "/health", timeout=2).status == 200:
                    return True
            except OSError:
                pass
            time.sleep(0.2)
        raise RuntimeError("the gateway did not answer GET /health in time:\n" + self.output())

    def output(self):
        self.log.seek(0)
        return self.log.read().decode(errors="replace")

    def stop(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(10)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
        if os.environ.get("GATEWAY_STOP"):
            subprocess.run(self._argv("GATEWAY_STOP"), capture_output=True)
        os.unlink(self.env_file.name)


class World:
    """One deploy, one mock provider and one gateway shared by every suite;
    suites that need another configuration start their own instance."""

    def __init__(self):
        self.admin_conn = os.environ["PGMI_TEST_CONN"]
        self.idp = MockProvider(free_port())
        self.deploy()
        self.admin = make_conninfo(self.admin_conn, dbname=DATABASE)
        server = conninfo_to_dict(self.admin_conn)
        self.database_url = (f"postgresql://{GATEWAY_ROLE}:gateway@{server.get('host', '127.0.0.1')}:"
                             f"{server.get('port', '5432')}/{DATABASE}")
        self.instances = []
        self.gateway = self.start_gateway()
        self.base = self.gateway.base

    def deploy(self):
        project = pathlib.Path(tempfile.mkdtemp()) / "project"
        pgmi = os.environ["PGMI_BIN"]
        subprocess.run([pgmi, "init", str(project), "-t", "advanced"], check=True, capture_output=True)
        params = project.parent / "params.env"
        params.write_text("\n".join([
            "env=dev",
            "oidc_issuers=" + json.dumps([self.idp.issuer]),
            "platform_operators=" + json.dumps([{"email": OPERATOR_EMAIL, "issuer": self.idp.issuer,
                                                 "subject": OPERATOR_SUBJECT}]),
        ]) + "\n")
        deployed = subprocess.run(
            [pgmi, "deploy", str(project), "-d", DATABASE, "--overwrite", "--force", "--params-file", str(params)],
            env={**os.environ, "PGMI_CONNECTION_STRING": self.admin_conn}, capture_output=True, text=True)
        if deployed.returncode != 0:
            raise RuntimeError("deploy failed:\n" + deployed.stdout + deployed.stderr)
        with psycopg.connect(make_conninfo(self.admin_conn, dbname=DATABASE), autocommit=True) as conn:
            conn.execute(f"DROP ROLE IF EXISTS {GATEWAY_ROLE}")
            conn.execute(f"CREATE ROLE {GATEWAY_ROLE} LOGIN PASSWORD 'gateway' IN ROLE {DATABASE}_api")
            conn.execute(FIXTURE_SQL)

    def gateway_env(self, issuer=None, **overrides):
        port = free_port()
        base = f"http://127.0.0.1:{port}"
        env = {"DATABASE_URL": self.database_url, "HOST": "127.0.0.1", "PORT": str(port),
               "ALLOWED_ORIGINS": base, "DEV_LOGIN": "1", "LOGIN_CSRF_SECRET": "csrf-secret-for-tests",
               "POOL_MAX_SIZE": "10", "RETRY_MAX_ATTEMPTS": str(RETRY_ATTEMPTS), "RETRY_BASE_DELAY": "0.01",
               "OIDC_ISSUER": issuer or self.idp.issuer, "OIDC_CLIENT_ID": CLIENT_ID,
               "OIDC_CLIENT_SECRET": CLIENT_SECRET, "OIDC_REDIRECT_URI": base + "/auth/callback"}
        env.update({k: v.replace("{base}", base) for k, v in overrides.items()})
        return env

    def start_gateway(self, issuer=None, must_start=True, **overrides):
        """A ready gateway, or None when it exits at startup and must_start is False.
        {base} in an override becomes the gateway's own http://127.0.0.1:<port>."""
        gateway = Gateway(self.gateway_env(issuer, **overrides))
        self.instances.append(gateway)
        if gateway.wait_ready():
            return gateway
        if must_start:
            raise RuntimeError("the gateway exited at startup:\n" + gateway.output())
        return None

    def scalar(self, sql, params=None):
        with psycopg.connect(self.admin, autocommit=True) as conn:
            return conn.execute(sql, params).fetchone()[0]

    def close(self):
        for gateway in self.instances:
            gateway.stop()
        self.idp.stop()


_world = None


def world():
    global _world
    if _world is None:
        _world = World()
    return _world


def close_world():
    if _world is not None:
        _world.close()
