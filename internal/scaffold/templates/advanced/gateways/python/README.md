# Python reference gateway

Demo code from pgmi, a baseline to learn from, not a library. Read it, then
write your own gateway that fits this project. The wire rules are in
[../PROTOCOL.md](../PROTOCOL.md).

| File | What |
|---|---|
| `gateway.py` | the HTTP server: REST under `/api`, MCP on `/mcp`, sign-in |
| `login_transport.py` | OIDC sign-in, session and CSRF cookies |
| `resource_server.py` | `/mcp` as an OAuth resource server: metadata, token checks, rate limits |
| `channel.py` | every database call behind one interface, and its decorators |
| `interceptors.py` | the request and response interceptor pipeline, and two examples |
| `test_gateway.py` | unit tests of the seams; database tests with `DATABASE_URL` |

Python 3.10 or newer, the standard library's `http.server` with a thread per
request, psycopg 3 with `psycopg_pool`, and PyJWT for token signatures. No
web framework: the rules that matter (headers seen raw, the body counted,
the status chosen) are easy to follow without one.

## Run it

```bash
pip install -r requirements.txt
DATABASE_URL="postgresql://mydb_gateway:$GATEWAY_PASSWORD@localhost:5432/mydb" python gateway.py
python -m unittest test_gateway    # the database tests run when DATABASE_URL is set
```

Settings are the environment variables of `../PROTOCOL.md` ("Configuration").

## How a request flows

`_Gateway` is a `ThreadingMixIn` `HTTPServer`: each request runs on its own
thread in `MCPHandler`. `do_POST` and `do_GET` pick the route: `/api/...`
goes to `_rest()`, `/mcp` to the MCP branch of `do_POST`, `/auth/...` to the
sign-in methods. A route checks the request, calls the channel and writes
the answer. The outcomes it expects are caught where the answer's shape is
known: `RetriesExhausted` is 409 (MCP: `-31000`), `InterceptorFailed` 502,
`PoolTimeout` 503, anything else a logged 500 that never shows its text.
`send_problem()` writes every problem.

Every request gets a context with a 30 s deadline (`_new_context`); the
retry backoff and every interceptor stop when it ends. Pass
`ctx.remaining()` to every outgoing call you add.

## Calling a new handler

REST and MCP need no gateway change: a new REST route is reachable at
`/api/<its path>` and a new MCP tool through `tools/call`, because the
gateway forwards to `api.rest_invoke` and `api.mcp_handle_request` and the
database routes. A client calls it like any other route:

```python
response = httpx.post("http://localhost:8080/api/projects", json={"name": "Apollo"},
                      headers={"X-CSRF-Token": csrf},            # the __Host-csrf cookie's value
                      cookies={"__Host-session": session})
if response.status_code >= 400:
    problem = response.json()                                    # application/problem+json
    # branch on problem["code"], never on title or detail
```

A new database entry point (RPC through `api.rpc_route_policy` and
`api.rpc_invoke`, say) is one more method on `Channel` in `channel.py`,
implemented on `PostgresChannel` with the same policy-then-dispatch pattern.
`channel.Decorator` passes every call it does not override through.

## The two seams

Both are in `../PROTOCOL.md` ("Extension seams").

### The database channel

`main()` composes it once; every request thread shares it:

```python
CHANNEL = channel.ConnectionRetry(
    channel.SerializationRetry(channel.TracingChannel(channel.PostgresChannel(POOL)),
                               MAX_RETRY_ATTEMPTS, RETRY_BASE_DELAY))
```

A decorator subclasses `channel.Decorator` and overrides only the calls it
changes. `ConnectionRetry` shows the rule every retry keeps: a call is sent
again only when it cannot apply anything twice (a policy lookup, a
`readOnly` route). A write never is.

### Interceptors

`INTERCEPTORS=embed,request-id` runs the two examples; unset, none run and
every body passes as bytes. Add your own in `build_pipeline()` in
`gateway.py`.

Embeddings from a real model, in a few lines. `httpx.Client` is thread-safe
and pools its connections; create one at startup and share it (add `httpx` to
`requirements.txt`):

```python
class ModelEmbedder:
    def __init__(self, client: httpx.Client, model: str):
        self._client, self._model = client, model

    def embed(self, texts, timeout):
        response = self._client.post("/embeddings", json={"model": self._model, "input": texts},
                                     timeout=timeout)
        response.raise_for_status()
        return [item["embedding"] for item in response.json()["data"]]

client = httpx.Client(base_url=os.environ["EMBED_URL"],
                      headers={"Authorization": "Bearer " + os.environ["EMBED_KEY"]})
requests.append(interceptors.EmbedFields(ModelEmbedder(client, "text-embedding-3-small"),
                                         failure=interceptors.DEGRADE))
```

Then mark the property in the handler's `inputSchema`:
`"query": {"type": "string", "x-embed": true}, "query_embedding": {"type": "array"}`.

Signed storage URLs, after the commit:

```python
class SignUrls(interceptors.ResponseInterceptor):
    name, needs_json = "sign-urls", True

    def __init__(self, signer):            # the key comes from configuration, never logged
        self._signer = signer

    def applies(self, ctx):
        return ctx.handler in SIGNING_HANDLERS   # ADAPT: handler names, as the database publishes them

    def on_response(self, response, ctx):
        body = response.json if isinstance(response.json, dict) else {}
        for item in body.get("files", []):
            item["url"] = self._signer.sign(item["url"], expires_in=300)
```

The rules: an interceptor keeps no per-request state (it is in `request`,
`response` and `ctx`), selects itself from `ctx`, and is `fail` unless the
request is still right without it (`degrade`). Its `timeout` is enforced;
pass `ctx.remaining()` to every outgoing call.

## OAuth and sign-in

### MCP clients with OAuth

`MCP_RESOURCE=https://mcp.example.com/mcp MCP_AUTHORIZATION_SERVERS=https://login.example.com`
turns it on (`../PROTOCOL.md`, "MCP clients with OAuth"). `_mcp_caller()` in
`gateway.py` calls `RESOURCE.verify(...)` before reading the body and puts
only `identity.context()` into the MCP context. A connection handle
(`Authorization: Bearer cn_...`, or `X-Connection` without another
`Authorization`) goes to the database as it is, OAuth on or off; one the
database does not know is 401 (`_handle_unknown()`). The places to adapt are marked `ADAPT` in
`resource_server.py`.

### Sign-in

With `OIDC_ISSUER`, `OIDC_CLIENT_ID` and `OIDC_REDIRECT_URI` set,
`login_transport.py` runs the authorization code flow with PKCE and checks
the ID token with PyJWT. The pending sign-ins live in this process's memory
(`PendingLogins`): several processes behind a load balancer need a shared
store or sticky sessions. `DEV_LOGIN=1` serves `POST /auth/dev-login` for an
`env=dev` deploy; never in production.

## What to adapt

Every place is marked `ADAPT` in the code.

| What | Where |
|---|---|
| the name in `pg_stat_activity`, the pool size | `APPLICATION_NAME`, `POOL_MAX_SIZE` in `gateway.py` |
| the channel decorators | `main()` in `gateway.py` |
| your interceptors | `build_pipeline()` in `gateway.py` |
| RPC, if you serve it | `Channel` in `channel.py` |
| trace lines to your logging or OpenTelemetry | `TracingChannel` in `channel.py` |
| cookie names and lifetimes, the development issuer | `login_transport.py` |
| client ids that are never agents; the client address behind a reverse proxy | `resource_server.py` |
| the server itself | `http.server` is fine to read and to run small; serve the same rules from your ASGI or WSGI server to scale |
