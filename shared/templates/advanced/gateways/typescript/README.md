# TypeScript reference gateway

Demo code from pgmi, a baseline to learn from, not a library. Read it, then
write your own gateway that fits this project. The wire rules are in
[../PROTOCOL.md](../PROTOCOL.md); every place that needs a project decision is
marked `ADAPT`.

Node 22 or newer, plain `node:http`, node-postgres (`pg`) for the pool and
`jose` for JWT checks. No web framework: the routing is a dozen lines in
`route()`, and the rules that matter (headers seen raw, the body counted, the
status chosen) read better without one. In an Express, Fastify or Hono
project, keep the handlers and the error classes and let the framework route.

| File | What |
|---|---|
| `src/main.ts` | reads the environment (a typo is a startup error), composes the seams, listens |
| `src/gateway.ts` | the HTTP side: REST under `/api`, MCP on `/mcp`, sign-in routes; every error answered in `answerError()` |
| `src/channel.ts` | every database call behind the `Channel` interface, and its decorators |
| `src/interceptors.ts` | the request and response interceptor pipeline, and three examples |
| `src/login.ts` | OIDC sign-in, session and CSRF cookies, the headers the database sees |
| `src/resource-server.ts` | `/mcp` as an OAuth resource server: metadata, token checks, rate limits |
| `src/gateway.test.ts` | unit tests of the seams; database tests with `DATABASE_URL` |

## Run it

The database login and `DATABASE_URL` come first: the project README's
Quick Start, "Run a gateway", creates them.

```bash
npm ci
npm run build
DATABASE_URL="postgresql://mydb_gateway:$GATEWAY_PASSWORD@localhost:5432/mydb" npm start
npm test        # the database tests run when DATABASE_URL is set
```

Settings are the environment variables of `../PROTOCOL.md` ("Configuration").

## How a request flows

`createGateway()` returns the `node:http` listener. `route()` picks a handler
(`rest`, `mcp`, `signIn`, ...). A handler checks the request, calls the
channel and writes the answer; for anything else it throws a typed error
(`Problem`, `Unauthorized`, `RpcError`, `RetriesExhausted`, `PoolTimeout`,
`DeadlineExceeded`, `InterceptorFailed`, ...). `answerError()` maps each error to its answer in
one place: a problem on REST, a JSON-RPC envelope on `/mcp`.

Each request has one `AbortSignal` (`ex.signal`, `ctx.signal`). It aborts at
the request deadline (`REQUEST_DEADLINE_SECONDS`, 30 s) or when the client
hangs up; the body read, the retry backoff and every interceptor listen to
it. Pass it to every outgoing call you add.

The deadline reaches the database too: every transaction the channel opens
sets `statement_timeout` local to it to the milliseconds the request has
left, so the server cancels a statement nobody waits for and the pooled
connection is free again. A statement cancelled that way (SQLSTATE `57014`)
or a wait the deadline ends is `DeadlineExceeded`: 504
`pgmi.deadline_exceeded` on REST, 504 with JSON-RPC `-32603` on `/mcp`,
never retried.

The signal reaches the database as well: when it aborts mid-statement, the
channel sends `pg_cancel_backend` for the transaction's backend over a short
connection of its own (`cancelBackend()`), so a client that hangs up frees its
pooled connection within moments. node-postgres has no public cancel API,
hence the SQL. A connection a cancel was sent to is discarded, not pooled, so
a cancel that lands late cannot hit the next request's statement.

Outgoing HTTP (the identity provider, an embedding API) uses the built-in
`fetch`. Its global dispatcher is one keep-alive connection pool for the
whole process, so there is no client object to create or share.

## Calling a new handler

REST and MCP need no gateway change: a new REST route is reachable at
`/api/<its path>` and a new MCP tool through `tools/call`, because the gateway
forwards to `api.rest_invoke` and `api.mcp_handle_request` and the database
routes. A client calls it like any other route:

```ts
const response = await fetch("http://localhost:8080/api/projects", {
  method: "POST",
  headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf, Cookie: `__Host-session=${session}` },
  body: JSON.stringify({ name: "Apollo" }),
});
if (!response.ok) {
  const problem = await response.json();   // application/problem+json
  // branch on problem.code, never on title or detail
}
```

A new database entry point (RPC through `api.rpc_route_policy` and
`api.rpc_invoke`, say) is one more method on `Channel` in `channel.ts`,
implemented in `postgresChannel` with the same policy-then-dispatch pattern,
and passed through by each decorator (`...inner` passes what a decorator does
not change).

## The two seams

Both are in `../PROTOCOL.md` ("Extension seams").

### The database channel

`main.ts` composes it once; every request shares it:

```ts
const channel = connectionRetry(serializationRetry(tracing(postgresChannel(pool)), attempts, baseDelayMs));
```

A decorator is a function from `Channel` to `Channel` that overrides only the
calls it changes:

```ts
export function timing(inner: Channel, record: (op: string, ms: number) => void): Channel {
  const timed = async <T>(op: string, call: () => Promise<T>) => {
    const start = performance.now();
    try { return await call(); } finally { record(op, performance.now() - start); }
  };
  return {
    ...inner,
    restInvoke: (ctx, policy, headers, body) => timed("rest.invoke", () => inner.restInvoke(ctx, policy, headers, body)),
  };
}
```

`connectionRetry` shows the rule every retry keeps: a call is sent again only
when it cannot apply anything twice (a policy lookup, a `readOnly` route). A
write never is: the connection can drop after the commit. A pool timeout is
not a dropped connection; it is answered 503 at once.

### Interceptors

`INTERCEPTORS=embed,request-id,header-names` runs the three examples; unset, none run and
every body passes as the bytes the client sent. An interceptor is an object:

```ts
export function signUrls(signer: Signer): ResponseInterceptor {
  return {
    name: "sign-urls",
    needsJson: true,              // reads response.json; the body is parsed once for all
    failure: "fail",              // an unsigned URL is a broken answer: 502
    timeoutMs: 2000,              // bounded by the request's deadline too
    applies: (ctx) => ctx.handler === "list_files",   // ADAPT: handler names, as the database publishes them
    async run(response, ctx, signal) {
      const body = isObject(response.json) ? response.json : {};
      for (const item of Array.isArray(body.files) ? body.files : []) {
        if (isObject(item) && typeof item.url === "string") item.url = await signer.sign(item.url, 300, signal);
      }
    },
  };
}
```

Register it in `EXAMPLES` in `interceptors.ts` (a `Map` from the name
`INTERCEPTORS` uses to a factory), or build the `Pipeline` yourself in
`main.ts`. A request interceptor is the same with `RequestInterceptor`; it
runs after the policy call and before the transaction opens, and may change
the method, URL, headers and body. It sees every header the client sent,
credentials included; it never logs or stores one, and sends one only to the
service the caller authorised. Change the route and the gateway resolves the
policy again; the client's handles are dropped and the session handles set
again after it, whatever it did.

Embeddings from a real model: an `Embedder` over `fetch`.

```ts
const openAiEmbedder: Embedder = {
  async embed(texts, signal) {
    const response = await fetch(process.env.EMBED_URL + "/embeddings", {
      method: "POST", signal,
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.EMBED_KEY },
      body: JSON.stringify({ model: "text-embedding-3-small", input: texts }),
    });
    if (!response.ok) throw new Error(`embedding API answered ${response.status}`);
    const { data } = (await response.json()) as { data: { embedding: number[] }[] };
    return data.map((d) => d.embedding);
  },
};
EXAMPLES.set("embed", (failure) => ({ request: embedFields(openAiEmbedder, failure) }));
```

Then mark the property in the handler's `inputSchema`:
`"query": {"type": "string", "x-embed": true}, "query_embedding": {"type": "array"}`.

The rules: an interceptor keeps no per-request state (it is in the message
and `ctx`), selects itself from `ctx` (the handler and its `x-*`
annotations, never a path list), and is `"fail"` unless the request is still
right without it (`"degrade"`). Its timeout is enforced; pass `signal` to
every outgoing call.

## OAuth and sign-in

### MCP clients with OAuth

`MCP_RESOURCE=https://mcp.example.com/mcp MCP_AUTHORIZATION_SERVERS=https://login.example.com`
turns it on (`../PROTOCOL.md`, "MCP clients with OAuth"). `mcpCaller()` in
`gateway.ts` calls `resource.verify(...)` before reading the body and puts
only the verified issuer, subject and client id into the MCP context.
A connection handle (`Authorization: Bearer cn_...`, or `X-Connection`
without another `Authorization`) goes to the database as it is, OAuth on or
off; one the database does not know is 401 (`handleUnknown()`). The places to adapt are
marked `ADAPT` in `resource-server.ts`.

### Sign-in

With `OIDC_ISSUER`, `OIDC_CLIENT_ID` and `OIDC_REDIRECT_URI` set, `login.ts`
runs the authorization code flow with PKCE and checks the ID token with
`jose`. The pending sign-ins live in this process's memory (`PendingLogins`),
at most 10 000 for at most 600 seconds, the oldest going first.
`/auth/login` and `/auth/callback` count against `RATE_LIMIT_PER_IP`, the
same limit as `/mcp`, so one address cannot push other people's sign-ins
out. Several processes behind a load balancer need a shared store or sticky
sessions, or a signed, short-lived `__Host-login` cookie in place of the store
(`ADAPT`). `DEV_LOGIN=1` serves `POST /auth/dev-login` for an
`env=dev` deploy; never in production.

## What to adapt

Every place is marked `ADAPT` in the code.

| What | Where |
|---|---|
| the name in `pg_stat_activity`, the pool size | `APPLICATION_NAME` and `createPool` in `channel.ts` |
| the channel decorators | `main.ts` |
| your interceptors | `EXAMPLES` in `interceptors.ts`, or the `Pipeline` in `main.ts` |
| RPC, if you serve it | `Channel` in `channel.ts` |
| trace lines to your logger or OpenTelemetry | `tracing` in `channel.ts` |
| the request deadline; a web framework instead of `node:http` | `gateway.ts` |
| cookie names and lifetimes, the development issuer, `LOGIN_CSRF_SECRET`; pending sign-ins across processes | `login.ts` |
| client ids that are never agents; the client address behind a reverse proxy | `resource-server.ts` |
