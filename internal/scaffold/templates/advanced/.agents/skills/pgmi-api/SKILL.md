---
name: pgmi-api
description: The design rules of this advanced-template project's API — REST, RPC and MCP as three HTTP protocols over one handler registry, what the gateway does and what PostgreSQL does, identity from login, organization and connection sessions, access and permissions, roles and cross-organization kernels, one kernel per operation across protocols, and the security review of runtime code. Use when choosing a protocol, deciding whether a check belongs to the gateway (HTTP server) or the database, changing who may call what, or reviewing the API for security.
---

# The API: protocols, identity, access

The database is the application. A gateway in front of it terminates TLS,
signs people in and opens one transaction per request; PostgreSQL resolves
who is calling, decides access, runs the handler and shapes the answer.

Neighbouring skills: pgmi-endpoint (adding a resource), pgmi-layers, then
pgmi-api-layer (tables, kernels, views, rules), pgmi-handlers (what goes in a body), pgmi-mcp (MCP
tools, resources and prompts). Client code is not a skill:
`gateways/README.md` and `gateways/PROTOCOL.md`.

## One declaration, many derivations

A handler declares its contract once, in its registration metadata. The
router, the access decision, the transaction policy, OpenAPI and the MCP
listings are all derived from it. Check both directions when you add a
feature:

- **Declared means enforced.** A key the spec publishes (`access`,
  `permissions`, `querySchema`, `inputSchema`, `outputSchema`, `readOnly`,
  `minTransactionIsolation`) changes what the dispatcher (`api.rest_invoke` and its RPC and MCP
  siblings) does.
- **Enforced means declared.** Anything the dispatcher checks appears in the
  declaration and the spec.

`lib/__test__/test_contract_conformance.sql` and
`lib/__test__/test_openapi_agreement.sql` hold the registry to both.

## Choosing a protocol

| Protocol | Register with | Use for |
|---|---|---|
| REST | `api.create_or_replace_rest_handler` | resources a browser or service reads and writes |
| RPC | `api.create_or_replace_rpc_handler` | named operations that are not a resource (`tasks.reassign`) |
| MCP | `api.create_or_replace_mcp_handler` | tools, resources and prompts for AI agents |

All three share the registry, identity, the access decision and the
transaction. They differ in routing and envelope:
[protocols](references/protocols.md) has the status and error-code mapping.

## Where a check belongs

| Concern | Who |
|---|---|
| TLS, rate limits, request size, CORS, OIDC token checks | the gateway |
| Who is calling (session handles to a person and an organization) | PostgreSQL, per request |
| May they call this handler | PostgreSQL, from `access` / `permissions`, before the body |
| Which rows they see | row-level security on every table |
| Is the input well-formed | the dispatcher's declared checks (400), then the body (422) |
| Business rules, state, references | the body and the kernels |
| Error sanitizing | PostgreSQL: the client never sees `SQLERRM` |

## Identity and access

- A person signs in through the gateway, which calls `api.open_login_session`;
  that is the only path that creates a person. Requests carry opaque handles:
  `x-login-session`, `x-organization-session` (one tab, one organization),
  or for MCP an approved `connection` (a handle, or an OAuth token the
  gateway verified). An unknown, expired or revoked handle fails closed.
- Handlers read identity only through `api.current_principal_id()`,
  `api.current_organization_id()`, `api.has_permission(text)` and
  `api.vw_current_user`. A deploy test fails on any handler, job or `core`
  routine that reads an `auth.*` setting or a session header by name.
- Every handler declares `access` (`public`, `authenticated`,
  `organization`) or `permissions`. Permissions and roles live in `data/authorization.json`.
  Prefer `permissions` for organization data.
- Every table is forced to row-level security or exempt with a reason; a
  kernel that must work across organizations is `SECURITY DEFINER`, handed to
  the kernel role, and checks its own reach.

[Identity and access](references/identity-and-access.md) has the roles, the
kernel rules and the test helpers.

## Layers

Handlers sit on a semantic layer, not on tables: kernels are the only
writers, views the read surface, and every rule is one `*_problem` function
that the kernel and every handler read. The pgmi-layers skill has the
physical and semantic layers; pgmi-api-layer has what they look like in this
template and the API layer on top.

- **One operation, one kernel.** A REST handler, an RPC method and an MCP
  tool that do the same thing call the same kernel and the same problem
  function. A REST page and an MCP tool for one task share one `LANGUAGE sql
  STABLE` function returning `jsonb`.
- **Complete documents.** Answer what the client renders, references
  resolved and counts included, from one snapshot; after a write, the whole
  entity.

## Habits to unlearn

| From an application server | Here |
|---|---|
| a service layer in another language querying the database | the handler is a database function; the gateway only proxies |
| CRUD per table, the client joining the results | a task or a page answered in one call from a view; writes through a kernel |
| `RAISE` or an exception class for "not found", "conflict" | a `*_problem` code turned into a coded answer |
| an auth check at the top of every handler | `access` / `permissions` in the registration |
| a retry loop around a serialization failure | none in SQL: whoever opened the transaction retries it |
| an ORM migration per change | run-once structure files and re-run code files, deployed by `deploy.sql` |

## Reviewing for security

Deploy-time code and request-time code have different threat models; most
false alarms come from mixing them. [Security review](references/security.md)
says what to flag and what belongs to the gateway.

## Rules

- Never expose PostgreSQL to the internet; the gateway is the only caller,
  through a login in the api role.
- Never read identity by name; never grant a routine to `PUBLIC` (the deploy
  revokes it anyway, and `test_exact_grants.sql` fails on extra grants).
- A handler that calls a cross-organization kernel is never `public`.
- A route whose request or response carries a credential sets
  `'autoLog', false`: the exchange log keeps bodies in plain text, the
  database keeps only the credential's hash.
- Return the whole entity from a write; build lists in one set-based query.
- Pick the protocol by who calls; a second protocol for a caller another one
  already serves is a second contract to keep true.
- Every new declaration key needs an arm in the conformance tests.
