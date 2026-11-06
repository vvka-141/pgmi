# Design Record: Agent Principals and Gateway Parity

Status: implemented 2026-11-03, in v0.14.0. Accepted the same day; what
changed or was added while it was built is under "Decided while building".

## Problem

Before v0.14, in the advanced template an MCP client acted through a
connection a person approved on the web, and the request ran *as that
person*, with every permission they held. Audit, change tracking and the exchange log cannot tell
the agent from the person. Only the Python reference gateway served MCP and
OIDC sign-in; the TypeScript, Go, C# and Rust gateways served REST only.

## Decision

### Who acts, and for whom

- An approved connection gets its own **agent principal**: a third principal
  subtype beside user and service. It never signs in on the web and has no
  external identity.
- Every request carries an **effective principal** (whom the request is for)
  and an **actual principal** (who made it), and an **actor kind**: `human`,
  `agent`, `service` or `system`. For an agent call the effective principal is
  the person, the actual principal is the agent, the actor kind is `agent`.
  Row-level security sees the person and the connection's organization.
- An agent's **permissions** are its role's permissions intersected with the
  person's, recomputed on every request. An agent can never do more than its
  person.
- The **role lives on the connection** (no agent membership row). Roles come
  from reserved `agent.*` entries in `data/authorization.json`. Approval
  refuses a role that is not a subset of the approver's permissions, so a
  role's name never overstates what the agent can do.
- Every log records both principals and the actor kind: the audit log, change
  tracking, the access-denial log and the exchange log context. Replay of a
  logged exchange re-checks the connection.

### How an agent authenticates

Two credentials, one model:

1. **OAuth** (default for MCP clients). The gateway is an OAuth 2.1 resource
   server: protected-resource metadata (RFC 9728) at
   `/.well-known/oauth-protected-resource` and its `/mcp` path form, 401 with
   `WWW-Authenticate: Bearer resource_metadata="..."` for a missing or invalid
   token. Tokens come from an external OIDC provider and are verified locally
   (JWKS), with a **strict audience check** (RFC 8707) against the canonical
   `/mcp` URI. A provider that cannot set the audience is not supported. Web
   login tokens are never agent tokens. The first call from an unknown
   (person, client) creates a **pending** connection and answers with a short
   pairing code; the person approves it on the web, choosing organization and
   agent role.
2. **Approved handle** (`X-Connection`, today's `cn_` handle) for headless
   scripts and development: approved on the web first, copied to the client.
   It resolves to the same agent principal and role model.

### Defaults taken (owner may overrule)

- Approval names the organization in the request body; an organization
  session is not required.
- Connections keep an expiry (1 to 365 days); OAuth tokens add their own.
- Lifecycle: pause, resume and revoke by the person; organization managers
  (`members.manage`) list, pause, resume and revoke their organization's agents; an
  organization-wide switch (owner) turns all agents off. When a person's
  membership ends, their connections in that organization are revoked.
- Pending, paused, disabled and not-approved answer JSON-RPC `-31001` with
  `data.code` (and the pairing code while pending) at HTTP 200, so every MCP
  client shows it; a missing or invalid token is HTTP 401.
- Rate limits live in the gateway: per client IP before authentication (so a
  flood of bad tokens never reaches the identity provider) and per
  (subject, client) after it; 429 with `Retry-After`. Per process, documented.
- Pairing codes are 6 characters from an alphabet without 0, O, 1, I, L and
  U, valid for an hour; the first call after expiry issues a new one, stale
  ones are never swept. A pending connection is its own table, so an
  approved connection keeps every column it had; an OAuth connection has no
  handle. One live connection per (person, issuer, client). The `-31001`
  codes are `membership.connection.pending`, `.paused`, `.disabled` and
  `.not_admitted`.

### Gateways

- **MCP header checks move into the database.** `api.mcp_request_policy`
  receives `MCP-Protocol-Version`, `Mcp-Method` and `Mcp-Name` and returns the
  `-32020`, `-32022` or `-32602` envelope; a refused request logs no exchange.
  Each gateway forwards three headers and maps error codes to HTTP statuses.
- **All five gateways reach parity:** REST, MCP (both eras), OIDC sign-in, the
  OAuth resource server and the rate limits. MCP needs no new dependency;
  token verification uses one library per language, while the flow (state,
  nonce, PKCE, `returnTo`, cookies, problem codes) stays hand-written to
  match.
- **One language-neutral end-to-end harness** (REST, MCP, login, OAuth, with a
  mock identity provider) runs against every gateway in CI, asserting codes
  and statuses, never message prose. Environment variable names are the same
  in every language.

## Decided while building

What the record above did not say, or said differently, and how it landed.

### Identity

- **The role is a column of the connection**, `role_id`, NOT NULL, a foreign
  key to the three agent roles: `agent.reader` (a viewer's grants),
  `agent.contributor` (a member's) and `agent.delegate` (`*`). Connections
  that existed before got the narrowest role that covers their person, so
  their reach did not change on upgrade. Kernels refuse to give an agent
  role to a member, and CHECK constraints back that up.
- **The actor kind is derived, not stored beside the principal.** It follows
  from the actual principal's kind: user is `human`, agent is `agent`,
  service is `service`, no principal is `system`. The two can never
  disagree. The log columns are CHECKed on the four values and `NOT NULL
  NOT VALID`, so history written before is not mislabelled.
- **A different actual principal is honoured only** when it is the agent of
  a live connection of that same person; the organization is always the
  connection's. One function, `membership.fn_live_connection`, decides
  liveness on every request and on replay, so a revoked agent replays as 401.
- **Only a person acting as themselves changes connections**: actor kind
  `human` and the actual principal equal to the effective one. An agent
  never approves, extends or resumes an agent.
- **A manager's pause is lifted only by a manager.** Not in the record; added
  so a person cannot undo the pause of their own agent.
- **A handle and a token are refused alike.** A paused or switched-off
  connection answers `-31001` with its reason on both paths
  (`membership.fn_connection_refusal` decides it for both, over
  `membership.fn_live_connection`). A handle that names no live connection
  (unknown, revoked, expired, its membership ended, its person disabled)
  answers `-31001` `pgmi.connection_unknown`, which the gateway sends as
  HTTP 401 with `WWW-Authenticate`, as it does a refused token. The handle
  may come as `X-Connection` or `Authorization: Bearer cn_...`.
- **`fn_add_member` refuses an agent**, and the audit subject of a lifecycle
  change is the agent, with the person in the detail.

### OAuth and pairing

- **A pending connection is its own table**, `membership.pending_connection`.
  An approved connection keeps every column it had and carries exactly one
  credential: a handle, or an OAuth (issuer, client). A partial unique index
  allows one live connection per (person, issuer, client).
- **Pairing codes** come from an unbiased CSPRNG over
  `23456789ABCDEFGHJKMNPQRSTVWXYZ`. Concurrent first calls share one code
  (`ON CONFLICT`), and a code is only ever shown to the same issuer, subject
  and client. `POST /connections/approve` locks the pending row and accepts
  only the code's own person; a wrong or expired code is 422
  `membership.connection.pairing_code_unknown`.
- **With `MCP_RESOURCE` set, an anonymous MCP call is 401**: public tools are
  not reachable without a token or a handle. The owner may overrule this.
- **A client that cannot act yet is refused on every method**, so its user
  sees the pairing code on the first call.
- **Every connection and agent problem code is `membership.connection.<reason>`**
  (13 codes renamed, no aliases), following the template's
  `<schema>.<entity>.<reason>` convention.
- **pgmi's MCP codes sit outside the JSON-RPC reserved range** (MCP
  2026-07-28, "Error Codes"): `-31001` the client cannot act, `-31003`
  access refused (was `-32003`), `-31000` retries exhausted or no database
  connection (was `-32000`); the isolation floor is `-32603`. Clients branch
  on `data.code`. pgmi's own JSON-RPC endpoint keeps `-32001` and `-32003`.

### Gateways

- **The header checks returned a refusal, not an error**:
  `api.mcp_request_policy` returns `modern` and `refusal`, a refusal is
  answered 400 and nothing is dispatched or logged. Only a refusal maps to
  400: a `-32602` from dispatch (an unknown tool) stays 200, because the
  spec asks for 400 only for a header mismatch, an unsupported version and a
  missing required `_meta` field. A dispatched `-32601` is 404 in the
  2026-07-28 era.
- **The gateway's own refusals on `/mcp` are problems with codes**: 405
  `pgmi.method_not_allowed` with `Allow: POST`, 406 `pgmi.not_acceptable`,
  415 `pgmi.unsupported_media_type`, 403 `pgmi.origin_refused`, 429
  `pgmi.rate_limited`. No pooled connection is 503 `pgmi.unavailable`.
- **The path is the database's.** The path under `/api` reaches the
  database exactly as sent: no cleaning, no decoding, no redirect. Two
  frameworks (Go's `ServeMux`, ASP.NET Core's Kestrel) clean paths before
  routing, so those gateways read the raw request target.
- **An unreachable identity provider** is 502 `pgmi.login.provider_unavailable` on
  sign-in, and 503 `pgmi.identity_provider_unavailable` on `/mcp`.
- **One name per setting in all five gateways**: `ALLOWED_ORIGINS`,
  `POOL_MAX_SIZE`, `RETRY_MAX_ATTEMPTS`, `RETRY_BASE_DELAY`, and the OAuth and
  rate-limit variables. Every gateway serves `GET /health` and answers an
  unknown path 404 `pgmi.not_found`.
- **Idiomatic, not ported.** Each gateway is written the way its language is
  usually written, opens every file with a header saying it is an example to
  read and not a library, and marks the decisions a project makes with
  `ADAPT`. Token signatures use one pinned library per language; what the
  library leaves to the caller (nonce, `azp`, `iat`, single audience,
  leeway) is checked by hand.

### Extension seams

Added during the work, for projects that need embeddings, URL signing,
enrichment or redaction without editing request handling. The full contract
is `gateways/PROTOCOL.md`, "Extension seams", in the scaffolded project.

- **The database channel**: one interface with one entry per database call
  (REST policy and invoke, MCP policy and request). Decorators compose once:
  `ConnectionRetry(SerializationRetry(Tracing(Postgres(pool))))`. Connection
  retry re-sends only policy lookups and `READ ONLY` dispatches; MCP is
  never re-sent, and a pool timeout is not a lost connection.
- **Interceptors**: a request interceptor runs after the policy call and
  before the transaction opens, a response interceptor after the commit.
  Each declares whether it needs parsed JSON, its failure policy (`fail` or
  `degrade`), a timeout and when it applies. It selects itself from what the
  policy functions return, `handler_name` and `input_annotations` (the `x-*`
  keywords of the input schema), never from a list of paths.
- **Zero cost when unused**: a route no interceptor selects passes the
  client's bytes unchanged, and the body is parsed once for all that do.
- **A failed interceptor is 502** `pgmi.interceptor_failed` (MCP: `-32603`
  with that `data.code`); after the commit the detail says the request was
  applied. `degrade` keeps none of its changes.
- **A changed route resolves the policy again**, and the session handles are
  set again after the pipeline, whatever an interceptor did. No database
  connection is held while a request interceptor waits.

## Not copied from the downstream design

A missing audience check, rate limiting only after token validation,
connections without expiry, approving a role broader than the approver, and
agent membership rows that outlive the person's membership.
