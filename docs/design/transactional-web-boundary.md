---
title: "The transactional web boundary"
description: "Design record for the advanced template's API framework: what it is for, the law it obeys, and the shape that follows."
weight: 20
---

# The transactional web boundary

> **Status: DESIGN — partially implemented.**
> This record states the thesis the advanced template's API framework exists to serve,
> the single architectural law that follows from it, and the target shape. It is the
> reference for epic PGMI-298 and the standing answer to design questions about
> route matching, contract publication, and request canonicalization.

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

### The law explains the existing bug cluster

These were filed independently, across separate reviews, as unrelated defects. Under the
law they are one defect class:

| ticket | violation |
|---|---|
| PGMI-277 | OpenAPI declares `bearerAuth`; gateway never reads `Authorization` — **declared, unenforced** |
| PGMI-268 | `requiresAuth` accepts any well-formed `x-user-id` without resolving a user — **declared, unenforced** |
| PGMI-293 | MCP `outputSchema` advertised; nothing obliges the handler to return `structuredContent` — **declared, unenforced** |
| PGMI-290 | OPTIONS always 405; CORS preflight can never succeed — **behavior undeclared** |
| query parameters | read via `api.query_params()`, absent from the spec entirely — **enforced, undeclared** |
| `pathParams` | hand-synced to the regex's capture-group count (`08-registration.sql:227`) — **declared twice** |
| `address_regexp` | serves matching *and* OpenAPI path generation — **one field, two contracts** |

A framework whose contract and behavior can disagree is not a contract. The law is what
makes the disagreement impossible to express.

## The shape

Eight layers, each with exactly one job. Every defect above is a layer doing another
layer's job.

```
 1. Ingress            raw request, as spelled by the client
 2. Canonicalize       normalize -> exactly one identity
 3. Resolve            identity -> handler, provably unambiguous
 4. Enforce contract   auth, negotiation, input schema, preconditions
 5. Open transaction   declared isolation, declared read-only
 6. Execute            four-phase handler body
 7. Shape response     status, headers, ETag, problem+json
 8. Publish            OpenAPI / MCP / registry, derived from the declaration
```

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

**Layer 3 is the identity.** Exact by construction, and *provably* unambiguous: route
resolution must never depend on registration order.

**Layer 8 is derived, never authored.** The spec is a projection of the declaration, not
a parallel artifact to be kept in sync.

### Route declaration: identity is declared, matching is derived

The minimum a handler declares is its canonical identity:

```sql
'path', '/users/{id}'
```

From that the framework derives the matcher, the parameter names, and the OpenAPI path.
The author writes no regex, keeps nothing in sync, and gets full spelling tolerance from
layer 2 for free. This is **simpler than the status quo**, which requires a hand-written
anchored regex *plus* a parallel `pathParams` array *plus* keeping their arity aligned.

An advanced author may additionally supply a matcher:

```sql
'path', '/orders/{id}/confirm',
'uri',  '^/orders/([0-9a-f-]{36})/(?:confirm|accept)$'
```

Registration then proves two things at deploy time:

1. **Self-consistency** — the route's own canonical path matches its own regex.
2. **Non-overlap** — no other route's canonical path matches this regex.

Self-consistency is a precondition for non-overlap being meaningful: without it, a route
could declare an identity its own matcher rejects, and the overlap test would compare
against a fiction. Together they are strictly stronger than the current syntactic
`^...$` check, which catches neither — it accepts `^/users/.*$` and `^/users/([0-9]+)$`
sitting on top of each other.

Once non-overlap is proven, `ORDER BY sequence_number DESC` stops being load-bearing.
Routing becomes order-independent, which is the actual fix for the class of bug PGMI-214
was reaching for.

### What stays regex-only

The escape hatch earns its keep. These are not expressible as a path template:

- segment alternation — `^/v(1|2)/orders$`
- optional segments — `^/orders(/archive)?$`
- constrained parameters — `^/reports/([0-9]{4}-[0-9]{2})$`
- greedy multi-segment — `^/files/(.+)$`
- case-insensitive matching

Known and accepted cost: alternation and optional segments correspond to *several*
OpenAPI paths. Such a route declares multiple canonical paths or accepts a lossy spec.
This is inherent to the expressiveness, not to this design — it is true today, silently.

### Query strings are declared, not matched

A regex over a raw query string is the over-strict-matcher mistake one layer down:
`?a=1&b=2` and `?b=2&a=1` are the same request but two different strings, before
considering `%20` vs `+` and repeated keys. Layer 2 therefore strips the query before
matching, and query contracts are declared structurally:

```sql
'query', '[{"name":"format","required":true}]'
```

Order-insensitive and encoding-safe by construction, enforced at layer 4, and projected
to OpenAPI `in: query` parameters at layer 8. Variant selection stays handler-side via
`api.query_params()`, which is where every mainstream framework puts it.

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
| Route regex | retained as an advanced escape hatch | Full regex is a genuine differentiator over path-DSL frameworks; the fix is to stop it doing layer 8's job, not to remove it. |
| Anchoring guard | replaced by semantic proof | `^...$` is a syntactic proxy for a semantic property, and it outlawed legitimate tolerance as collateral. |

## Non-goals

- **Routing on query parameters.** Structurally brittle; declared and enforced instead.
- **A path DSL replacing regex.** The template is the default, not the ceiling.
- **Backward compatibility with hand-written `pathParams`.** No user base to protect;
  derived names replace it outright.

## History

The framework drifted from this thesis in one commit, not gradually.

- **v0.7.0 (2025-12-24)** — `api.url_path()` ships. Query stripping is original design.
- **v0.10.0 (2026-05-04, `87ebe32a`)** — `rest_invoke` matches on `api.url_path(p_url)`.
  Layer 2 tolerance, correctly centralized.
- **2026-07-25 (`2c82bae4`, PGMI-214)** — a real prefix-catch-all bug (`^/hello` swallowing
  `/helloworld`) is fixed by mandating `^...$`. The bug was real; the instrument was a
  syntactic sledgehammer that also outlawed legitimate spelling tolerance. This is the
  drift.
- **OpenAPI generation** — never drift, but built on `address_regexp`, conflating layers
  3 and 8 and making the identity implicit.

## See also

- Epic PGMI-298 — delivery of this shape
- `internal/scaffold/templates/advanced/lib/api/` — implementation
- [Session API](../session-api.md), [API versioning](api-versioning.md)
