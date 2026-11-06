# Client protocol contract

The wire rules every client, gateway or service in front of this database
keeps, in any language. The examples in this folder implement them; the
database enforces most of them again. The full texts are
`docs/advanced/LOGIN-TRANSPORT.md` and `docs/advanced/MCP-GATEWAY.md` in the
pgmi repository.

## Who talks to PostgreSQL

The gateway connects as a login that is a member of the api role
(`<db>_api`). That role executes the entry points and nothing else:

| Entry point | For |
|---|---|
| `api.rest_route_policy(method, url, requested_isolation)` | the transaction to open for a REST request, and the handler's name and `input_annotations` |
| `api.rest_invoke(method, url, headers hstore, content bytea)` | a REST request; returns `status_code`, `headers`, `content` |
| `api.mcp_request_policy(request, requested_isolation, protocol_version, mcp_method, mcp_name)` | the transaction to open for an MCP request, its era (`modern`) and its `refusal` |
| `api.mcp_handle_request(request jsonb, context jsonb)` | an MCP request; returns the JSON-RPC envelope, NULL for a notification |
| `api.rpc_route_policy(method, requested_isolation)`, `api.rpc_invoke(api.rpc_resolve(method), headers hstore, content bytea)` | JSON-RPC on `POST /rpc`, the same pattern as REST: the method from the body picks the policy, the database checks the envelope; the Go gateway routes it |
| `api.open_login_session`, `api.close_login_session` | sign-in after OIDC, logout |
| `api.open_dev_login_session` | sign-in without a provider; exists only in an `env=dev` deploy |

That login asserts identities, so it is a credential, not only a
connection. The database checks a secret for a session or `cn_` handle, but
it trusts the issuer and subject the gateway names to `api.open_login_session`
after an OIDC sign-in, and the `{"client": {issuer, subject, clientId}}` it
sends for an OAuth client on `/mcp`. Whoever holds `DATABASE_URL` can sign in
as anyone and act as every person who approved an OAuth client, in every
organization they approved it for. Keep it as secret as a token-signing key:
one login per gateway, never shared with jobs, migrations or tooling.

## Connections

- One pool per process, bounded, with connect and acquire timeouts (5 s in
  the examples). Never a connection per request. No connection within the
  acquire timeout, or a database that cannot be reached, is 503
  `pgmi.unavailable` with `Retry-After`.
- Set `application_name` to a name operators recognize (the examples use
  `pgmi-gateway-<language>`). It shows in `pg_stat_activity` and the log.
- Leave nothing on a pooled connection: set transaction characteristics with
  `BEGIN ...` or `SET TRANSACTION` inside the transaction, never per session.
- The request deadline (`REQUEST_DEADLINE_SECONDS`, 30 s) reaches the
  database: first in every transaction, `SELECT set_config('statement_timeout', $1, true)`
  with the milliseconds the request has left. It is transaction-local, so the
  connection goes back to the pool with the server's default. A statement the
  timeout cancels (SQLSTATE `57014`), or a request the gateway's own deadline
  ends, is 504 `pgmi.deadline_exceeded` (MCP and JSON-RPC: `-32603` with
  `data.code` `pgmi.deadline_exceeded`), never retried.
- A client that hangs up mid-request cancels its statement (a cancel request
  to the backend, as `pg_cancel_backend` does): the pooled connection is free
  again within moments, not when the query would have ended.
- Honour `sslmode` in `DATABASE_URL` as libpq does: `require` encrypts,
  `verify-ca` and `verify-full` also check the server certificate against
  `sslrootcert` (and the host name, for `verify-full`); `require` with
  `sslrootcert` is `verify-ca`. No `sslmode` means `prefer`: TLS first, plain
  text only when the server has no TLS; `allow` is plain text first, TLS when
  the server demands it. A driver without that fallback makes the choice on
  its first connection, never with a startup probe, so the gateway still
  starts while the database is down. `sslrootcert` is the only trust anchor: a
  driver that adds it to the system roots, or falls back to them when it is
  missing, lets any public CA vouch for the database. So `verify-ca` and
  `verify-full` without `sslrootcert` are refused at startup, before the pool
  opens (`sslmode=<mode> needs sslrootcert in DATABASE_URL: the CA file to
  trust, or system`); `sslrootcert=system` means the system roots and is taken
  with `verify-full` only. The check uses the effective values, the driver's
  `PGSSLMODE` and `PGSSLROOTCERT` included. An `sslmode` outside libpq's six
  values, and a setting the driver cannot apply, are refused at startup (exit
  code 1), never dropped: a lost `sslmode` is a plaintext or
  unverified connection that still works.

## The path is the database's

The gateway passes the request target under the REST prefix to the database
exactly as the client sent it: no path cleaning, no decoding, no redirect
(`/api//x`, `/api/./x` and `/api/%2e%2e/x` reach `api.rest_route_policy` as
written). Route matching, and refusing what does not match, is the
database's.

## One request, one transaction

1. **Resolve, then open.** Before the transaction, call the policy function
   (`api.rest_route_policy`, `api.mcp_request_policy`). It returns
   `transaction_isolation`, `transaction_read_only`, `transaction_deferrable`;
   no row means "no such route": open with the defaults and let the invoke
   answer 404. A client may ask for more isolation with
   `X-PGMI-Transaction-Isolation` (`read-committed`, `repeatable-read`,
   `serializable`, case and separators free); an unknown value is 400
   `pgmi.transaction_isolation_unsupported` before the database is called.
2. **Open** the transaction with exactly those characteristics, build the SQL
   from fixed text (never from the request), call the invoke function,
   commit.
3. **Retry 40001 and 40P01** (serialization failure, deadlock) as a whole new
   transaction, a few times with jittered exponential backoff. Only the code
   that runs `BEGIN` can retry: a savepoint or a loop in PL/pgSQL keeps the
   old snapshot and never converges. The commit can fail with 40001 too, so it
   belongs inside the retry. When attempts run out: REST answers 409 with
   code `pgmi.transaction_retryable` and `Retry-After`; MCP answers the
   JSON-RPC error `-31000` with `data.code` `pgmi.transaction_retryable`.
4. **Any other database error** is logged by the gateway and answered as 500
   `pgmi.internal_error` (MCP: `-32603`). The client never sees SQL text.

## Identity: handles and verified tokens

| To the database | Comes from |
|---|---|
| `x-login-session` | the gateway's own `__Host-session` cookie (HttpOnly, Secure, SameSite=Strict, so no cross-site request carries it, a link included), set at sign-in; the `__Host-csrf` cookie is Strict too, the short-lived `__Host-login` cookie Lax, because the provider's redirect back to `/auth/callback` is cross-site |
| `x-organization-session` | the client, as sent; worth nothing without its login session |
| MCP context `{"client": {"issuer", "subject", "clientId"}}` | an OAuth access token on `/mcp` that the gateway verified ("MCP clients with OAuth"); never the token |
| MCP context `{"connection": ...}` | a connection handle on `/mcp`: `Authorization: Bearer cn_...`, or the `X-Connection` header when no other `Authorization` header is sent |

- Drop client-sent `x-login-session`, `x-connection` and `cookie`, in any
  letter case.
- The database sees each header name once, lower-cased. A repeated name is
  joined with `, ` (RFC 9110 §5.3), `Cookie` with `; `. A repeat of
  `authorization`, `content-length`, `content-type`, `host`, `origin`,
  `x-csrf-token`, `x-organization-session` or
  `x-pgmi-transaction-isolation`, in any letter case, is 400
  `pgmi.header_collision`: one of two values would be guessed at. (Most HTTP
  servers refuse a repeated `Host` or a conflicting `Content-Length`
  themselves, with a bare 400.)
- No header ever names a user, an email or an organization.
- A request with the session cookie and a method other than GET, HEAD or
  OPTIONS must carry `X-CSRF-Token`, the value of the readable `__Host-csrf`
  cookie (an HMAC of the session handle under a server secret), or it is 403
  `pgmi.csrf_refused`.
- A browser `Origin` not on the allowlist is 403 `pgmi.origin_refused`.
- Copy every response header the database returns (`Vary`,
  `WWW-Authenticate`, `Location`, ...), except hop-by-hop headers. `Vary`
  names `Cookie`, not `x-login-session`: the session reaches the gateway in
  `__Host-session`, and a cache sees only what the client sent.

## Request and response bodies

- A request body over 1 MiB is 413 `pgmi.body_too_large`, before the
  database is called. A `Content-Length` that is not a decimal number is 400;
  most HTTP servers answer it themselves, without a problem body. A chunked
  body is read; a gateway that cannot read one answers 411
  `pgmi.length_required` and never runs the request with an empty body.
  Reads from the client time out; a client that stops sending never holds a
  thread or a connection.
- Request headers over 64 KiB are 431 before the database is called; a
  server may refuse less (Node's default is 16 KiB, Kestrel's 32 KiB).
- `Content-Length` on a response is the length of its content, except for
  HEAD: there it is the database's `content-length`, the length GET would
  have sent (RFC 9110 §8.6), and no content follows. A 1xx, 204 or 304
  carries no `Content-Length` and no content.
- A problem the gateway answers itself replaces whatever headers it had
  already copied from the database.

## Status codes

Errors are RFC 9457 problem details (`application/problem+json`) with a
`code`. Branch on `code`, never on `title` or `detail`.

| Status | Means | `code` examples |
|---|---|---|
| 400 | the request is malformed; it does not match the declared input schema | `pgmi.invalid_body` (with `invalid-params`), `pgmi.header_collision`, `pgmi.method_too_long` |
| 401 | no live session; carries `WWW-Authenticate` | `pgmi.unauthenticated` (an MCP 401 from the gateway's token check has no body) |
| 403 | signed in, but not allowed; or a browser `Origin` not on the allowlist | `pgmi.forbidden`, `pgmi.organization_required`, `pgmi.csrf_refused`, `pgmi.origin_refused` |
| 404 | no such route, or a resource the caller cannot see | `pgmi.not_found` (a path the gateway does not serve) |
| 405 | a method the path does not serve; carries `Allow` | `pgmi.method_not_allowed` |
| 406 | `Accept` refuses every type the path answers | `pgmi.not_acceptable` |
| 408 | the request body stalled or did not arrive in time (30 s in most examples; C# uses Kestrel's minimum data rate) | `pgmi.request_timeout` |
| 409 | the request conflicts with the current state; or retries ran out | `membership.connection.agents_disabled`, `pgmi.transaction_retryable` |
| 411 | a chunked request body the gateway does not read | `pgmi.length_required` |
| 413 | the request body is over the limit | `pgmi.body_too_large` |
| 414 | the URL is over 8192 characters | `pgmi.uri_too_long` |
| 415 | a request body of a type the path does not take | `pgmi.unsupported_media_type` |
| 429 | over a rate limit on `/mcp` or `/auth`; carries `Retry-After` | `pgmi.rate_limited` |
| 422 | well-formed, but a business rule refuses it | `membership.connection.not_member` |
| 428 | a proxy opened a weaker transaction than the route needs | `pgmi.transaction_isolation_too_weak` |
| 503 | no database connection within the acquire timeout or the database is unreachable, or the identity provider is unreachable; carries `Retry-After` | `pgmi.unavailable`, `pgmi.identity_provider_unavailable` |
| 504 | the request ran past its deadline; the database cancelled the statement | `pgmi.deadline_exceeded` |

Application codes (`membership.*`, `core.*`, your own) come from the
handlers; `pgmi.*` codes come from the framework.

## MCP

- `POST /mcp` only, `Content-Type: application/json`, JSON answers (no SSE).
  What the gateway refuses on its own, before the body is read, is a problem
  (`application/problem+json`), never an empty or HTML body: another method
  is 405 `pgmi.method_not_allowed` with `Allow: POST`; another content type
  415 `pgmi.unsupported_media_type`; an `Accept` that refuses JSON 406
  `pgmi.not_acceptable`; an `Origin` not on the allowlist 403
  `pgmi.origin_refused`.
- The gateway knows no protocol version. It passes the body and the
  `MCP-Protocol-Version`, `Mcp-Method` and `Mcp-Name` headers, as sent (NULL
  when absent), to `api.mcp_request_policy`. A non-NULL `refusal` is the
  answer and nothing is dispatched: `-32020` when a header disagrees with the
  body (`data.header` names it), `-32022` for an unknown version
  (`data {supported, requested}`), `-32602` for a missing
  `clientCapabilities`. Older clients send `initialize` first, no such headers
  and no `_meta` version.
- No pooled connection is 503 with `Retry-After` and the JSON-RPC error
  `-31000`, `data.code` `pgmi.unavailable`.
- The caller is the agent of a connection a person approved, through an
  OAuth access token ("MCP clients with OAuth") or a handle: signed in on
  the web, `POST /connections` (`clientName`, `organizationId`, an `agent.*`
  `roleId`, `validDays`) returns the `cn_` handle once. The client sends it
  as `Authorization: Bearer cn_...` or as `X-Connection`, with or without
  `MCP_RESOURCE`. A `cn_` handle is not an OAuth token: the gateway passes
  it to the database as `{"connection": ...}` and never to the token
  verifier. MCP provisions nobody.
- No handle and no token is anonymous (401 with `MCP_RESOURCE` set, see
  below). A handle the database knows no live connection by (unknown,
  revoked, expired, its membership ended, its person disabled) is 401 with
  `WWW-Authenticate: Bearer error="invalid_token"` (with `resource_metadata`
  and `scope` when `MCP_RESOURCE` is set, the same header as a refused
  token) and no body. The database says so in the envelope it returns:
  `-31001` with `data.code` `pgmi.connection_unknown`; the gateway sends
  the 401 instead of that envelope.
- A caller the database cannot let act yet gets the JSON-RPC error `-31001`
  at 200, so every client shows its message. Branch on `data.code`:

  | `data.code` | Means | Remedy |
  |---|---|---|
  | `membership.connection.pending` | an OAuth client this person has not approved; `data.pairingCode` and `data.expiresAt` come with it | the person approves the code on the web |
  | `membership.connection.paused` | its connection is paused | the person (or a manager, for a manager's pause) resumes it |
  | `membership.connection.disabled` | its organization is inactive, its agents are switched off, or the agent is disabled | the owner switches them on |
  | `membership.connection.not_admitted` | the token's issuer and subject are no person who signed in on the web, or the client id is one the database does not take | sign in on the web first, as the same person |

### MCP clients with OAuth

With `MCP_RESOURCE` set, `/mcp` is an OAuth 2.1 resource server (MCP
2026-07-28 "Authorization"; RFC 9728, RFC 8707, OAuth 2.1 §5.2).

- `GET /.well-known/oauth-protected-resource` and
  `GET /.well-known/oauth-protected-resource<path of MCP_RESOURCE>` answer
  `{"resource", "authorization_servers", "bearer_methods_supported": ["header"], "scopes_supported"}`.
  `resource` is `MCP_RESOURCE` exactly.
- A request with `Authorization: Bearer <token>` is checked before the
  database is called: a JWT signed with RS256, PS256 or ES256 (no other
  algorithm, in every gateway) by a key in the issuer's JWKS (discovered
  from `<issuer>/.well-known/openid-configuration`, cached; an unknown key id
  refetches the set at most once per 10 s), `iss` one of
  `MCP_AUTHORIZATION_SERVERS`, `exp`, `iat` and `nbf` JSON numbers
  (fractions allowed, strings refused) checked with 60 s of leeway,
  `aud` exactly `MCP_RESOURCE` (one audience; scheme and host compared
  case-insensitively), a non-empty string client id that is not the web
  sign-in's `OIDC_CLIENT_ID` (`client_id` when the claim is present, even as
  `null`; `azp` only when it is absent), and a `sub`. A token that is not a JWT,
  names another issuer or another audience is refused without fetching any
  key.
- Only `{"client": {"issuer", "subject", "clientId"}}` reaches the database.
  The gateway never forwards, stores or logs the token; an interceptor sees
  it and is bound by the same rule (Interceptors).
- No token, and no connection handle: 401 with
  `WWW-Authenticate: Bearer resource_metadata="<metadata URL>"` (and
  `scope="..."`). An invalid token: the same with `error="invalid_token"`.
  No body.
- The identity provider's discovery or keys cannot be read: 503 with
  `Retry-After` and the JSON-RPC error `-31000`, `data.code`
  `pgmi.identity_provider_unavailable`.
- The first call of an unknown (person, client) answers `-31001`
  `membership.connection.pending` with a 6-character pairing code (from
  `23456789ABCDEFGHJKMNPQRSTVWXYZ`), the same code on every call for an
  hour, to that same issuer, subject and client only. The person, signed in
  on the web, sends `POST /connections/approve`
  `{"pairingCode", "organizationId", "roleId", "validDays"}` (201; 422
  `membership.connection.pairing_code_unknown` for a code that is not theirs
  or expired). From then on the same token acts as the connection's agent.

### Rate limits

On `/mcp`, in every gateway process (N processes allow N times the limit;
put a shared limit in the reverse proxy when that matters):

- per client address, `RATE_LIMIT_PER_IP` requests a minute, counted first,
  before the token is looked at, so a flood of forged tokens never reaches
  the identity provider;
- per verified (issuer, subject, client id), `RATE_LIMIT_PER_CLIENT` a minute,
  after the token check and before the database.

Over a limit: 429 `pgmi.rate_limited` with `Retry-After` (seconds). A token
bucket per key, refilled continuously, behind one lock, at most 10 000 keys
(least recently seen goes first). `0` turns a limit off.

### MCP error codes

MCP 2026-07-28 reserves `-32768`..`-32000` for JSON-RPC and the spec. pgmi
answers there only with the spec's own codes (`-32700`, `-32600`..`-32603`,
`-32020`..`-32022`); its own are outside it. `data.code` is the stable key
to branch on; the number names the family.

| Code | Means | `data.code` |
|---|---|---|
| `-31000` | try again later: retries ran out, no pooled connection, the identity provider is unreachable (from the gateway) | `pgmi.transaction_retryable`, `pgmi.unavailable`, `pgmi.identity_provider_unavailable` |
| `-31001` | the client cannot act (yet), from the database before any handler | `membership.connection.pending`, `.paused`, `.disabled`, `.not_admitted`; `pgmi.connection_unknown` (sent as 401) |
| `-31003` | access refused, from the database | `pgmi.forbidden`, `pgmi.organization_required` |
| `-32603` | internal error; also a transaction weaker than the handler's isolation floor, or a request past its deadline (sent as 504) | `pgmi.transaction_isolation_too_weak` (with `requiredIsolation`), `pgmi.interceptor_failed`, `pgmi.deadline_exceeded` |

pgmi's own JSON-RPC endpoint (`api.rpc_invoke`) is plain JSON-RPC 2.0, not
MCP, and keeps `-32001` (authentication required) and `-32003` (no
permission). A handler body refuses with `-32010` (the state refuses, sent as
409, `data.code`) or `-32011` (invalid input, sent as 422,
`data.invalid-params`); a gateway passes the status and body through.

### MCP status codes

A refusal is answered with 400 and never dispatched. A dispatched answer
takes its status from the policy row's `modern` and the JSON-RPC error code:
the 2026-07-28 spec asks for 400 only for a header mismatch, an unsupported
version and a missing required `_meta` field, and answers ordinary JSON-RPC
errors with 200. Branch on the code, never on the message.

| Answer | 2026-07-28 era | legacy era |
|---|---|---|
| a `refusal` from `api.mcp_request_policy` (`-32020`, `-32022`, `-32602`) | `400` | `400` |
| `-32601` from `api.mcp_handle_request` | `404` | `200` |
| `-32022` from `api.mcp_handle_request` | `400` | `200` |
| `-31001` with `data.code` `pgmi.connection_unknown` | `401`, no body | `401`, no body |
| any other answer, a `-32602` for an unknown tool or resource included | `200` | `200` |
| a notification | `202`, no body | `202`, no body |

## Extension seams

Every gateway offers two places to add behaviour without touching the
request handling: the database channel and the interceptor pipeline.
Projects build features on them (embeddings, storage-URL signing,
enrichment, redaction); the examples ship only enough to show the seams.

### The database channel

One interface with one entry per database call the gateway makes for a
request: the policy and the dispatch of each protocol it serves (REST
policy, REST invoke, MCP policy, MCP request; RPC policy and invoke where
the gateway serves RPC). The real implementation talks to the pool;
decorators implement the same interface and wrap it, so cross-cutting
behaviour is composed once at startup and no call site changes:

    ConnectionRetry(SerializationRetry(Tracing(Postgres(pool))))

- **Serialization retry** (40001, 40P01): the database rolled the whole
  transaction back, so the call runs again as a new transaction, writes
  included (see "One request, one transaction").
- **Connection retry**: a call is sent again after the connection dropped
  only when sending it twice cannot apply anything twice: the policy
  lookups, and a dispatch the database opened `READ ONLY` (the route
  declared `readOnly`). Never a write: the connection can drop after the
  commit and before the answer. MCP is never read-only, so its dispatch is
  never re-sent. A server error with a SQLSTATE, and "no pooled connection",
  are not a dropped connection.
- **Tracing**: one log line per call with the request id, the operation, the
  route, the outcome and the time. The route is the path, never the query
  string, which can carry a token. Never bodies or headers.
- Both retries wrap the tracing, so every attempt is a line of its own.

### Interceptors

An ordered pipeline around each dispatch, built at startup and read-only
afterwards.

- A **request interceptor** runs after the policy call and before the
  transaction opens. It sees every header the client sent, lower-cased,
  repeated names joined, credentials included (`authorization`, `cookie`,
  `x-connection`, `x-login-session`, `x-csrf-token`): an on-behalf-of token
  exchange or a request signature needs them. It may change the method, URL,
  headers and body. If it changes the route (REST method or URL; MCP method,
  tool name or URI), the gateway resolves the policy again and the context
  then carries the new handler's name and annotations. On MCP that second
  call states `Mcp-Method` and `Mcp-Name` from the rewritten body; the
  client's own were checked against the body it sent.
- A **response interceptor** runs after the commit. It may change the
  status, headers and body.
- Each receives a read-only **request context**: request id, protocol
  (`rest`, `mcp`), method, URL, deadline, and what the policy returned
  about the handler. There is no principal: the database resolves who is
  calling inside the transaction.

The policy functions return two columns for this, in the call the gateway
already makes: `handler_name` (the handler's function) and
`input_annotations`, the `x-*` keywords of each top-level `inputSchema`
property as `{"property": {"x-keyword": value}}`, NULL when there are none.

Rules:

- **Credentials stay the caller's.** An interceptor never logs or stores a
  credential, and sends one only to the service the caller authorised.
- **Identity is not an interceptor's.** On `/api` the gateway drops the
  handles and cookie the client sent and sets the login session from its own
  cookie after the pipeline, whatever an interceptor did; on `/mcp` the
  caller is resolved before the pipeline and only the body reaches the
  database. An interceptor may set `x-organization-session`: the database
  accepts it only with the login session it was opened in.
- **The database still decides.** An interceptor never decides access or
  validity. It selects itself from the context (an annotation such as
  `"x-embed": true`, the handler name), never from a list of paths.
- **Zero cost when unused.** When no interceptor applies, the body passes as
  the bytes the client sent. Each interceptor declares whether it needs the
  parsed JSON; when one does, the body is parsed once and serialized once for
  all of them. A body that is not JSON passes unchanged: the database
  answers it.
- **Failure policy per interceptor.** `fail` (the default): the answer is
  `502` with code `pgmi.interceptor_failed` (MCP: JSON-RPC `-32603` with
  `data.code` `pgmi.interceptor_failed`), logged with the request id; a
  request interceptor's failure means nothing was dispatched. `degrade`:
  logged, and the request goes on as it was before that interceptor, none
  of its changes kept.
- **Per-interceptor timeout**, bounded by the request's deadline. Overrunning
  it is a failure.
- **No database connection is held** while a request interceptor waits.
- **Thread or task safety.** Interceptors keep no per-request state; it
  travels in the request, the response and the context. An outside service
  is called through one long-lived, pooled, thread-safe client created at
  startup.

### The examples

Enabled by `INTERCEPTORS` (comma-separated, in order); unset, none run.

| Name | Kind | Does |
|---|---|---|
| `embed` | request | for each `inputSchema` property with `"x-embed": true` and a non-blank string value, adds `<property>_embedding`, unless the client sent one; on MCP inside `params.arguments`. Its failure policy is `EMBED_FAILURE_POLICY` |
| `request-id` | response | answers the request id in `X-Request-Id`; never parses the body |
| `header-names` | request | adds `headerNames`, the sorted names of the request's headers (never their values), to the body; on MCP inside `params.arguments` |

The embedder behind `embed` is a fake every language implements the same:
the vector is the first four bytes of the SHA-256 of the UTF-8 text, each
divided by 255 and rounded to four decimal places. It refuses a text over
2000 characters, as a real model refuses one over its token limit.

## Sign-in

Production sign-in is OIDC in the gateway (authorization code with PKCE, ID
token verified, signed with RS256, PS256 or ES256), then `api.open_login_session` with the verified claims; see
`python/login_transport.py`. The examples also serve `POST /auth/dev-login`,
which calls `api.open_dev_login_session` and sets the same cookies. It answers
404 unless the gateway runs with `DEV_LOGIN=1`, and the database has the
function only with `env=dev`. Tests use it; production never does.

`POST /auth/logout` checks `Origin` and `X-CSRF-Token` like any other write,
calls `api.close_login_session` with the session cookie (when there is one),
clears both cookies and answers 204. The closed handle is 401 from then on.

An ID token that fails verification is 401 `pgmi.login.token_invalid`; a provider
the gateway cannot reach (discovery, the token endpoint, or its signing keys)
is 502 `pgmi.login.provider_unavailable`. The pending sign-ins (state, nonce, PKCE
verifier between `/auth/login` and `/auth/callback`) live in the process, at
most 10 000 of them for at most 600 seconds, the oldest going first; expiry
and eviction take the oldest without scanning. `/auth/login` and
`/auth/callback` count against `RATE_LIMIT_PER_IP`, the same limit as `/mcp`,
so one address cannot push other people's sign-ins out: at the default 600 a
minute it holds at most 6 600 live entries (the burst of 600 plus ten minutes
of refill). `RATE_LIMIT_PER_IP=0`, or a flood from many addresses, removes
that bound. Several gateway processes need a shared store or sticky sessions;
or carry the pending sign-in in a signed, short-lived `__Host-login` cookie
and keep no store (ADAPT).

## Configuration

Every example reads the same environment variables, so one test harness and
one deployment recipe fit them all:

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | the gateway login's connection URL (required) |
| `HOST`, `PORT` | where to listen (`127.0.0.1`, `8080`) |
| `ALLOWED_ORIGINS` | comma-separated `Origin` allowlist (default: this gateway's own) |
| `LOGIN_CSRF_SECRET` | key of the CSRF tokens; the same on every process (default: random) |
| `DEV_LOGIN` | `1` serves `POST /auth/dev-login` |
| `POOL_MAX_SIZE` | most database connections held (10) |
| `RETRY_MAX_ATTEMPTS` | attempts at a transaction that loses a serialization race (3) |
| `RETRY_BASE_DELAY` | seconds; first backoff ceiling, doubled per attempt, capped at 1 s (0.05) |
| `REQUEST_DEADLINE_SECONDS` | seconds a request may take, the database's `statement_timeout` included (30) |
| `INTERCEPTORS` | the example interceptors to run, in order: `embed`, `request-id`, `header-names` (none) |
| `EMBED_FAILURE_POLICY` | `fail` or `degrade`, for `embed` (`fail`) |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URI`, `OIDC_SCOPES` | OIDC sign-in |
| `MCP_RESOURCE` | the canonical URI of `/mcp` as clients reach it; set, `/mcp` is an OAuth resource server (unset) |
| `MCP_AUTHORIZATION_SERVERS` | comma-separated issuer URLs whose access tokens `/mcp` takes; each also a web sign-in issuer |
| `MCP_SCOPES` | space-separated `scopes_supported` (`openid`) |
| `RATE_LIMIT_PER_IP` | `/mcp`, `/auth/login` and `/auth/callback` requests a minute per client address, one limit, before anything else (600; 0 off) |
| `RATE_LIMIT_PER_CLIENT` | `/mcp` requests a minute per verified OAuth client (300; 0 off) |

`GET /health` answers 200 `{"status": "healthy"}` once the process serves
requests. Any path the gateway does not serve is 404 `pgmi.not_found`.

The end-to-end harness in `internal/scaffold/testdata/gateways/e2e/` of the
pgmi repository checks these rules against a running gateway in any language.
