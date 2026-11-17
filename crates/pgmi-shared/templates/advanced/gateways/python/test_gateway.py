"""
Tests of gateway.py against a deployed database.

Demo code from pgmi. Do not copy this file into the project as is. Read it,
then write your own gateway that fits this project. Namespaces, modules and
package names are examples: rename them to match the project and add its
specifics freely.

Needs an env=dev deploy (for api.open_dev_login_session) and a login in the
api role:

    DATABASE_URL=postgresql://gateway:secret@127.0.0.1:5432/mydb python -m unittest test_gateway

Without DATABASE_URL the database tests are skipped; the tests of the two
extension seams (channel.py, interceptors.py), of the resource server and
rate limits (resource_server.py) and of the pending sign-ins and the sign-in
rate limit need no database. The MCP test calls echo_note, the example tool in
api/examples.sql.
"""

import collections
import http.client
import json
import os
import pathlib
import secrets
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from http.cookies import SimpleCookie
from types import MappingProxyType
from unittest import mock

import jwt
import psycopg
from cryptography.hazmat.primitives.asymmetric import ec, rsa

import channel
import gateway
import interceptors
import login_transport
import resource_server

HERE = pathlib.Path(__file__).resolve().parent
APPLICATION_NAME = "pgmi-gateway-python"  # ADAPT: as in gateway.py
POOL_MAX_SIZE = 10
MCP_VERSION = "2026-07-28"
MCP_META = {"io.modelcontextprotocol/protocolVersion": MCP_VERSION, "io.modelcontextprotocol/clientCapabilities": {}}


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

    def json(self):
        return json.loads(self.body)


class GatewayTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not os.environ.get("DATABASE_URL"):
            raise unittest.SkipTest("DATABASE_URL is not set")
        cls.port = free_port()
        env = {**os.environ, "HOST": "127.0.0.1", "PORT": str(cls.port), "DEV_LOGIN": "1",
               "POOL_MAX_SIZE": str(POOL_MAX_SIZE), "PYTHONDONTWRITEBYTECODE": "1", "PYTHONUNBUFFERED": "1"}
        for name in ("OIDC_ISSUER", "ALLOWED_ORIGINS"):
            env.pop(name, None)
        cls.log = tempfile.TemporaryFile()
        cls.gateway = subprocess.Popen([sys.executable, str(HERE / "gateway.py")], env=env,
                                       stdout=cls.log, stderr=subprocess.STDOUT)
        deadline = time.time() + 20
        while time.time() < deadline:
            try:
                socket.create_connection(("127.0.0.1", cls.port), timeout=1).close()
                break
            except OSError:
                if cls.gateway.poll() is not None:
                    break
                time.sleep(0.1)
        else:
            raise RuntimeError("gateway did not start")
        if cls.gateway.poll() is not None:
            cls.log.seek(0)
            raise RuntimeError("gateway exited:\n" + cls.log.read().decode(errors="replace"))
        cls.session, cls.csrf = cls.sign_in("client-python")

    @classmethod
    def tearDownClass(cls):
        cls.gateway.terminate()
        cls.gateway.wait()

    @classmethod
    def call(cls, method, path, body=None, headers=None, session=None):
        conn = http.client.HTTPConnection("127.0.0.1", cls.port, timeout=30)
        data = json.dumps(body).encode() if body is not None else None
        all_headers = {"Accept": "application/json", **(headers or {})}
        if data is not None:
            all_headers["Content-Type"] = "application/json"
        if session:
            all_headers["Cookie"] = f"__Host-session={session}"
        conn.request(method, path, body=data, headers=all_headers)
        raw = conn.getresponse()
        response = Response(raw.status, raw.getheaders(), raw.read())
        conn.close()
        return response

    @classmethod
    def sign_in(cls, subject, path="/auth/dev-login"):
        """Development sign-in. In production the transport runs OIDC and
        calls api.open_login_session instead (login_transport.py)."""
        response = cls.call("POST", path,
                            {"subject": subject, "email": f"{subject}@example.com", "displayName": subject})
        if response.status != 204:
            raise RuntimeError(f"dev sign-in: {response.status} {response.body!r}")
        jar = SimpleCookie()
        for name, value in response.headers:
            if name.lower() == "set-cookie":
                jar.load(value)
        return jar["__Host-session"].value, jar["__Host-csrf"].value

    def post(self, path, body, headers=None):
        return self.call("POST", path, body, {"X-CSRF-Token": self.csrf, **(headers or {})}, session=self.session)

    def organization(self):
        created = self.post("/api/organizations", {"name": "Python client", "slug": "py-" + secrets.token_hex(4)})
        self.assertEqual(created.status, 201, created.body)
        return created.json()["id"]

    def assert_problem(self, response, status, code):
        self.assertEqual(response.status, status, response.body)
        self.assertTrue(response.header("Content-Type").startswith("application/problem+json"))
        self.assertEqual(response.json().get("code"), code, response.body)

    def mcp(self, method, params, connection, bearer=False):
        headers = {"MCP-Protocol-Version": MCP_VERSION, "Mcp-Method": method}
        headers.update({"Authorization": "Bearer " + connection} if bearer else {"X-Connection": connection})
        if "name" in params:
            headers["Mcp-Name"] = params["name"]
        body = {"jsonrpc": "2.0", "id": 1, "method": method, "params": {**params, "_meta": MCP_META}}
        return self.call("POST", "/mcp", body, headers)

    def test_openapi_lists_what_the_caller_may_call(self):
        anonymous = self.call("GET", "/api/openapi.json")
        self.assertEqual(anonymous.status, 200, anonymous.body)
        self.assertIn("/hello", anonymous.json()["paths"])
        self.assertNotIn("/me", anonymous.json()["paths"])

        signed_in = self.call("GET", "/api/openapi.json", session=self.session)
        self.assertIn("/me", signed_in.json()["paths"])

    def test_public_route(self):
        response = self.call("GET", "/api/hello?name=Python")
        self.assertEqual(response.status, 200, response.body)
        self.assertEqual(response.json()["message"], "Hello, Python!")

    def test_authenticated_route(self):
        self.assertEqual(self.call("GET", "/api/me").status, 401)
        response = self.call("GET", "/api/me", session=self.session)
        self.assertEqual(response.status, 200, response.body)

    def test_a_client_sent_session_header_is_dropped(self):
        response = self.call("GET", "/api/me", headers={"X-Login-Session": self.session})
        self.assertEqual(response.status, 401, response.body)

    def test_coded_problems(self):
        no_csrf = self.call("POST", "/api/connections", {"clientName": "x"}, session=self.session)
        self.assert_problem(no_csrf, 403, "pgmi.csrf_refused")
        self.assert_problem(self.post("/api/connections", {"clientName": 5}), 400, "pgmi.invalid_body")
        stranger = self.post("/api/connections", {"clientName": "x", "organizationId": "00000000-0000-4000-8000-000000000000",
                                                   "roleId": "agent.reader"})
        self.assert_problem(stranger, 422, "membership.connection.not_member")
        too_long = self.post("/api/connections", {"clientName": "x", "validDays": 999,
                                                  "organizationId": self.organization(), "roleId": "agent.reader"})
        self.assert_problem(too_long, 400, "pgmi.invalid_body")

    def test_mcp_through_an_approved_connection(self):
        approved = self.post("/api/connections", {"clientName": "Python test", "validDays": 1,
                                                  "organizationId": self.organization(), "roleId": "agent.reader"})
        self.assertEqual(approved.status, 201, approved.body)
        connection = approved.json()["connection"]

        listed = self.mcp("tools/list", {}, connection)
        self.assertEqual(listed.status, 200, listed.body)
        self.assertIn("echo_note", [t["name"] for t in listed.json()["result"]["tools"]])

        called = self.mcp("tools/call", {"name": "echo_note", "arguments": {"note": "hi"}}, connection)
        self.assertEqual(called.status, 200, called.body)
        self.assertEqual(called.json()["result"]["structuredContent"]["note"], "hi")

        self.assertEqual(self.mcp("tools/list", {}, connection, bearer=True).status, 200)
        for bearer in (False, True):
            refused = self.mcp("tools/call", {"name": "echo_note", "arguments": {}}, "cn_unknown", bearer)
            self.assertEqual(refused.status, 401, refused.body)
            self.assertEqual(refused.header("WWW-Authenticate"), 'Bearer error="invalid_token"')

    def test_a_query_string_leaves_dev_login_and_logout_as_they_are(self):
        session, csrf = self.sign_in("client-python-logout", "/auth/dev-login?x=1")
        self.assertEqual(self.call("GET", "/api/me", session=session).status, 200)

        response = self.call("POST", "/auth/logout?x=1", headers={"X-CSRF-Token": csrf}, session=session)
        self.assertEqual(response.status, 204, response.body)
        jar = SimpleCookie()
        for name, value in response.headers:
            if name.lower() == "set-cookie":
                jar.load(value)
        for name in (login_transport.SESSION_COOKIE, login_transport.CSRF_COOKIE):
            self.assertEqual((jar[name].value, jar[name]["max-age"]), ("", "0"), name)
        self.assertEqual(self.call("GET", "/api/me", session=session).status, 401)

    def test_application_name_and_a_bounded_pool(self):
        self.call("GET", "/api/hello")
        with psycopg.connect(os.environ["DATABASE_URL"]) as conn:
            count = conn.execute("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()"
                                 " AND application_name = %s", [APPLICATION_NAME]).fetchone()[0]
        self.assertGreaterEqual(count, 1)
        self.assertLessEqual(count, POOL_MAX_SIZE)



# -- The extension seams, without a database ---------------------------------

EMBED = {"text": {"x-embed": True}}


def context(protocol="rest", annotations=None, deadline=30.0):
    return channel.RequestContext(request_id="rid-1", protocol=protocol, method="POST", url="/notes",
                                  deadline=time.monotonic() + deadline,
                                  annotations=MappingProxyType(annotations or {}))


class Recorder(interceptors.RequestInterceptor):
    """Records what it was handed; optionally changes it or fails."""

    def __init__(self, name="recorder", needs_json=False, failure=interceptors.FAIL, timeout=2.0,
                 change=None, applies=True):
        self.name, self.needs_json, self.failure, self.timeout = name, needs_json, failure, timeout
        self._change, self._applies = change, applies

    def applies(self, ctx):
        return self._applies

    def on_request(self, request, ctx):
        if self._change:
            self._change(request, ctx)


def boom(request, ctx):
    request.headers["x-half-done"] = "1"
    if isinstance(request.json, dict):
        request.json["half"] = True
    raise RuntimeError("upstream down")


class PipelineTest(unittest.TestCase):
    def run_requests(self, pipeline, request, ctx):
        pipeline.run_requests(pipeline.requests_for(ctx), request, ctx)

    def test_no_interceptor_applies_and_nothing_is_selected(self):
        pipeline = interceptors.Pipeline([interceptors.EmbedFields(interceptors.FakeEmbedder())],
                                         [interceptors.RequestIdHeader()])
        self.assertEqual(pipeline.requests_for(context()), ())
        self.assertEqual(len(pipeline.responses_for(context())), 1)
        self.assertIsNone(interceptors.Pipeline()._executor)

    def test_an_interceptor_that_needs_no_json_leaves_the_bytes_alone(self):
        raw = b'{ "b" : 1,\n  "a":"\\u00e9" }'
        request = interceptors.Request("POST", "/notes", {}, raw)
        pipeline = interceptors.Pipeline([Recorder(change=lambda r, c: r.headers.update(x="1"))])
        with mock.patch.object(interceptors.json, "loads", side_effect=AssertionError("parsed")):
            self.run_requests(pipeline, request, context())
        self.assertIs(request.body, raw)
        self.assertEqual(request.headers, {"x": "1"})

    def test_json_is_parsed_once_and_serialized_once(self):
        def add(key):
            return lambda r, c: r.json.__setitem__(key, True)
        pipeline = interceptors.Pipeline([Recorder("one", True, change=add("one")),
                                          Recorder("two", True, change=add("two"))])
        request = interceptors.Request("POST", "/notes", {}, b'{"a": 1}')
        loads, dumps = mock.Mock(wraps=json.loads), mock.Mock(wraps=json.dumps)
        with mock.patch.object(interceptors.json, "loads", loads), \
                mock.patch.object(interceptors.json, "dumps", dumps):
            self.run_requests(pipeline, request, context())
        self.assertEqual((loads.call_count, dumps.call_count), (1, 1))
        self.assertEqual(json.loads(request.body), {"a": 1, "one": True, "two": True})

    def test_a_body_that_is_not_json_passes_as_it_was(self):
        request = interceptors.Request("POST", "/notes", {}, b"not json")
        self.run_requests(interceptors.Pipeline([Recorder(needs_json=True)]), request, context())
        self.assertEqual(request.body, b"not json")

    def test_fail_stops_the_request(self):
        log = []
        pipeline = interceptors.Pipeline([Recorder("broken", True, change=boom)], log=log.append)
        with self.assertRaises(interceptors.InterceptorFailed) as raised:
            self.run_requests(pipeline, interceptors.Request("POST", "/notes", {}, b"{}"), context())
        self.assertEqual(raised.exception.name, "broken")
        self.assertIn("rid-1", log[0])
        self.assertIn("upstream down", log[0])

    def test_degrade_goes_on_with_the_request_as_it_was(self):
        log = []
        after = Recorder("after", True, change=lambda r, c: r.json.__setitem__("after", True))
        pipeline = interceptors.Pipeline([Recorder("broken", True, interceptors.DEGRADE, change=boom), after],
                                         log=log.append)
        request = interceptors.Request("POST", "/notes", {"h": "v"}, b'{"a": 1}')
        self.run_requests(pipeline, request, context())
        self.assertEqual(json.loads(request.body), {"a": 1, "after": True})
        self.assertEqual(request.headers, {"h": "v"})
        self.assertIn("degraded", log[0])

    def test_a_timeout_is_a_failure_and_a_late_change_is_never_read(self):
        release = threading.Event()

        def slow(request, ctx):
            release.wait(5)
            request.json["late"] = True
            request.headers["late"] = "1"

        for failure in (interceptors.FAIL, interceptors.DEGRADE):
            with self.subTest(failure=failure):
                release.clear()
                pipeline = interceptors.Pipeline([Recorder("slow", True, failure, timeout=0.1, change=slow)],
                                                 log=lambda line: None)
                request = interceptors.Request("POST", "/notes", {}, b'{"a": 1}')
                started = time.monotonic()
                if failure == interceptors.FAIL:
                    with self.assertRaises(interceptors.InterceptorFailed):
                        self.run_requests(pipeline, request, context())
                else:
                    self.run_requests(pipeline, request, context())
                    release.set()
                    time.sleep(0.1)
                    self.assertEqual(json.loads(request.body), {"a": 1})
                    self.assertEqual(request.headers, {})
                self.assertLess(time.monotonic() - started, 1.0)
                release.set()

    def test_the_request_deadline_bounds_every_interceptor(self):
        pipeline = interceptors.Pipeline([Recorder("slow", timeout=10, change=lambda r, c: time.sleep(2))],
                                         log=lambda line: None)
        started = time.monotonic()
        with self.assertRaises(interceptors.InterceptorFailed):
            self.run_requests(pipeline, interceptors.Request("GET", "/", {}, None), context(deadline=0.2))
        self.assertLess(time.monotonic() - started, 1.0)

    def test_concurrent_requests_never_see_each_other(self):
        pipeline = interceptors.Pipeline([interceptors.EmbedFields(interceptors.FakeEmbedder())])
        embedder = interceptors.FakeEmbedder()

        def one(i):
            request = interceptors.Request("POST", "/notes", {}, json.dumps({"text": "note %d" % i}).encode())
            self.run_requests(pipeline, request, context(annotations=EMBED))
            return i, json.loads(request.body)

        with ThreadPoolExecutor(16) as pool:
            for i, body in pool.map(one, range(400)):
                self.assertEqual(body, {"text": "note %d" % i,
                                        "text_embedding": embedder.embed(["note %d" % i], 1)[0]})


class EmbedFieldsTest(unittest.TestCase):
    def setUp(self):
        self.pipeline = interceptors.Pipeline([interceptors.EmbedFields(interceptors.FakeEmbedder())])

    def run_requests(self, request, ctx):
        self.pipeline.run_requests(self.pipeline.requests_for(ctx), request, ctx)

    def test_selected_by_the_annotation_only(self):
        self.assertEqual(self.pipeline.requests_for(context(annotations={"text": {"x-embed": False}})), ())
        self.assertEqual(len(self.pipeline.requests_for(context(annotations=EMBED))), 1)

    def test_rest_body(self):
        request = interceptors.Request("POST", "/notes", {}, b'{"text": "hello", "other": "x"}')
        self.run_requests(request, context(annotations={**EMBED, "other": {"x-label": "y"}}))
        body = json.loads(request.body)
        self.assertEqual(len(body["text_embedding"]), interceptors.FakeEmbedder.DIMENSIONS)
        self.assertNotIn("other_embedding", body)

    def test_mcp_arguments(self):
        message = {"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                   "params": {"name": "t", "arguments": {"text": "hello"}}}
        request = interceptors.Request("POST", "/mcp", {}, None, parsed=message)
        self.run_requests(request, context("mcp", EMBED))
        self.assertIn("text_embedding", message["params"]["arguments"])

    def test_a_vector_the_client_sent_is_kept(self):
        request = interceptors.Request("POST", "/notes", {}, b'{"text": "hello", "text_embedding": [1]}')
        self.run_requests(request, context(annotations=EMBED))
        self.assertEqual(json.loads(request.body)["text_embedding"], [1])

    def test_the_fake_refuses_a_text_over_its_limit(self):
        long_text = "x" * (interceptors.FakeEmbedder.MAX_CHARS + 1)
        request = interceptors.Request("POST", "/notes", {}, json.dumps({"text": long_text}).encode())
        self.pipeline._log = lambda line: None
        with self.assertRaises(interceptors.InterceptorFailed):
            self.run_requests(request, context(annotations=EMBED))


class HeaderNamesTest(unittest.TestCase):
    CREDENTIALS = [("Authorization", "Bearer s1"), ("Cookie", "a=s2"), ("cookie", "b=s3"),
                   ("X-Connection", "cn_s4"), ("X-Login-Session", "ls_s5"), ("X-CSRF-Token", "s6"),
                   ("Accept", "a/b"), ("ACCEPT", "c/d")]

    def setUp(self):
        self.pipeline = interceptors.Pipeline([interceptors.HeaderNames()])

    def run_requests(self, request, ctx):
        self.pipeline.run_requests(self.pipeline.requests_for(ctx), request, ctx)

    def test_incoming_headers_keep_credentials_and_join_repeats(self):
        headers = login_transport.incoming_headers(self.CREDENTIALS)
        self.assertEqual(headers["cookie"], "a=s2; b=s3")
        self.assertEqual(headers["accept"], "a/b, c/d")
        self.assertEqual(headers["x-login-session"], "ls_s5")
        forwarded = login_transport.forwarded_headers(self.CREDENTIALS, "ls_real")
        self.assertEqual(forwarded["x-login-session"], "ls_real")
        self.assertFalse({"cookie", "x-connection", "x-csrf-token"} & forwarded.keys())

    def test_rest_body_gains_the_names_never_the_values(self):
        headers = login_transport.incoming_headers(self.CREDENTIALS)
        request = interceptors.Request("POST", "/notes", headers, b'{"text": "a"}')
        self.run_requests(request, context())
        self.assertEqual(json.loads(request.body)["headerNames"], sorted(headers))
        for secret in (b"s1", b"s2", b"s4", b"s5", b"s6"):
            self.assertNotIn(secret, request.body)

    def test_mcp_arguments(self):
        message = {"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                   "params": {"name": "t", "arguments": {"text": "a"}}}
        request = interceptors.Request("POST", "/mcp", {"authorization": "Bearer s1"}, None, parsed=message)
        self.run_requests(request, context("mcp"))
        self.assertEqual(message["params"]["arguments"]["headerNames"], ["authorization"])

    def test_no_arguments_object_is_left_alone(self):
        message = {"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
        request = interceptors.Request("POST", "/mcp", {"x": "1"}, None, parsed=message)
        self.run_requests(request, context("mcp"))
        self.assertEqual(message, {"jsonrpc": "2.0", "id": 1, "method": "tools/list"})


class FakeChannel(channel.Channel):
    """Fails the first `failures` calls of each kind with `error`."""

    def __init__(self, error=None, failures=0):
        self.error, self.failures, self.calls = error, failures, []

    def _call(self, op, result):
        self.calls.append(op)
        if self.calls.count(op) <= self.failures:
            raise self.error
        return result

    def rest_policy(self, ctx, requested):
        return self._call("rest.policy", channel.Policy(handler="api.notes"))

    def rest_invoke(self, ctx, policy, headers, body):
        return self._call("rest.invoke", channel.RestResult(200, {}, b"{}"))

    def mcp_policy(self, ctx, request, requested, mcp_headers):
        return self._call("mcp.policy", channel.Policy())

    def mcp_request(self, ctx, policy, request, context):
        return self._call("mcp.request", {"jsonrpc": "2.0", "id": 1, "result": {}})


LOST = psycopg.OperationalError("server closed the connection unexpectedly")


class ChannelTest(unittest.TestCase):
    def retrying(self, inner):
        return channel.ConnectionRetry(inner, attempts=3, base_delay=0, log=lambda line: None)

    def test_statement_timeout_rounds_up_so_the_server_never_fires_first(self):
        class Left:
            def __init__(self, seconds):
                self.seconds = seconds

            def remaining(self):
                return self.seconds
        self.assertEqual(channel._deadline_milliseconds(Left(1.9995)), "2000")
        self.assertEqual(channel._deadline_milliseconds(Left(0.0004)), "1")
        with self.assertRaises(channel.DeadlineExceeded):
            channel._deadline_milliseconds(Left(0))

    def test_a_lost_connection_retries_the_policy_lookups(self):
        inner = FakeChannel(LOST, failures=2)
        outer = self.retrying(inner)
        self.assertEqual(outer.rest_policy(context(), None).handler, "api.notes")
        outer.mcp_policy(context(), {}, None, (None, None, None))
        self.assertEqual(inner.calls.count("rest.policy"), 3)

    def test_a_lost_connection_retries_a_read_only_dispatch(self):
        inner = FakeChannel(LOST, failures=1)
        self.retrying(inner).rest_invoke(context(), channel.Policy(read_only=True), {}, None)
        self.assertEqual(inner.calls, ["rest.invoke", "rest.invoke"])

    def test_a_write_is_never_sent_twice(self):
        for call in (lambda c: c.rest_invoke(context(), channel.Policy(read_only=False), {}, b"{}"),
                     lambda c: c.mcp_request(context(), channel.Policy(), {}, None)):
            inner = FakeChannel(LOST, failures=1)
            with self.assertRaises(psycopg.OperationalError):
                call(self.retrying(inner))
            self.assertEqual(len(inner.calls), 1)

    def test_an_answer_from_the_server_is_not_a_lost_connection(self):
        inner = FakeChannel(psycopg.errors.SerializationFailure("conflict"), failures=1)
        with self.assertRaises(psycopg.errors.SerializationFailure):
            self.retrying(inner).rest_policy(context(), None)
        self.assertEqual(len(inner.calls), 1)

    def test_an_exhausted_pool_is_not_waited_out_twice(self):
        inner = FakeChannel(channel.PoolTimeout("no connection"), failures=1)
        with self.assertRaises(channel.PoolTimeout):
            self.retrying(inner).rest_policy(context(), None)
        self.assertEqual(len(inner.calls), 1)

    def test_serialization_failures_rerun_the_whole_transaction(self):
        inner = FakeChannel(psycopg.errors.SerializationFailure("conflict"), failures=2)
        outer = channel.SerializationRetry(inner, attempts=3, base_delay=0)
        outer.mcp_request(context(), channel.Policy(), {}, None)
        self.assertEqual(inner.calls.count("mcp.request"), 3)

        inner = FakeChannel(psycopg.errors.DeadlockDetected("deadlock"), failures=3)
        with self.assertRaises(channel.RetriesExhausted) as raised:
            channel.SerializationRetry(inner, attempts=3, base_delay=0).rest_invoke(
                context(), channel.Policy(), {}, b"{}")
        self.assertEqual((raised.exception.sqlstate, raised.exception.attempts), ("40P01", 3))

    def test_tracing_logs_each_call_with_the_request_id_and_no_body(self):
        log = []
        traced = channel.TracingChannel(FakeChannel(), log=log.append)
        traced.rest_invoke(context(), channel.Policy(), {"x-login-session": "secret"}, b'{"secret": 1}')
        self.assertEqual(len(log), 1)
        self.assertIn("rid-1 rest.invoke POST /notes status=200", log[0])
        self.assertNotIn("secret", log[0])

        failing = channel.TracingChannel(FakeChannel(LOST, failures=1), log=log.append)
        with self.assertRaises(psycopg.OperationalError):
            failing.mcp_request(context(), channel.Policy(), {}, None)
        self.assertIn("error=OperationalError", log[1])

    def test_the_decorators_compose(self):
        inner = FakeChannel(LOST, failures=1)
        log = []
        composed = channel.ConnectionRetry(
            channel.SerializationRetry(channel.TracingChannel(inner, log=log.append), 3, 0),
            attempts=2, base_delay=0, log=log.append)
        composed.rest_policy(context(), None)
        self.assertEqual(len([line for line in log if line.startswith("[trace]")]), 2)


RESOURCE_URI = "https://mcp.example.com/mcp"
ISSUER = "https://idp.example.com"
LOGIN_CLIENT = "web-login"


class FakeKeys:
    """The issuer's key set: one key, or an error; counts every lookup."""

    def __init__(self, key, error=None):
        self.key, self.error, self.lookups = key, error, 0

    def get_signing_key(self, kid):
        self.lookups += 1
        if self.error is not None:
            raise self.error
        if kid != "k1":
            raise jwt.PyJWKClientError("no such key")
        return self.key


class ResourceServerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.private = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        cls.public = jwt.PyJWK(json.loads(jwt.algorithms.RSAAlgorithm.to_jwk(cls.private.public_key())), "RS256")

    def setUp(self):
        self.server = resource_server.ResourceServer(RESOURCE_URI, [ISSUER], ["openid"], LOGIN_CLIENT)
        self.keys = FakeKeys(self.public)
        self.server._issuers[ISSUER]._keys = self.keys

    def token(self, kid="k1", **claims):
        now = int(time.time())
        body = {"iss": ISSUER, "sub": "person-1", "aud": RESOURCE_URI, "client_id": "desktop-agent",
                "iat": now, "exp": now + 300}
        body.update(claims)
        body = {k: v for k, v in body.items() if v is not None}
        return "Bearer " + jwt.encode(body, self.private, algorithm="RS256", headers={"kid": kid})

    def assert_rejected(self, authorization, error="invalid_token", fetches=None):
        with self.assertRaises(resource_server.TokenRejected) as raised:
            self.server.verify(authorization)
        self.assertEqual(raised.exception.error, error)
        if fetches is not None:
            self.assertEqual(self.keys.lookups, fetches)

    def test_metadata_and_challenge(self):
        self.assertEqual(self.server.metadata(), {
            "resource": RESOURCE_URI, "authorization_servers": [ISSUER],
            "bearer_methods_supported": ["header"], "scopes_supported": ["openid"]})
        self.assertEqual(self.server.metadata_url, "https://mcp.example.com/.well-known/oauth-protected-resource/mcp")
        self.assertEqual(self.server.metadata_paths, {"/.well-known/oauth-protected-resource",
                                                      "/.well-known/oauth-protected-resource/mcp"})
        self.assertEqual(self.server.challenge(),
                         'Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp", '
                         'scope="openid"')
        self.assertTrue(self.server.challenge("invalid_token").endswith(', error="invalid_token"'))

    def test_configuration_is_checked(self):
        for resource, servers in [("http://mcp.example.com/mcp", [ISSUER]), (RESOURCE_URI + "?x=1", [ISSUER]),
                                  (RESOURCE_URI + "#f", [ISSUER]), (RESOURCE_URI, []),
                                  (RESOURCE_URI, ["http://idp.example.com"])]:
            with self.subTest(resource=resource, servers=servers), self.assertRaises(ValueError):
                resource_server.ResourceServer(resource, servers, [])
        resource_server.ResourceServer("http://127.0.0.1:8080/mcp", ["http://localhost:9000"], [])

    def test_a_valid_token_proves_issuer_subject_and_client(self):
        identity = self.server.verify(self.token())
        self.assertEqual(identity.context(), {"client": {"issuer": ISSUER, "subject": "person-1",
                                                         "clientId": "desktop-agent"}})
        self.assertEqual(self.server.verify(self.token(client_id=None, azp="other-agent")).client_id, "other-agent")
        self.assertEqual(self.server.verify(self.token(aud=[RESOURCE_URI])).subject, "person-1")
        self.assertEqual(self.server.verify(self.token(aud="HTTPS://MCP.EXAMPLE.COM/mcp")).subject, "person-1")

    def test_no_token_is_401_without_an_error_code(self):
        self.assert_rejected(None, error=None)

    def test_the_token_alg_verifies_against_an_alg_less_key_of_its_type(self):
        self.keys.key = jwt.PyJWK(json.loads(jwt.algorithms.RSAAlgorithm.to_jwk(self.private.public_key())))
        now = int(time.time())
        body = {"iss": ISSUER, "sub": "person-1", "aud": RESOURCE_URI, "client_id": "desktop-agent",
                "iat": now, "exp": now + 300}
        ps256 = jwt.encode(body, self.private, algorithm="PS256", headers={"kid": "k1"})
        self.assertEqual(self.server.verify("Bearer " + ps256).subject, "person-1")
        p256 = ec.generate_private_key(ec.SECP256R1())
        self.keys.key = jwt.PyJWK(json.loads(jwt.algorithms.ECAlgorithm.to_jwk(p256.public_key())))
        self.assert_rejected("Bearer " + ps256)
        self.assertEqual(self.server.verify(
            "Bearer " + jwt.encode(body, p256, algorithm="ES256", headers={"kid": "k1"})).subject, "person-1")

    def test_cheap_refusals_never_fetch_a_key(self):
        for authorization in ["Basic abc", "Bearer", "Bearer not-a-jwt", "Bearer " + "a" * 9000,
                              self.token(iss="https://evil.example.com"), self.token(aud="https://other.example.com/mcp"),
                              self.token(aud=[RESOURCE_URI, "https://other.example.com"])]:
            with self.subTest(authorization=authorization[:40]):
                self.assert_rejected(authorization, fetches=0)

    def test_signature_and_claims(self):
        now = int(time.time())
        forged = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        cases = {
            "forged": "Bearer " + jwt.encode({"iss": ISSUER, "sub": "s", "aud": RESOURCE_URI, "client_id": "c",
                                              "iat": now, "exp": now + 60}, forged, algorithm="RS256",
                                             headers={"kid": "k1"}),
            "expired": self.token(iat=now - 600, exp=now - 120),
            "not yet valid": self.token(nbf=now + 600),
            "issued in the future": self.token(iat=now + 600, exp=now + 900),
            "no exp": self.token(exp=None),
            "no sub": self.token(sub=None),
            "no client id": self.token(client_id=None),
            "the web login's client": self.token(client_id=LOGIN_CLIENT),
            "unknown key": self.token(kid="k9"),
        }
        for label, authorization in cases.items():
            with self.subTest(label):
                self.assert_rejected(authorization)

    def test_an_unreachable_provider_is_not_a_bad_token(self):
        self.keys.error = jwt.PyJWKClientConnectionError("down")
        with self.assertRaises(resource_server.ProviderUnavailable):
            self.server.verify(self.token())


class RateLimiterTest(unittest.TestCase):
    def test_a_burst_then_retry_after_then_refill(self):
        now = [0.0]
        limiter = resource_server.RateLimiter(60, clock=lambda: now[0])
        self.assertTrue(all(limiter.allow("a")[0] for _ in range(60)))
        self.assertEqual(limiter.allow("a"), (False, 1))
        self.assertTrue(limiter.allow("b")[0], "keys are counted apart")
        now[0] += 1.0
        self.assertEqual(limiter.allow("a"), (True, 0))
        self.assertFalse(limiter.allow("a")[0])

    def test_zero_is_off(self):
        limiter = resource_server.RateLimiter(0)
        self.assertTrue(all(limiter.allow("a")[0] for _ in range(1000)))

    def test_memory_is_bounded(self):
        limiter = resource_server.RateLimiter(10, max_keys=100)
        for i in range(1000):
            limiter.allow(i)
        self.assertEqual(len(limiter._buckets), 100)

    def test_concurrent_requests_never_exceed_the_limit(self):
        class SlowBuckets(collections.OrderedDict):
            def pop(self, *args):
                bucket = super().pop(*args)
                time.sleep(0.0005)
                return bucket

        limiter = resource_server.RateLimiter(200, clock=lambda: 0.0)
        limiter._buckets = SlowBuckets()
        with ThreadPoolExecutor(16) as pool:
            results = list(pool.map(lambda _: limiter.allow("shared")[0], range(1000)))
        self.assertEqual(sum(results), 200)



class PendingLoginsTest(unittest.TestCase):
    def test_expired_sign_ins_go_on_the_next_put_and_the_oldest_at_the_limit(self):
        now = [0.0]
        pending = login_transport.PendingLogins(ttl=10, limit=3, clock=lambda: now[0])
        for _ in range(3):
            pending.put({})
        now[0] += 11
        kept = pending.put({"state": "fresh"})
        self.assertEqual(len(pending._items), 1, "expired sign-ins must go on the next put")
        for _ in range(3):
            pending.put({})
        self.assertIsNone(pending.take(kept), "at the limit the oldest goes first")
        self.assertEqual(len(pending._items), 3)

    def test_usable_once_and_not_after_the_ttl(self):
        now = [0.0]
        pending = login_transport.PendingLogins(ttl=10, clock=lambda: now[0])
        key = pending.put({"state": "s"})
        self.assertEqual(pending.take(key), {"state": "s"})
        self.assertIsNone(pending.take(key))
        late = pending.put({})
        now[0] += 11
        self.assertIsNone(pending.take(late))


class FakeLogin:
    provider = object()

    def begin(self, return_to, cookies):
        return "https://idp.example/authorize", []

    def complete(self, cookies, query, connect):
        raise login_transport.LoginFailed(400, "pgmi.login.state_invalid", "no such sign-in")


class ReturnToTest(unittest.TestCase):
    def test_return_to_stays_on_this_site(self):
        for hostile in (None, "", "app", "//evil.example/x", "/\\evil.example", "https://evil.example/",
                        "/\r\nSet-Cookie: x=1", "/\tx", "/caf\u20ac", "/a b"):
            with self.subTest(returnTo=hostile):
                self.assertEqual(login_transport.safe_return_to(hostile), "/api/docs")
        self.assertEqual(login_transport.safe_return_to("/app?tab=2"), "/app?tab=2")


class SignInRateLimitTest(unittest.TestCase):
    def setUp(self):
        for name, value in (("LOGIN", FakeLogin()), ("PER_IP", resource_server.RateLimiter(3))):
            patcher = mock.patch.object(gateway, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        server = gateway._Gateway(("127.0.0.1", 0), gateway.MCPHandler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        self.port = server.server_address[1]

    def get(self, path):
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        conn.request("GET", path)
        raw = conn.getresponse()
        response = Response(raw.status, raw.getheaders(), raw.read())
        conn.close()
        return response

    def test_sign_in_and_callback_share_the_per_ip_limit(self):
        self.assertEqual([self.get("/auth/login").status for _ in range(3)], [302] * 3)
        refused = self.get("/auth/login")
        self.assertEqual(refused.status, 429, refused.body)
        self.assertTrue(refused.header("Content-Type").startswith("application/problem+json"))
        self.assertEqual(refused.json()["code"], "pgmi.rate_limited")
        self.assertGreaterEqual(int(refused.header("Retry-After")), 1)
        self.assertEqual(self.get("/auth/callback?code=x&state=y").status, 429, "the callback shares the limit")

class InProcessGateway(unittest.TestCase):
    """The gateway's HTTP server in this process, on a channel the test gives."""

    def serve(self, fake_channel):
        patcher = mock.patch.object(gateway, "CHANNEL", fake_channel)
        patcher.start()
        self.addCleanup(patcher.stop)
        server = gateway._Gateway(("127.0.0.1", 0), gateway.MCPHandler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        return server.server_address[1]


class HealthTest(InProcessGateway):
    def test_a_query_string_is_still_health(self):
        port = self.serve(FakeChannel())
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        conn.request("GET", "/health?x=1")
        raw = conn.getresponse()
        self.assertEqual((raw.status, json.loads(raw.read())), (200, {"status": "healthy"}))
        conn.close()


class AnsweringChannel(FakeChannel):
    def __init__(self, headers):
        super().__init__()
        self.headers = headers

    def rest_invoke(self, ctx, policy, headers, body):
        return channel.RestResult(200, self.headers, b"{}")


class ResponseHeaderTest(InProcessGateway):
    def test_a_header_with_cr_lf_or_nul_never_reaches_the_wire(self):
        for headers in ({"x-echo": "a\r\nSet-Cookie: injected=1"}, {"x-echo": "a\nb"}, {"x-a\rb": "1"},
                        {"x-echo": "a\0b"}):
            with self.subTest(headers=headers):
                port = self.serve(AnsweringChannel(headers))
                conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
                conn.request("GET", "/api/echo")
                raw = conn.getresponse()
                response = Response(raw.status, raw.getheaders(), raw.read())
                conn.close()
                self.assertEqual(response.status, 500, response.body)
                self.assertEqual(response.json()["code"], "pgmi.internal_error")
                self.assertIsNone(response.header("Set-Cookie"))
                self.assertIsNone(response.header("x-echo"))

    def test_a_clean_header_is_sent(self):
        port = self.serve(AnsweringChannel({"x-echo": "fine"}))
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        conn.request("GET", "/api/echo")
        raw = conn.getresponse()
        self.assertEqual((raw.status, raw.getheader("x-echo")), (200, "fine"))
        conn.close()


class RecordingChannel(FakeChannel):
    def __init__(self):
        super().__init__()
        self.mcp_headers = []

    def mcp_policy(self, ctx, request, requested, mcp_headers):
        self.mcp_headers.append(mcp_headers)
        return channel.Policy(modern=True)


class RouteChangeTest(InProcessGateway):
    def test_a_route_changing_interceptor_restates_the_mcp_headers(self):
        def rename(request, ctx):
            request.json["params"]["name"] = "rewritten_tool"
        patcher = mock.patch.object(gateway, "PIPELINE", interceptors.Pipeline([Recorder(needs_json=True, change=rename)]))
        patcher.start()
        self.addCleanup(patcher.stop)
        recording = RecordingChannel()
        port = self.serve(recording)
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        conn.request("POST", "/mcp", json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                                                 "params": {"name": "echo_note", "arguments": {}}}),
                     {"Content-Type": "application/json", "MCP-Protocol-Version": "2026-07-28",
                      "Mcp-Method": "tools/call", "Mcp-Name": "echo_note", "X-Connection": "cn_test"})
        conn.getresponse().read()
        conn.close()
        self.assertEqual(len(recording.mcp_headers), 2, recording.mcp_headers)
        self.assertEqual(tuple(recording.mcp_headers[1]), ("2026-07-28", "tools/call", "rewritten_tool"))


class SlowClientTest(InProcessGateway):
    def setUp(self):
        for name in ("HEADER_READ_SECONDS", "BODY_READ_SECONDS"):
            patcher = mock.patch.object(gateway, name, 0.5)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.port = self.serve(FakeChannel())

    def test_trickled_headers_are_cut_off(self):
        with socket.create_connection(("127.0.0.1", self.port), timeout=5) as sock:
            started = time.monotonic()
            closed = False
            for byte in b"GET /health HTTP/1.1\r\nHost: x\r\n":
                try:
                    sock.sendall(bytes([byte]))
                    time.sleep(0.1)
                except OSError:
                    closed = True
                    break
            if not closed:
                closed = sock.recv(1024) == b""
            self.assertTrue(closed, "the gateway kept a trickling client")
            self.assertLess(time.monotonic() - started, 3)

    def test_a_trickled_body_is_408(self):
        with socket.create_connection(("127.0.0.1", self.port), timeout=5) as sock:
            sock.sendall(b"POST /api/echo HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n"
                         b"Content-Length: 100\r\n\r\n{")
            answer = b""
            while chunk := sock.recv(4096):
                answer += chunk
        self.assertTrue(answer.startswith(b"HTTP/1.0 408") or answer.startswith(b"HTTP/1.1 408"), answer)
        self.assertIn(b'"pgmi.request_timeout"', answer)


class FakeConnection:
    def __init__(self):
        self.cancels = 0

    def cancel_safe(self, timeout=None):
        self.cancels += 1


class FakeHangup:
    def __init__(self):
        self.seen = threading.Event()

    def happened(self):
        self.seen.set()


class HangupTest(unittest.TestCase):
    def test_a_hang_up_cancels_the_running_statement_and_no_later_one(self):
        hangup, conn = channel.Hangup(), FakeConnection()
        with hangup.running(conn):
            hangup.happened()
        self.assertEqual(conn.cancels, 1)
        with self.assertRaises(channel.ClientGone):
            with hangup.running(FakeConnection()):
                pass

    def test_a_returned_connection_is_never_cancelled(self):
        hangup, conn = channel.Hangup(), FakeConnection()
        with hangup.running(conn):
            pass
        hangup.happened()
        self.assertEqual(conn.cancels, 0)

    def watched(self):
        client, server = socket.socketpair()
        self.addCleanup(client.close)
        self.addCleanup(server.close)
        hangup = FakeHangup()
        self.addCleanup(gateway.watch_for_hangup(server, hangup))
        return client, server, hangup

    def test_a_closed_client_is_a_hang_up(self):
        client, _, hangup = self.watched()
        client.close()
        self.assertTrue(hangup.seen.wait(2))

    def test_a_pipelined_request_is_not_a_hang_up_and_stays_unread(self):
        client, server, hangup = self.watched()
        client.sendall(b"GET / HTTP/1.1\r\n")
        time.sleep(3 * gateway.HANGUP_POLL_SECONDS)
        client.close()
        self.assertFalse(hangup.seen.wait(3 * gateway.HANGUP_POLL_SECONDS))
        self.assertEqual(server.recv(64), b"GET / HTTP/1.1\r\n")

    def test_a_stopped_watch_sees_nothing(self):
        client, server = socket.socketpair()
        self.addCleanup(server.close)
        hangup = FakeHangup()
        gateway.watch_for_hangup(server, hangup)()
        time.sleep(2 * gateway.HANGUP_POLL_SECONDS)
        client.close()
        self.assertFalse(hangup.seen.wait(3 * gateway.HANGUP_POLL_SECONDS))


class StartupLineTest(unittest.TestCase):
    def test_no_credential_or_parameter_is_printed(self):
        for url in ("postgresql://gw:secret@db.example.com:5480/app?password=gwpass&sslmode=disable",
                    "postgresql://db.example.com:5480/app?user=gw&password=gwpass",
                    "host=db.example.com port=5480 dbname=app user=gw password=gwpass"):
            with self.subTest(url=url):
                shown = gateway.database_display(url)
                self.assertEqual(shown, "db.example.com:5480/app")


    def test_a_tls_setting_libpq_cannot_meet_is_refused_before_the_pool_opens(self):
        base = "postgresql://gw@db.example.com/app"
        for url, env, refused in [
            (base + "?sslmode=bogus", {}, "sslmode=bogus is not a libpq sslmode"),
            (base + "?sslmode=verify-full", {}, "sslmode=verify-full needs sslrootcert"),
            (base, {"PGSSLMODE": "verify-ca"}, "sslmode=verify-ca needs sslrootcert"),
            (base + "?sslmode=require&sslrootcert=system", {}, "sslrootcert=system needs sslmode=verify-full"),
            ("host=db.example.com sslmode=verify-full sslrootcert=/ca.pem", {}, None),
            (base + "?sslmode=verify-full", {"PGSSLROOTCERT": "/ca.pem"}, None),
            (base + "?sslrootcert=system", {}, None),
            (base, {}, None),
        ]:
            with self.subTest(url=url, env=env):
                problem = gateway.database_url_problem(url, env)
                if refused is None:
                    self.assertIsNone(problem)
                else:
                    self.assertIn(refused, problem or "")

    def test_a_malformed_url_is_refused_without_its_password(self):
        for url in ("postgresql://gw:Pw0rd%zzLEAK@db.example.com/app", "postgres://gw:Pw0rdLEAK@[::1/db"):
            with self.subTest(url=url):
                problem = gateway.database_url_problem(url, {})
                self.assertEqual(problem, "DATABASE_URL is not a valid connection string")
                self.assertNotIn("LEAK", problem)

    def test_an_unencoded_at_is_refused_without_the_password(self):
        problem = gateway.database_url_problem("postgresql://gw:Sup3r@ecret-ZZ9@127.0.0.1:5432/app", {})
        self.assertEqual(problem, "DATABASE_URL has an unencoded @ in its user or password: percent-encode it as %40")
        for url in ("postgresql://gw:Sup3r%40ecret-ZZ9@127.0.0.1:5432/app",
                    "host=127.0.0.1 dbname=app user=gw password=Sup3r@ecret-ZZ9"):
            with self.subTest(url=url):
                self.assertIsNone(gateway.database_url_problem(url, {}))


if __name__ == "__main__":
    unittest.main(verbosity=2)
