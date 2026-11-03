"""
Request and response interceptors: an ordered pipeline around each dispatch.

Demo code from pgmi. Do not copy this file into the project as is. Read it,
then write your own that fits this project: it is a baseline to learn from,
not a library. The rules it keeps are in ../PROTOCOL.md ("Extension seams").

A request interceptor runs after the policy call and before the transaction
opens; it may change the method, URL, headers and body. A response
interceptor runs after the commit; it may change the status, headers and
body. Uses: embedding vectors for text fields, enrichment from another
service, signing storage URLs the handler returned, redaction.

The rules:

- The database still decides. An interceptor never decides access or
  validity; it selects itself from what the policy call returned (the
  handler, the x-* annotations on its inputSchema), never from a path list.
- Zero cost when unused. No interceptor applies: the body passes as bytes,
  never parsed. Some need JSON: it is parsed once and serialized once for all.
- Failure policy per interceptor. "fail" (default) answers 502 with the
  problem code pgmi.interceptor_failed; "degrade" logs and goes on with the
  request as it was before that interceptor. A timeout is a failure.
- No database connection is held while an interceptor runs.
- Thread safety. Pipelines are built at startup and only read afterwards;
  interceptors keep no per-request state (it is in the request, the response
  and the context, one set per request thread). An outgoing client an
  interceptor uses is one long-lived, thread-safe, pooled client created at
  startup and handed to its constructor (see README.md).

This file also holds the two examples the gateway can enable (INTERCEPTORS):
EmbedFields with a FakeEmbedder, and RequestIdHeader.
"""

import copy
import hashlib
import json
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from typing import Protocol

FAIL = "fail"
DEGRADE = "degrade"
_UNPARSED = object()


class Message:
    """Headers and a body, the body readable as bytes or as parsed JSON. The
    JSON is parsed on first read and cached, so every interceptor shares one
    parse; finish() serializes it once. A body that is not JSON reads as
    None and passes on as the bytes it was: the database answers it."""

    def __init__(self, headers, body, parsed=_UNPARSED):
        self.headers = headers
        self.body = body
        self._json = parsed

    @property
    def json(self):
        if self._json is _UNPARSED:
            try:
                self._json = json.loads(self.body) if self.body else None
            except (ValueError, UnicodeDecodeError):
                self._json = None
        return self._json

    @json.setter
    def json(self, value):
        self._json = value

    @property
    def parsed(self):
        return self._json is not _UNPARSED

    def finish(self):
        if self.parsed and self._json is not None:
            self.body = json.dumps(self._json, separators=(",", ":"), ensure_ascii=False).encode("utf-8")

    def copy(self):
        twin = copy.copy(self)
        twin.headers = dict(self.headers)
        if self.parsed:
            twin._json = copy.deepcopy(self._json)
        return twin

    def adopt(self, other):
        self.__dict__.update(other.__dict__)


class Request(Message):
    def __init__(self, method, url, headers, body, parsed=_UNPARSED):
        super().__init__(headers, body, parsed)
        self.method = method
        self.url = url


class Response(Message):
    def __init__(self, status, headers, body, parsed=_UNPARSED):
        super().__init__(headers, body, parsed)
        self.status = status


def arguments(request, ctx):
    """The object the handler's inputSchema describes: the REST body, or an
    MCP call's params.arguments. None when there is none."""
    value = request.json
    if ctx.protocol == "mcp":
        params = value.get("params") if isinstance(value, dict) else None
        value = params.get("arguments") if isinstance(params, dict) else None
    return value if isinstance(value, dict) else None


class Interceptor:
    """name shows in the log. needs_json says whether it reads request.json
    or response.json; one that only touches headers leaves the body unparsed.
    applies() selects it from the context, which holds what the database
    published for the route."""

    name = "interceptor"
    needs_json = False
    failure = FAIL
    timeout = 2.0

    def applies(self, ctx):
        return True


class RequestInterceptor(Interceptor):
    def on_request(self, request: Request, ctx) -> None:
        raise NotImplementedError


class ResponseInterceptor(Interceptor):
    def on_response(self, response: Response, ctx) -> None:
        raise NotImplementedError


class InterceptorFailed(Exception):
    def __init__(self, name):
        super().__init__(name)
        self.name = name


class Pipeline:
    """Runs the interceptors that apply, in order, each on a worker thread so
    its timeout holds even when it blocks (Python cannot stop a thread: one
    that overruns is abandoned and its changes are never read). That hop is
    paid on every request an interceptor applies to, so one that applies to
    every route costs every route a little. ADAPT: size the executor for the
    interceptors' concurrency; a request thread waits on its own interceptor,
    so a full executor shows up as timeouts."""

    def __init__(self, request_interceptors=(), response_interceptors=(), workers=32, log=print):
        self.request_interceptors = tuple(request_interceptors)
        self.response_interceptors = tuple(response_interceptors)
        self._executor = ThreadPoolExecutor(workers, thread_name_prefix="interceptor") \
            if self.request_interceptors or self.response_interceptors else None
        self._log = log

    def requests_for(self, ctx):
        return tuple(i for i in self.request_interceptors if i.applies(ctx))

    def responses_for(self, ctx):
        return tuple(i for i in self.response_interceptors if i.applies(ctx))

    def run_requests(self, interceptors, request, ctx):
        self._run(interceptors, "on_request", request, ctx)

    def run_responses(self, interceptors, response, ctx):
        self._run(interceptors, "on_response", response, ctx)

    def _run(self, interceptors, hook, message, ctx):
        for interceptor in interceptors:
            # degrade must be able to go on "as before": the interceptor works
            # on a copy, adopted only when it succeeds. fail needs no copy:
            # the request ends.
            work = message.copy() if interceptor.failure == DEGRADE else message
            deadline = min(ctx.deadline, time.monotonic() + interceptor.timeout)
            if interceptor.needs_json:
                work.json
            future = self._executor.submit(getattr(interceptor, hook), work, replace(ctx, deadline=deadline))
            try:
                future.result(timeout=max(0.0, deadline - time.monotonic()))
            except Exception as e:
                future.cancel()
                reason = "timed out" if isinstance(e, TimeoutError) else "%s: %s" % (type(e).__name__, e)
                if interceptor.failure == DEGRADE:
                    self._log("[interceptor] %s %s degraded (%s)" % (ctx.request_id, interceptor.name, reason))
                    continue
                self._log("[interceptor] %s %s failed (%s)" % (ctx.request_id, interceptor.name, reason))
                raise InterceptorFailed(interceptor.name) from e
            if work is not message:
                message.adopt(work)
        message.finish()


# -- Example: a computed field for every property the schema marks x-embed ---

class Embedder(Protocol):
    """The outside call behind an interface: a real one posts to an embedding
    API through one shared, pooled client; tests use the fake."""

    def embed(self, texts: list[str], timeout: float) -> list[list[float]]: ...


class FakeEmbedder:
    """Deterministic and offline: a vector from a hash of the text. Like a real
    model with a token limit, it refuses a text over MAX_CHARS."""

    DIMENSIONS = 4
    MAX_CHARS = 2000

    def embed(self, texts, timeout):
        vectors = []
        for text in texts:
            if len(text) > self.MAX_CHARS:
                raise ValueError("text over %d characters" % self.MAX_CHARS)
            digest = hashlib.sha256(text.encode("utf-8")).digest()
            vectors.append([round(b / 255, 4) for b in digest[:self.DIMENSIONS]])
        return vectors


class EmbedFields(RequestInterceptor):
    """For each inputSchema property marked "x-embed": true, adds a
    <property>_embedding sibling holding its vector. The schema declares the
    sibling too, so the database validates it like any other field.

    One batched call for all fields. A sibling already present is left
    alone: a client that embeds for itself is not embedded twice.

    ADAPT: an embedding outage can degrade (the handler searches without the
    vector) or fail; choose per project."""

    name = "embed-fields"
    needs_json = True

    def __init__(self, embedder: Embedder, failure=FAIL, timeout=2.0):
        self._embedder = embedder
        self.failure = failure
        self.timeout = timeout

    def applies(self, ctx):
        return any(a.get("x-embed") is True for a in ctx.annotations.values())

    def on_request(self, request, ctx):
        args = arguments(request, ctx)
        if args is None:
            return
        fields = [name for name, a in ctx.annotations.items()
                  if a.get("x-embed") is True and isinstance(args.get(name), str) and args[name].strip()
                  and name + "_embedding" not in args]
        if not fields:
            return
        vectors = self._embedder.embed([args[name] for name in fields], timeout=ctx.remaining())
        for name, vector in zip(fields, vectors, strict=True):
            args[name + "_embedding"] = vector


# -- Example: a response header ---------------------------------------------

class RequestIdHeader(ResponseInterceptor):
    """Answers the gateway's request id in X-Request-Id, the id its log lines
    carry. Headers only: the body is never parsed."""

    name = "request-id"

    def on_response(self, response, ctx):
        response.headers["x-request-id"] = ctx.request_id
