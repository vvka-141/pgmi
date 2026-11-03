"""
The database channel: every call the gateway makes to PostgreSQL for a
request, behind one interface.

Demo code from pgmi. Do not copy this file into the project as is. Read it,
then write your own that fits this project: it is a baseline to learn from,
not a library. The rules it keeps are in ../PROTOCOL.md ("Extension seams").

    Channel                 one method per call: the policy and the dispatch
                            of each protocol the gateway serves
    PostgresChannel         the real thing, over a psycopg_pool pool
    TracingChannel          a log line per call: op, route, outcome, time
    SerializationRetry      40001/40P01: the whole transaction again
    ConnectionRetry         a dropped connection: again, idempotent calls only

Decorators wrap a channel and are channels themselves, so cross-cutting
behaviour (caching, metrics, a test double) is added at one place, startup,
without touching a call site:

    channel = ConnectionRetry(SerializationRetry(TracingChannel(PostgresChannel(pool))))

Both retries sit outside the tracing, so every attempt is a log line of its
own. The objects are built once and only read afterwards: every request
thread shares them, and per-request data travels in the arguments.
"""

import json
import random
import time
from abc import ABC, abstractmethod
from dataclasses import dataclass, field, replace
from types import MappingProxyType

import psycopg
from psycopg_pool import PoolTimeout


@dataclass(frozen=True)
class RequestContext:
    """What every channel call and every interceptor may read about the
    request. Read-only: a change is a new object (dataclasses.replace).

    There is no principal here: the gateway forwards handles and the database
    resolves who is calling inside the transaction, so nothing before the
    dispatch knows. handler and annotations come from the policy call."""

    request_id: str
    protocol: str                      # "rest" or "mcp"
    method: str                        # REST: the HTTP method; MCP: the JSON-RPC method
    url: str                           # REST: the URL under /api; MCP: /mcp
    deadline: float                    # time.monotonic() after which the request is abandoned
    handler: str | None = None         # the handler's function name, from the policy
    annotations: MappingProxyType = field(default_factory=lambda: MappingProxyType({}))  # {property: {"x-...": value}}

    def remaining(self):
        return max(0.0, self.deadline - time.monotonic())


@dataclass(frozen=True)
class Policy:
    """One row of a policy function (api.rest_route_policy,
    api.mcp_request_policy). isolation None: the connection default."""

    isolation: str | None = None
    read_only: bool = False
    deferrable: bool = False
    handler: str | None = None
    annotations: dict | None = None
    modern: bool = False               # MCP only: the 2026-07-28 era
    refusal: dict | None = None        # MCP only: the answer instead of a dispatch


@dataclass(frozen=True)
class RestResult:
    status: int
    headers: dict                      # lower-case names
    content: bytes


class RetriesExhausted(Exception):
    def __init__(self, sqlstate, attempts):
        super().__init__(sqlstate)
        self.sqlstate = sqlstate
        self.attempts = attempts


class Channel(ABC):
    """One entry per call. ADAPT: a gateway that serves RPC adds
    rpc_policy/rpc_invoke here, and every decorator gets them too."""

    @abstractmethod
    def rest_policy(self, ctx: RequestContext, requested: str | None) -> Policy: ...

    @abstractmethod
    def rest_invoke(self, ctx: RequestContext, policy: Policy, headers: dict, body: bytes | None) -> RestResult: ...

    @abstractmethod
    def mcp_policy(self, ctx: RequestContext, request, requested: str | None, mcp_headers: tuple) -> Policy: ...

    @abstractmethod
    def mcp_request(self, ctx: RequestContext, policy: Policy, request, context: dict | None) -> dict | None: ...


_ISOLATION_SQL = {
    "read committed": "ISOLATION LEVEL READ COMMITTED",
    "repeatable read": "ISOLATION LEVEL REPEATABLE READ",
    "serializable": "ISOLATION LEVEL SERIALIZABLE",
}


def transaction_characteristics(policy):
    """SET TRANSACTION for a resolved policy, built from fixed text only."""
    parts = [_ISOLATION_SQL[policy.isolation]] if policy.isolation is not None else []
    parts.append("READ ONLY" if policy.read_only else "READ WRITE")
    if policy.deferrable:
        parts.append("DEFERRABLE")
    return "SET TRANSACTION " + " ".join(parts)


class PostgresChannel(Channel):
    """Each policy call runs on a pooled connection in its own short
    transaction; each dispatch in one transaction opened with the resolved
    characteristics. SET TRANSACTION is the transaction's first statement, so
    a pooled connection carries nothing from one request to the next.

    The policy costs one round trip per request. It changes only at deploy
    time: a caching decorator would key it on api.catalog_version()."""

    def __init__(self, pool):
        self._pool = pool

    def _policy_row(self, sql, params):
        with self._pool.connection() as conn:
            return conn.execute(sql, params).fetchone()

    def _dispatch(self, policy, sql, params):
        # The commit at the end of the with block can fail with 40001 too;
        # SerializationRetry sees it either way.
        with self._pool.connection() as conn:
            conn.execute(transaction_characteristics(policy))
            return conn.execute(sql, params).fetchone()

    def rest_policy(self, ctx, requested):
        row = self._policy_row(
            "SELECT transaction_isolation, transaction_read_only, transaction_deferrable,"
            " handler_name, input_annotations FROM api.rest_route_policy(%s, %s, %s)",
            [ctx.method, ctx.url, requested])
        if row is None:
            return Policy()
        return Policy(*row[:3], handler=row[3], annotations=row[4])

    def rest_invoke(self, ctx, policy, headers, body):
        status, response_headers, content = self._dispatch(
            policy,
            "SELECT r.status_code, extensions.hstore_to_json(r.headers), r.content"
            " FROM api.rest_invoke(%s, %s, extensions.hstore(%s::text[], %s::text[]), %s::bytea) r",
            [ctx.method, ctx.url, list(headers), list(headers.values()), body])
        return RestResult(status, {k.lower(): v for k, v in (response_headers or {}).items()},
                          bytes(content or b""))

    def mcp_policy(self, ctx, request, requested, mcp_headers):
        row = self._policy_row(
            "SELECT transaction_isolation, transaction_read_only, transaction_deferrable,"
            " handler_name, input_annotations, modern, refusal"
            " FROM api.mcp_request_policy(%s, %s, %s, %s, %s)",
            [json.dumps(request), requested, *mcp_headers])
        return Policy(*row)

    def mcp_request(self, ctx, policy, request, context):
        row = self._dispatch(policy, "SELECT (api.mcp_handle_request(%s, %s)).envelope",
                             [json.dumps(request), json.dumps(context) if context else None])
        return row[0] if row else None


class Decorator(Channel):
    """Passes every call to the inner channel; a decorator overrides only
    what it changes."""

    def __init__(self, inner: Channel):
        self.inner = inner

    def rest_policy(self, ctx, requested):
        return self.inner.rest_policy(ctx, requested)

    def rest_invoke(self, ctx, policy, headers, body):
        return self.inner.rest_invoke(ctx, policy, headers, body)

    def mcp_policy(self, ctx, request, requested, mcp_headers):
        return self.inner.mcp_policy(ctx, request, requested, mcp_headers)

    def mcp_request(self, ctx, policy, request, context):
        return self.inner.mcp_request(ctx, policy, request, context)


class TracingChannel(Decorator):
    """One line per call with the request id, so a slow or failing request can
    be followed from the gateway log into the database's exchange log.
    Never bodies or headers: they carry personal data and session handles.
    ADAPT: send it to your logging or OpenTelemetry instead of print."""

    def __init__(self, inner, log=print):
        super().__init__(inner)
        self._log = log

    def _traced(self, ctx, op, call, outcome):
        start = time.monotonic()
        try:
            result = call()
        except Exception as e:
            self._log("[trace] %s %s %s %s error=%s %.1fms" % (ctx.request_id, op, ctx.method, ctx.url,
                                                              getattr(e, "sqlstate", None) or type(e).__name__,
                                                              (time.monotonic() - start) * 1000))
            raise
        self._log("[trace] %s %s %s %s %s %.1fms" % (ctx.request_id, op, ctx.method, ctx.url, outcome(result),
                                                    (time.monotonic() - start) * 1000))
        return result

    def rest_policy(self, ctx, requested):
        return self._traced(ctx, "rest.policy", lambda: self.inner.rest_policy(ctx, requested),
                            lambda p: "handler=%s" % p.handler)

    def rest_invoke(self, ctx, policy, headers, body):
        return self._traced(ctx, "rest.invoke", lambda: self.inner.rest_invoke(ctx, policy, headers, body),
                            lambda r: "status=%d" % r.status)

    def mcp_policy(self, ctx, request, requested, mcp_headers):
        return self._traced(ctx, "mcp.policy", lambda: self.inner.mcp_policy(ctx, request, requested, mcp_headers),
                            lambda p: "refused" if p.refusal is not None else "handler=%s" % p.handler)

    def mcp_request(self, ctx, policy, request, context):
        return self._traced(ctx, "mcp.request", lambda: self.inner.mcp_request(ctx, policy, request, context),
                            lambda e: "notification" if e is None else "error" if "error" in e else "result")


def _backoff(attempt, base_delay, max_delay=1.0):
    """Exponential backoff with full jitter: unjittered losers of a race retry
    in lockstep and collide again."""
    return random.uniform(0, min(max_delay, base_delay * (2 ** attempt)))


class SerializationRetry(Decorator):
    """40001 (serialization failure) and 40P01 (deadlock): the database rolled
    the whole transaction back, so running it again is safe for any call,
    writes included. Each attempt is a new transaction and therefore a fresh
    snapshot; a savepoint or a loop in PL/pgSQL keeps the old snapshot and
    never converges. After the last attempt, RetriesExhausted.

    A handler on a route that can hit 40001 must be safe to run twice: a side
    effect outside the transaction (an HTTP call, a queue publish) happens
    again."""

    RETRYABLE = (psycopg.errors.SerializationFailure, psycopg.errors.DeadlockDetected)

    def __init__(self, inner, attempts, base_delay):
        super().__init__(inner)
        self.attempts = attempts
        self._base_delay = base_delay

    def _retrying(self, call):
        for attempt in range(self.attempts):
            try:
                return call()
            except self.RETRYABLE as e:
                if attempt == self.attempts - 1:
                    raise RetriesExhausted(e.sqlstate, self.attempts) from e
                time.sleep(_backoff(attempt, self._base_delay))

    def rest_invoke(self, ctx, policy, headers, body):
        return self._retrying(lambda: self.inner.rest_invoke(ctx, policy, headers, body))

    def mcp_request(self, ctx, policy, request, context):
        return self._retrying(lambda: self.inner.mcp_request(ctx, policy, request, context))


def connection_lost(e):
    """A failure of the connection, not an answer from the server: no SQLSTATE
    (the socket broke), class 08 (connection exception) or 57P01..57P03 (the
    server shut down). 40001 is an OperationalError too, so the class alone
    does not say it; so is PoolTimeout, which is no connection at all and is
    answered 503 at once rather than waited out twice."""
    if not isinstance(e, psycopg.OperationalError) or isinstance(e, PoolTimeout):
        return False
    return e.sqlstate is None or e.sqlstate.startswith("08") or e.sqlstate in ("57P01", "57P02", "57P03")


class ConnectionRetry(Decorator):
    """Sends a call again when the connection dropped, ONLY if sending it
    twice cannot apply anything twice: the policy lookups (STABLE functions)
    and a dispatch the database runs READ ONLY (the route declared readOnly,
    so PostgreSQL refuses any write in it). A write is never re-sent: the
    connection can drop after the server committed and before the answer
    arrived, and a second send would apply it twice. MCP is never read-only
    (every exchange is logged), so its dispatch is never retried here.

    The method name says nothing: a GET handler can write. The database's
    readOnly declaration does."""

    def __init__(self, inner, attempts=2, base_delay=0.05, log=print):
        super().__init__(inner)
        self._attempts = attempts
        self._base_delay = base_delay
        self._log = log

    def _retrying(self, ctx, op, call):
        for attempt in range(self._attempts):
            try:
                return call()
            except psycopg.OperationalError as e:
                if not connection_lost(e) or attempt == self._attempts - 1:
                    raise
                self._log("[retry] %s %s: connection lost (%s), attempt %d" % (ctx.request_id, op, e, attempt + 2))
                time.sleep(_backoff(attempt, self._base_delay))

    def rest_policy(self, ctx, requested):
        return self._retrying(ctx, "rest.policy", lambda: self.inner.rest_policy(ctx, requested))

    def rest_invoke(self, ctx, policy, headers, body):
        if not policy.read_only:
            return self.inner.rest_invoke(ctx, policy, headers, body)
        return self._retrying(ctx, "rest.invoke", lambda: self.inner.rest_invoke(ctx, policy, headers, body))

    def mcp_policy(self, ctx, request, requested, mcp_headers):
        return self._retrying(ctx, "mcp.policy", lambda: self.inner.mcp_policy(ctx, request, requested, mcp_headers))


def with_policy(ctx, policy):
    """The context with what the policy says about the handler."""
    return replace(ctx, handler=policy.handler, annotations=MappingProxyType(policy.annotations or {}))
