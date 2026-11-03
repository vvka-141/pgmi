---
title: "The transactional web boundary"
description: "Design record for the advanced template's API framework: what it is for, the law it obeys, and the shape that follows."
weight: 20
---

# The transactional web boundary

> **Status: IMPLEMENTED.** All eight layers are in code in
> `internal/scaffold/templates/advanced/lib/api/`. Layer 4 checks the top level
> of a declared request body and the type or allowed values of a declared query
> parameter; deeper constraints stay with the handler (see
> [Known residue](#known-residue)).
> This record states the thesis the advanced template's API framework exists to serve,
> the single architectural law that follows from it, and the shape that implements it.
> It records the delivery of epic PGMI-298 and is the standing answer to design
> questions about route matching, contract publication, and request canonicalization.
> Read it before changing how routes are matched.

## Thesis

The advanced template exists to be one thing:

> **A boundary where an HTTP request becomes a database transaction, with nothing in
> between.**

No application tier translating between two models. No ORM, no service layer, no
serialization round-trip. The request arrives, a transaction opens at a declared
isolation level, the handler runs, the transaction commits, the response leaves.

That is the whole value proposition, and it requires two properties to hold at once:

**1. Real web.** The framework must accept every spelling a real client may legitimately
produce. Clients, proxies, SDKs, browsers and `curl` each render the same request
differently, and HTTP is deliberately tolerant of that. A gateway that isn't is broken
for real traffic, and it fails as a 404 that looks like a missing route.

**2. Transactional consistency.** Each request must map to exactly one resource identity,
one handler, and one transaction with declared isolation and declared read-only status.

## Why these are not in tension

They look opposed. Tolerance admits ambiguity; transactions forbid it. The apparent
conflict dissolves once you notice that the web is flexible about two different things
to two different degrees:

| | flexibility | authority |
|---|---|---|
| **how a client spells a request** | high — percent-encoding, dot-segments, query order, header case | RFC 3986 §6.2.2 defines several of these as *equivalent*; Postel's law covers the rest |
| **what a resource is** | none — one URI, one resource | RFC 3986 §6.1 (equivalence is what caching, ETags, `Location` and idempotency are built on) |

The flexibility lives in the **input funnel**. It does not live in the **identity**.
HTTP always assumed a stable identity behind a URL — it simply never forced anyone to
write it down. Caches, conditional requests and optimistic concurrency all depend on it.

This yields the resolution:

> **Be liberal at the funnel. Be exact at the identity.**

Canonicalization is what connects them: reduce every valid spelling to exactly one
identity, once, centrally, before anything else observes the request.

### Transactions need this more than plain HTTP does

Plain HTTP degrades gracefully under fuzzy identity — you get a cache miss. Transactional
consistency does not degrade; it breaks. If `/orders/42` and `/orders/42/` resolve to
different route rows with different isolation floors, or an `If-Match` precondition is
evaluated against one spelling while the write commits under another, the 428/40001
machinery in `lib/api/09-gateways.sql` is unsound.

Canonicalization is therefore not a tax the framework pays to OpenAPI. It is a
precondition of the guarantee the framework exists to make.

### What OpenAPI actually did

OpenAPI did not introduce a constraint the web lacks. It **transcribed an invariant the
framework already depended on**, and in doing so exposed that the invariant was implicit
and unenforced. That is a service, not an imposition — but it means the answer is to give
identity its own field, not to weaken the matcher.

## The law

One organizing principle governs the whole framework:

> ## One declaration. Many derivations.
> ### Nothing declared twice. Nothing enforced undeclared. Nothing declared unenforced.

A handler declares its contract **once**. Everything else is derived from that
declaration:

- the router (what matches)
- the OpenAPI document (what clients are promised)
- the MCP tool descriptor
- auth enforcement
- transaction policy (isolation floor, read-only)
- test probes

Three corollaries, each of which is independently violable:

| corollary | violation looks like |
|---|---|
| **Nothing declared twice** | two sources of truth drift apart |
| **Nothing enforced undeclared** | behavior invisible to clients; the spec understates reality |
| **Nothing declared unenforced** | the published contract lies |

### The law explained the bug cluster

These were filed independently, across separate reviews, as unrelated defects. Under the
law they are one defect class, and each was closed by restoring the law rather than by a
local patch:

| ticket | violation | resolution |
|---|---|---|
| PGMI-277 | OpenAPI declared `bearerAuth`; gateway never read `Authorization` — **declared, unenforced** | the spec advertises `loginSession` and `organizationSession` on `x-login-session` and `x-organization-session`, the headers the gateway reads |
| PGMI-268 | `requiresAuth` accepted any well-formed `x-user-id` without resolving a user — **declared, unenforced** | `rest_invoke` gates on `api.current_principal_id()`, a resolved enabled person |
| PGMI-293 | MCP `outputSchema` advertised; nothing obliged the handler to return `structuredContent` — **declared, unenforced** | the conformance harness fails the deploy when a tool declaring `outputSchema` returns none |
| PGMI-290 | OPTIONS always 405; CORS preflight could never succeed — **behavior undeclared** | OPTIONS on a matched path answers 204 with `Allow`, before identity |
| query parameters | read via `api.query_params()`, absent from the spec entirely — **enforced, undeclared** | declared with the `querySchema` key, enforced as 400, published as `in: query` |
| path parameter names | a hand-written name array kept in sync with the regex's capture-group count — **declared twice** | removed; names come from the declared `path` |
| `address_regexp` | served matching *and* OpenAPI path generation — **one field, two contracts** | `canonical_path` carries identity; `address_regexp` only matches |

A framework whose contract and behavior can disagree is not a contract. The law is what
makes the disagreement impossible to express.

### The law is tested, in both directions

`lib/__test__/test_contract_conformance.sql` walks the registry, so a route or tool
added later is covered without editing the test:

- **Declared ⇒ enforced.** A non-public `access` (or `permissions`) declaration refuses both no identity and an identity that
  resolves to no user; every header security scheme the spec advertises is one the
  gateway reads; `produces` and `outputSchema` hold for every successful GET and for
  every MCP tool that declares an output schema; required query parameters are refused
  when absent; `readOnly` and isolation floors are refused in a transaction that does
  not meet them.
- **Enforced ⇒ declared.** The MCP listings equal the MCP registry; every request header
  the gateway maps into the session is either an advertised security scheme or named as a
  trust-boundary header (one a trusted proxy sets and a client must never send, so
  publishing it would invite forgery).

`lib/__test__/test_openapi_agreement.sql` covers the REST side of "enforced ⇒ declared":
every registered route is published and every published path has a route behind it;
every published operation, parameterized paths included (sent as the route's probe),
resolves to a handler; and every published path answers OPTIONS.

## The shape

Eight layers, each with exactly one job. Every defect above was a layer doing another
layer's job.

```
 1. Ingress            raw request, as spelled by the client
 2. Canonicalize       normalize -> exactly one identity
 3. Resolve            identity -> handler, unambiguous by registration-time proof
 4. Enforce contract   auth, querySchema, negotiation, preconditions
 5. Open transaction   declared isolation, declared read-only
 6. Execute            four-phase handler body
 7. Shape response     status, headers, ETag, problem+json
 8. Publish            OpenAPI / MCP / registry, derived from the declaration
```

Where each layer lives:

| layer | implementation |
|---|---|
| 1. Ingress | `api.rest_invoke(method, url, headers, content)` in `09-gateways.sql` |
| 2. Canonicalize | `api.canonical_path(api.url_path(url))` (`07-helpers.sql`), applied once at the top of `rest_invoke` |
| 3. Resolve | `internal.rest_route_lookup`: the most specific matching route; registration (`08-registration.sql`) refuses the pairs the ranking cannot order |
| 4. Enforce contract | `rest_invoke`: 401 on no resolved user, 400 on the querySchema, 428 on isolation floor and read-only, 415/406 on negotiation |
| 5. Open transaction | the client gateway resolves `api.rest_route_policy` before `BEGIN`; `rest_invoke` only reads and fails closed |
| 6. Execute | the registered handler function |
| 7. Shape response | `internal.finalize_response_headers`, `api.problem_response` |
| 8. Publish | `api.openapi_document()` from `canonical_path` and `query_schema` (`11-openapi.sql`); MCP listings from `api.mcp_route` |

**Layer 2 is the funnel.** All spelling tolerance lives here and nowhere else. A route
author must never hand-write slash or query tolerance into a pattern — that is the
symptom of tolerance leaking out of layer 2 into layer 3.

Layer 2 does two different kinds of work, and **conflating them is a documented trap**:

| RFC-mandated (RFC 3986) | pgmi policy (our choice) |
|---|---|
| §6.2.2.1 hex digits in percent-triplets are case-insensitive → uppercase; scheme/host lowercase, **path not case-folded** | strip a trailing `/` except at root |
| §6.2.2.2 decode percent-encoded **unreserved** characters only | collapse duplicate slashes |
| §6.2.2.3 remove dot-segments (via §5.2.4) | insert a leading `/` when absent |
| §6.2.3 for `http`, an empty path normalizes to `/` | |

Under RFC 3986, `/a` and `/a/` are **different URIs** — the RFC does not define
trailing-slash removal, duplicate-slash collapsing, or leading-slash insertion as
normalizations. Those three are deliberate pgmi tolerance decisions: defensible,
consistent with mainstream frameworks, and *not* justifiable by citing the RFC. Anyone
who checks the citation will find it false and may "correct" the behavior away.

**Layer 3 is the identity.** Exact by construction. Route resolution must not depend on
registration order; ranking plus one registration refusal, below, removes that
dependence for every pair of routes.

**Layer 8 is derived, never authored.** The spec is a projection of the declaration, not
a parallel artifact to be kept in sync.

### Why the matcher is not stricter

The funnel is liberal and the identity is exact. Strictness belongs to the identity, and
it is expressed as a proof over identities, not as a restriction on how patterns may be
spelled.

A real prefix-catch-all bug (`^/hello` swallowing `/helloworld`) was once fixed by
requiring every route regex to be anchored `^...$`. The bug was real; the instrument was
wrong. Anchoring is a syntactic proxy for a semantic property: it outlawed legitimate
spelling tolerance as collateral, and it still accepted `^/users/.*$` and
`^/users/([0-9]+)$` sitting on top of each other. The anchoring guard gave way to a
proof over identities, and then to path templates with a complete ranking, below.

This requirement has been lost more than once, each time by a contributor re-deriving a
strict matcher from first principles. Before tightening route matching, check that the
tightening is a proof about identities and does not reject a valid spelling of a path.
`lib/__test__/test_route_spelling.sql` is the guard: if it goes red, the funnel has been
narrowed.

### Route declaration: identity is declared, matching is derived

A handler declares its canonical identity, and nothing else:

```sql
'path', '/users/{id}'
```

From that the framework derives the matcher (`api.path_template_to_regex`, each
`{param}` becoming one `([^/]+)` segment), the parameter names, and the OpenAPI path.
The author writes no regex, keeps nothing in sync, and gets full spelling tolerance from
layer 2 for free. The template is the only declaration: a `uri` regex, an `example`
and a `version` key are registration errors.

A parameter may carry a type: `{id:uuid}`, `{n:int}`; plain `{name}` is text. The types
live in `api.rest_route.path_params`, are part of `api.catalog_version()`, and publish
as typed OpenAPI parameters. The gateway hands the values to the handler by name,
percent-decoded, as `(request).path_params`, and answers 400 `pgmi.invalid_path_parameter`
to a malformed typed value, after the access decision so a stranger still gets 401.

Registration refuses a template that cannot be ranked:

- a segment that mixes literal text and a parameter (`{name}.json`, `readme.{ext}`),
  named in the message;
- a parameter name used twice;
- a template not in canonical form (trailing or doubled slash, dot segment, query).

### Ranking: a literal segment beats a parameter

Two templates can match one path: `/projects/stats` and `/projects/{id}` both match
`/projects/stats`. Among the routes matching a path and a method, the most specific
wins: `api.rest_route.specificity` holds one digit per segment, `1` for a literal and
`0` for a parameter (`internal.route_specificity`), and `internal.rest_route_lookup`
orders by it first. The first segment where two such routes differ decides, so
`/shops/main/{section}` beats `/shops/{shop}/orders` on `/shops/main/orders`, in either
registration order.

The ranking is total once one more pair is refused. Two templates that match one path
have the same number of segments and equal literals wherever both are literal. If one
has a literal where the other has a parameter, their specificities differ and the
ranking orders them. Otherwise they have the same *shape* (`internal.route_shape`,
the template with parameter names erased), and registration refuses the second of one
shape with a common method. `lib/__test__/test_route_anchoring.sql` checks the property
over every route registered.

Each route also stores a *probe*, `api.rest_route.probe_path`: the template with each
parameter filled in by a valid value of its type. The conformance tests call every route through it.

### What a template cannot say

Segment alternation, optional segments, constrained or multi-segment parameters and
case-insensitive matching are not expressible. Each is one route per path, or a
handler that validates its own parameter. The regex escape hatch that once covered them
made the ranking unprovable: a public `^/x/([0-9]+)$` and an authenticated `^/x/.*$`
both registered, and `/x/5` reached the catch-all.

### Query strings are declared, not matched

A regex over a raw query string is the over-strict-matcher mistake one layer down:
`?a=1&b=2` and `?b=2&a=1` are the same request but two different strings, before
considering `%20` vs `+` and repeated keys. Layer 2 therefore strips the query before
matching, and query parameters are declared structurally, as a JSON Schema object in
the dialect of `inputSchema`:

```sql
'querySchema', jsonb_build_object('type', 'object',
    'required', jsonb_build_array('format'),
    'properties', jsonb_build_object('format', jsonb_build_object('type', 'string')))
```

It is stored as `api.rest_route.query_schema`. Order-insensitive and encoding-safe by
construction: `rest_invoke` parses the query and answers 400 `pgmi.invalid_query` with
`invalid-params` when a required parameter is absent or a value does not fit its type,
`enum`, range or format, before the transaction checks. Registration refuses any keyword
the gateway could not enforce, so the spec never promises a check that does not run. `api.openapi_document()` publishes each entry as an `in: query` parameter and
adds a 400 response. Variant selection stays handler-side via `api.query_params()` /
`api.query_params_multi()`, which is where every mainstream framework puts it.

## Decisions taken

| decision | choice | rationale |
|---|---|---|
| Query string in matching | stripped before match (layer 2) | RFC 3986 §3.4 makes query a distinct component; matching a path pattern against path+query is a category error. Present since v0.7.0 and correct. |
| Fragment in matching | **not** stripped — a `#` makes the target unroutable | RFC 9110 §4.2.1: the `http`/`https` grammar has no fragment, so no compliant client can send one. Stripping it would be tolerance pointed the wrong way — `/admin#x` would resolve to the route for `/admin`. A fragment behind a query is already removed with the query. |
| Trailing slash | accepted, normalized away — not redirected | **pgmi policy, not RFC.** A 308 preserves method and body but costs a round trip; for a machine-facing API silent acceptance is friendlier. |
| Duplicate slashes, missing leading slash | collapsed / inserted | **pgmi policy, not RFC.** Tolerance for spellings real clients emit. |
| Path case | **case-sensitive** | RFC 3986 §6.2.2.1 normalizes case only for scheme, host, and percent-triplet hex digits. Lowercasing paths would be wrong. |
| Percent-encoding | unreserved characters decoded, hex digits uppercased, before match | RFC 3986 §6.2.2.1–2. Also a security boundary: encoded dot-segments must not survive into layer 3. Reserved characters stay encoded — decoding `%2F` would forge a segment boundary. |
| Dot-segments | removed before match | RFC 3986 §6.2.2.3. Normalizing *after* an auth decision is a bypass. |
| Empty path | normalized to `/` | RFC 3986 §6.2.3 (scheme-based, for `http`). |
| Route declaration | `path` template only | Identity is declared once; matcher, names and OpenAPI path are derived. |
| Route regex | removed | A hand-written regex made the ranking proof incomplete. |
| Route order | specificity, then registration order | A literal segment beats a parameter; two routes of one shape and a common method are refused, so registration order never decides. |
| Route versioning | removed; version in the path | No route used it, OpenAPI cannot publish a header-selected version, and `Vary` on version headers fragments shared caches. |
| Anchoring guard | replaced by semantic proof | `^...$` is a syntactic proxy for a semantic property, and it outlawed legitimate tolerance as collateral. |
| OPTIONS | 204 with `Allow` on any matched path, before identity | A CORS preflight carries no credentials by definition and needs a 2xx. |
| Wrong method | 405 with `Allow` listing every method of the path, whoever asks | Method sets are public. 401 versus 404 already reveals that a path exists, and hiding methods would break CORS preflight for authenticated routes. |
| OpenAPI document | filtered by caller for readability, not as a boundary; ETag carries the caller class (`anon` / `auth`) | A 304 must never hand an authenticated copy to an anonymous caller. The access decision guards an operation, not its absence from the document. |

## Known residue

The eight layers are in place. These are the places where the law still holds only by
convention or only for the cases the proof can see:

- **Schemas are checked at the top level only.** `rest_invoke` checks a POST, PUT or
  PATCH body against the route's `inputSchema` with the same function the MCP gateway
  uses for tool arguments (`internal.json_schema_errors`): the root type, required keys,
  and the JSON type of each top-level property. A declared query parameter's `schema` is
  checked for `integer`, `number`, `boolean` and `enum`. Nested schemas, formats, ranges
  and patterns are published but checked only by the handler's validate phase.

## Non-goals

- **Routing on query parameters.** Structurally brittle; declared and enforced instead.
- **Backward compatibility with hand-written path parameter names.** No user base to
  protect; the metadata key was removed outright and names come from the declared `path`.

## History

The framework drifted from this thesis in one commit, not gradually, and was brought back
to it in two passes.

- **v0.7.0 (2025-12-24)** — `api.url_path()` ships. Query stripping is original design.
- **v0.10.0 (2026-05-04, `87ebe32a`)** — `rest_invoke` matches on `api.url_path(p_url)`.
  Layer 2 tolerance, correctly centralized.
- **2026-07-25 (`2c82bae4`, PGMI-214)** — a real prefix-catch-all bug (`^/hello` swallowing
  `/helloworld`) is fixed by mandating `^...$`. The bug was real; the instrument was a
  syntactic sledgehammer that also outlawed legitimate spelling tolerance. This is the
  drift.
- **OpenAPI generation** — never drift, but built on `address_regexp`, conflating layers
  3 and 8 and making the identity implicit.
- **2026-07-28 (PGMI-299..302)** — `api.canonical_path()` becomes layer 2; the `path`
  declaration and `canonical_path` column give identity its own field and retire the
  hand-written parameter-name array; the anchoring guard is replaced by the
  self-consistency and non-overlap proof; the spelling suite pins the funnel.
- **2026-09-24 (PGMI-380, 289, 290, 293, 304, 305)** — parameterized routes join the
  overlap proof through `probe_path`; OPTIONS preflight and caller-filtered 405 (public again since
  PGMI-398) and OpenAPI; MCP `outputSchema` obliges `structuredContent`; the query-string contract is
  declared, enforced and published; the conformance harness tests the law in both
  directions.
- **2026-10-31 (PGMI-448, 398, 399, 400)** — route versioning removed (version in the
  path, `/v2/projects`); method sets are public: 405 and OPTIONS list every method of the
  path. Path templates are the only declaration, with typed parameters delivered as
  `path_params`; a literal segment outranks a parameter,
  and registration refuses mixed segments and two routes of one shape with a common
  method, which makes the ranking total.

## See also

- Epic PGMI-298 — delivery of this shape
- `internal/scaffold/templates/advanced/lib/api/` — implementation
- [Session API](../session-api.md), [API versioning](api-versioning.md)
