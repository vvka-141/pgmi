---
title: "The Request Becomes a Transaction"
date: 2026-08-21
author: "Alexey Evlampiev"
description: "A transactional API has two halves: a network edge and a transactional operation. Keep the first at that edge; give the second to PostgreSQL, where its authority already lives — and every declared outcome of every operation becomes provable in the same transaction."
weight: 3
---

# The Request Becomes a Transaction

*A transactional API has two halves: a network edge and a transactional operation. Keep the first at that edge; give the second to PostgreSQL, where its authority already lives — and every declared outcome of every operation becomes provable in the same transaction.*

*By Alexey Evlampiev*

**Abstract.** The last five years consolidated storage into PostgreSQL: the queue, the cache, the search index, and the vector store moved in, one "just use Postgres" argument at a time. The API tier did not move — and the debate over whether it should is usually fought across the wrong boundary. A transactional API has two halves. The network edge authenticates the caller and adapts HTTP. The transactional boundary resolves the operation, validates its input, authorizes it against current state, executes the transition, and shapes the result. Convention puts the first half in a gateway and the second in an application framework — even though every authoritative decision in the second half already terminates in PostgreSQL. This article moves that boundary to where its authority lives, focusing on APIs whose valuable behavior is transactional decision-making over PostgreSQL state. The unit of design becomes the *transactional operation*: a named database operation with a typed contract, an authorization policy, a declared transaction, an implementation, and tests — and each protocol surface, starting with REST, becomes a binding to it. The payoff is one authority, one transaction, one executable proof: a test can invoke an operation end to end, assert on the response and the state transition in the same snapshot, and roll everything back.

![The two halves of a transactional API: a dashed network edge — TLS, credential verification, rate limiting, pooling — adapts the protocol and holds no business rule; an amber arrow, the request becomes a transaction, opens the operation's declared transaction inside PostgreSQL. At the center sits the transactional operation, the unit of design — typed contract, authorization policy, transaction policy, tests, and a four-phase handler: materialize, validate, probe, transition. To its right, REST routes, OpenAPI operations, and MCP tools are bindings — projections of one catalog. To its left, the executable proof invokes the same operation end to end inside the deploy transaction, asserts on response and state in one snapshot, then rolls back; a failing declared outcome refuses the commit. Everything stands on PostgreSQL state — constraints, row-level security, indexes — where the operation catalog itself is rows. One authority, one transaction, one executable proof.](/pgmi/docs/diagrams/a06-request-becomes-transaction.drawio.svg)

The argument runs in six parts: where the conventional stack splits the transactional boundary and what the split costs; the inventory of primitives PostgreSQL already provides for it; what a database-resident boundary buys that a framework tier cannot; a working model in plain SQL; where the design fits and where it does not; and the test gate that turns the design from a preference into a discipline.

But the destination fits in a few lines, so here it is before the argument — Part IV builds every piece this sketch uses:

```sql
BEGIN;
SELECT api.invoke('POST', '/orders', ''::hstore,
                  '{"customerId": "…", "total": 99.95}');
-- assert on the response — and on the row it created,
-- both visible in the same open snapshot
ROLLBACK;   -- the proof ran; nothing persists
```

The point is not that an API can be invoked from SQL. It is that the response and the state transition are still inside one open transaction when the assertion runs — no second interface, no orchestration between observations, nothing yet committed. Part VI makes this same shape a deployment's commit condition. The rest of the article explains why this shape is worth building, what PostgreSQL already provides for it, and where it stops making sense.

## Part I — The transactional boundary has two authorities

### The consolidation stopped one layer short

The "just use Postgres" movement is usually an argument about *storage*. Its flagship texts count engines: the queue that could have been a table with `FOR UPDATE SKIP LOCKED`, the cache that could have been an unlogged table, the search cluster that could have been `tsvector`, the vector store that could have been [pgvector](https://github.com/pgvector/pgvector). The 2026 restatement that reached the Hacker News front page — [It's 2026, Just Use Postgres](https://www.tigerdata.com/blog/its-2026-just-use-postgres) — opens with "You now have seven databases to manage" and closes with one. It contains no argument about logic, endpoints, or APIs.

So a team that follows the advice ends up in a curious place. One database now holds the relational state, the embeddings, the geospatial columns, the queue, and the full-text index — and in front of it still stands an application tier whose job is to translate HTTP into SQL and SQL back into HTTP, holding a second copy of the schema in classes, a second copy of the constraints in validators, a second copy of the authorization rules in middleware, and a transaction boundary that only approximately matches the business operation. The stores were consolidated. The logic that governs them was not.

The debate over that remaining tier is old, and it has a striking asymmetry. Read the recurring threads from 2019 through 2026 and the pattern holds: the case *for* moving logic into the database is argued in terms of correctness and data locality; the case *against* is argued almost entirely in terms of tooling, testing, deployment, and hiring. [The most thorough practitioner rebuttal of 2026](https://withblue.ink/2026/03/business-logic-does-not-belong-in-the-database) — a named engineer who moved logic into stored procedures and rolled it back — lists six objections: deployment coupling, weak debugging, testing needs a live database, vertical-only scaling, split languages, no canary releases. Five of the six are tooling and operations. The sixth (scaling) is real and gets its own section in Part V. What the rebuttal concedes without contest is the core: "The database engine is purpose-built for this." Even the strongest institutional statement of the case against carries an expiry date: ThoughtWorks held "logic in stored procedures" on its Technology Radar's HOLD ring from 2011 to 2013, on the grounds that the languages "lack expressiveness, are difficult to test, and discourage clean modular design" — an indictment of the tooling of 2011, since retired from the radar and never recalibrated against what exists now.

Neither side of that debate argues that the database is the wrong place for a *transaction*. That is the seam this article works.

Dimitri Fontaine put the honest version of the question in *The Art of PostgreSQL*: every SQL query already embeds part of the business logic, so the question is never *whether* logic lives in the database — it is *how much*. This article answers: for a transactional API, the part that decides what a request is allowed to mean — the operation's resolution, its validation, its authorization, and its state transition. Not the TLS termination, not the rate limiting, not the JWT parsing. The boundary between those two groups is drawn precisely in Part IV.

### Which API responsibilities belong with the transaction?

Strip any HTTP framework — ASP.NET Core, FastAPI, Spring, Express — to its responsibilities and a short list remains:

| Concern | What it operates on |
|---|---|
| Message model | method, URL, headers, body, status code |
| Routing | a catalog of (pattern, method, version) → handler |
| Content negotiation | `Content-Type`, `Accept` against a declared contract |
| Validation | request fields against types and business rules |
| Authorization | the caller's identity against permissions over data |
| Transaction management | the unit of work around the state change |
| Serialization | rows and objects to JSON and back |
| Error taxonomy | exceptions to status codes |
| Contract publication | OpenAPI, discovery, versioning |
| Testing | proving all of the above per endpoint |

Now sort the rows by which of the two authorities each belongs to. A few are genuinely protocol: content negotiation is a conversation about representations, and the message model's wire form belongs to HTTP. The rest belong with the transactional operation — not because each requires a transaction, but because each *describes or governs the operation that runs in one*, and its authoritative version already exists inside the database: the routing catalog declares which operations exist; validation restates the schema's types and constraints; authorization is a predicate over rows; the transaction is the database's native unit; serialization converts between two representations of the same tuple; the error taxonomy maps constraint violations — database events — onto HTTP statuses; the contract describes, ultimately, what the data model permits; and the tests are evidence about all of it. For every row in that second group, the framework tier maintains the second copy.

### The split-authority tax, in the frameworks' own words

Ted Neward named the underlying defect in 2006, in [the essay that gave ORMs their unflattering metaphor](https://blogs.newardassociates.com/blog/2006/the-vietnam-of-computer-science.html): "the metadata to the system is held fundamentally in two different places: once in the database schema, and once in the object model." *Authority* here means something specific: any layer that can independently change whether an operation is admitted, what it executes, or what the caller observes is exercising authority over the API's behavior. The constraint stays the law over durable state — but a validator that rejects what the constraint would accept, or accepts what it would reject, is exercising that same authority a second time. Everything below is that defect and its close relative — the same rule held in two places, and one operation's execution fragmented across the boundary between the copies — and the frameworks document both themselves.

**Joins.** Prisma's documentation states that its default query strategy "sends multiple queries to the database (one per table) and joins them on the application level" — and that this was [the only strategy the ORM supported before February 2024](https://www.prisma.io/blog/prisma-orm-now-lets-you-choose-the-best-join-strategy-preview). A mainstream ORM re-implemented the join — the operation query planners have optimized for four decades — in application memory, as the default. GitLab maintains [a dedicated test framework](https://docs.gitlab.com/ee/development/database/query_recorder.html) whose documented purpose is that without it, "a new feature which causes an additional model to be accessed can silently reintroduce the problem." The N+1 defect is not a bug in these stacks; it is a permanent tendency that requires standing tooling to suppress.

**Validation.** Rails' API documentation says of its uniqueness validator: it "does not guarantee the absence of duplicate record insertions, because uniqueness checks on the application level are inherently prone to race conditions," and recommends a database unique index as "the best way to work around this problem." [Django documents](https://docs.djangoproject.com/en/5.2/ref/models/instances/) that `full_clean()` "will not be called automatically when you call your model's save() method." The app-tier validator is a convenience copy; the constraint is the law. Two copies, and the copies drift.

**Transactions.** GitLab's development handbook [prohibits network calls inside database transactions](https://docs.gitlab.com/development/database/transaction_guidelines/) — "ideally, a transaction should only contain database statements" — enforced by code review, because no runtime can ban arbitrary I/O inside a transaction wholesale. Sidekiq's wiki warns of jobs racing records "that ha[ve] not committed yet" — a defect class Rails took until version 8.2 to close by default (`enqueue_after_transaction_commit`). Once an operation crosses a single ACID boundary, the [transactional outbox](https://microservices.io/patterns/data/transactional-outbox.html) and [saga](https://learn.microsoft.com/en-us/azure/architecture/patterns/saga) patterns become necessary tools — a literature of idempotency and compensation preserving part of what a local commit supplies whole. They are the right tools for genuinely distributed work; the defect is reaching for them when the work never had to leave one transaction.

The cost is measurable when it goes wrong. In December 2025, IBM's MCP gateway [documented the failure mode precisely](https://github.com/IBM/mcp-context-forge/issues/1706): under 1,000 concurrent users, 402 database connections — 65% of the pool — sat idle-in-transaction while exactly one connection executed queries, because sessions stayed attached across network I/O measured in seconds to minutes. Database work per request: ~20ms. The transaction boundary was in the application, so the application's latency became the database's concurrency problem. The obvious objection is that this is a bug, not an architecture — holding a session across network I/O is exactly what competent teams forbid. That is the point: the architecture makes the bug writable, and a review rule is all that stands between it and production. A boundary where the transaction opens and commits inside the database cannot express it.

**Caching.** When state must be read faster than the primary can serve it, the stateless tier reaches for Redis, and consistency becomes a distributed-systems project. Meta's TAO team [reported](https://engineering.fb.com/2022/06/08/core-infra/cache-made-consistent/) that raising cache consistency from six nines to ten nines took years of dedicated invalidation engineering, because "data gets mutated on both read (cache fill) and write (cache invalidation) paths," and "this exact conjunction makes many race conditions possible." In 2025, authentik [removed Redis as a required dependency](https://goauthentik.io/blog/2025-11-13-we-removed-redis/) and moved caching into PostgreSQL — reporting a simpler architecture and two to three fewer queries per request, while conceding that "PostgreSQL was not built for this; Redis PubSub was purpose built for it" and that WebSocket relay performance decreased. The concession matters and Part V returns to it. The structural point stands: a cache outside the transaction can never be invalidated *in* the transaction.

**Testing.** The stack that holds its logic in the application tier tests against a mock, an in-memory stand-in, or a fixture database that is none of the above at once. Docker's own Testcontainers guide documents the false confidence plainly: "Tests passing with H2 don't guarantee they'll work in production" — `INSERT … ON CONFLICT` parses in PostgreSQL and errors in H2. The industry's answer was to put a real database in the test loop — Testcontainers' Docker Hub pulls [doubled from 50 to 100 million in a single year](https://www.docker.com/blog/docker-whale-comes-atomicjar-maker-of-testcontainers/). That is the right direction — and it prompts the question this article ends on: if the honest test already requires the real database, what exactly is gained by keeping the logic somewhere else?

Five surfaces, two taxes. Some of these costs come from defining the same rule twice; the others from splitting one operation across a network boundary. Validation and the test double pay the first — the **split-authority tax**: the same semantic rule held in two places, and the copies drift. Joins, transactions, and caching pay the second — the **split-execution tax**: one logical operation decomposed across that boundary — the join reassembled in process memory, the commit point stretched around I/O, the invalidation outside the transaction that made the entry stale. They are siblings, not the same defect, and they have different remedies: consolidating authority removes the first by construction; only moving the boundary makes the second unexpressible — Part III keeps the two ledgers separate. The transactional boundary has two authorities, and the request path is where they argue.

A disciplined team can avoid much of this from the application tier: issue one chunky SQL statement instead of a chatty sequence, treat the constraints as the authority, keep transactions free of network waits. The argument of this article is not that frameworks make those disciplines impossible — it is that they leave them optional, enforced by review and vigilance. Moving the transactional boundary into the database turns the disciplines into invariants: a handler that runs inside the transaction cannot await the network, cannot validate against a different schema than it commits to, and cannot deploy apart from the constraints it depends on.

So the design that follows draws one line and defends it. Network concerns — TLS, sockets, credential verification, rate limiting, connection pooling — stay outside, in a gateway kept semantically narrow. The transactional API boundary — resolving the operation, validating the input, authorizing the caller, opening the declared transaction, executing the transition, shaping the response — moves inside, next to the state it governs. The unit that boundary is built from deserves a name, because the rest of the article hangs from it: a **transactional operation** — a named database operation with a typed contract, an authorization policy, a transaction policy, an implementation, and tests. A REST route is then a *binding* to the operation, not the operation itself — and so, it will turn out, are the OpenAPI entry and the MCP tool. ("REST" throughout means a resource-oriented transactional HTTP API; the uniform-interface debates are out of scope.)

The payoff, argued in the parts that follow, compresses to three properties: one authority, one transaction, one executable proof — where the proof runs over an operation's *declared outcomes*: each validation rejection, each authorization refusal, each failed state probe, each successful transition, each version-specific result. The third property is the one to hold onto: when the boundary moves, the same transaction becomes the implementation boundary, the authorization boundary, and the executable test boundary. Or, compressed to one sentence: the network tier adapts the protocol; the database decides what the request is allowed to mean. The sketch that opened the article is the third property in miniature — the rest of the machinery exists so that a seven-line proof can be written for every operation.

The scope of the claim, stated before the inventory: this architecture is for PostgreSQL-backed systems whose valuable behavior consists chiefly of transactional decisions over PostgreSQL state. It is not an argument for moving network I/O, media processing, streaming, orchestration, or arbitrary compute into the database — Part V prices those boundaries in detail. And one distinction runs through everything that follows, so it is worth two words now: *consolidation* benefits belong to PostgreSQL and reach any tier that connects to it; *placement* benefits exist only because of where the boundary sits. "I can issue the same SQL from Go" is true, and it is an argument about the first kind — Part III does the accounting.

## Part II — The API contract is data

The claim of this part is narrow and checkable: every primitive a transactional operation is built from — message, route, contract, authorization, transaction policy — can be made explicit, typed, transactional, queryable, and enforceable in PostgreSQL. Protocol semantics — HTTP parsing, TLS, transport — stay with the gateway and make no appearance here; this inventory covers the transactional half, where these representations become the authoritative declarations.

### The message is a composite value

HTTP messages are small algebraic structures, and PostgreSQL composite types express them directly:

```sql
CREATE SCHEMA api;   -- the boundary
CREATE SCHEMA app;   -- the domain
CREATE EXTENSION IF NOT EXISTS hstore;

CREATE DOMAIN api.http_status AS integer
    CHECK (VALUE BETWEEN 100 AND 599);

CREATE DOMAIN api.http_method AS text
    CHECK (VALUE ~ '^[A-Za-z0-9-]+$');

CREATE TYPE api.http_request AS (
    method  api.http_method,
    url     text,
    headers hstore,
    content jsonb
);

CREATE TYPE api.http_response AS (
    status_code api.http_status,
    headers     hstore,
    content     jsonb
);
```

Each field choice is deliberate.

**Headers are `hstore`** because an HTTP header block is, to a first approximation, a flat map of string keys to string values. [hstore](https://www.postgresql.org/docs/current/hstore.html) has carried exactly that shape as a contrib extension since 2006 — and a trusted one, installable without superuser, since PostgreSQL 13 — with indexable containment operators and a literal syntax that reads like a header block: `'content-type=>application/json, x-api-version=>2'::hstore`. Its structural limits — string values only, no nesting, unique keys — are very nearly the specification of the header field. (hstore is folklore-deprecated but not actually deprecated — no such statement exists in the PostgreSQL documentation; jsonb superseded it for *documents*, while for flat string maps it remains the tighter type.)

The approximation breaks in two narrow places. The first is repeated field lines: list-syntax fields (`Accept`, `Vary`, `Link`) comma-join losslessly into one value, and `Set-Cookie` — the one field RFC 9110 itself singles out as breaking that rule — does not, so cookie-setting belongs to the gateway that already owns CORS and TLS. The second is a discipline that travels with the choice: hstore keys are case-sensitive while HTTP names are not, so the boundary lowercases keys on store and on lookup — Part IV returns to this.

**The status code and the method are domains.** A [domain](https://www.postgresql.org/docs/current/sql-createdomain.html) is a named type with an attached predicate, checked at every conversion — `api.http_status` above makes an out-of-range status unrepresentable in a response value, which is why the composite uses the domain rather than a bare `integer`. It is the API field type the application tier keeps rebuilding as validator classes. PostgREST's [domain representations](https://docs.postgrest.org/en/latest/references/api/domain_representations.html) are the production proof: domains as public API field types, with casts defining their wire form. Both domains follow one rule worth naming: constrain to the *shape* the specification defines, never to what its registry currently lists. `http_status` enforces the 100–599 range, not the IANA status list; `http_method` enforces the shape of a method name — letters, digits, hyphen: a deliberately readable subset of RFC 9110's token grammar that covers every method ever registered, and still a grammar, because the rule forbids lists, not narrowing.

The registry is open, so a well-formed unregistered method stays representable and dispatch answers it with a 404 or 405, while a value no HTTP parser could have produced (an empty string, whitespace, a CR/LF smuggling attempt) cannot enter the message type at all. The tempting alternative — `VALUE IN ('GET', 'POST', …)` — would plant a second authority inside the type: the method allow-list already exists, per route, in the catalog, and the division of labor is exact — the type answers "does this fit the method grammar this boundary admits?", the catalog answers "do we serve it here?" — and widening the grammar (say, to the full `tchar` set) is editing one predicate, never migrating an enumeration. Keep that division and adopting a new method (WebDAV's `PROPFIND`, the draft `QUERY`) is a route row, not a type migration — and an unserved method is a 404, not an uncaught datatype error. Part IV puts a third domain to work as a business predicate declared exactly once.

**The body is `jsonb`** — for this article. A protocol-neutral model carries `bytea` and decodes at the edge of the handler, which is how a complete implementation supports JSON, XML, text, and binary payloads through one pipeline; the final section points at one. jsonb keeps every example here readable, and it is where PostgreSQL's investment has gone: PostgreSQL 16 shipped the SQL/JSON constructors (`JSON_OBJECT`, `JSON_ARRAYAGG`, `IS JSON`), and PostgreSQL 17 shipped [`JSON_TABLE`](https://www.postgresql.org/docs/current/functions-json.html) — a request body projected into a relational tuple source, in the `FROM` clause, in one declarative step. The row-to-JSON serialization layer the framework tier maintains is, in the database, a cast.

And when the contract itself should be enforced, not merely documented: [pg_jsonschema](https://github.com/supabase/pg_jsonschema) validates a jsonb value against a JSON Schema document inside a `CHECK` constraint. The OpenAPI request schema and the database constraint can be the same object — one declaration, not two copies.

The engine keeps moving toward this boundary. PostgreSQL 19 — in beta as this is written — adds `INSERT ... ON CONFLICT DO SELECT ... RETURNING`: create-or-return-existing in one statement — the core primitive of an idempotent POST, expressed where the uniqueness constraint lives.

### The router is a table

Every framework holds a route catalog: an in-memory trie or list, built at startup from annotations, rebuilt on every deploy of every instance, inspectable only through whatever debug endpoint the framework offers. Model it as data instead:

```sql
CREATE TABLE api.route (
    route_id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sequence_number bigint GENERATED ALWAYS AS IDENTITY,
    canonical_path  text NOT NULL,
    path_regexp  text NOT NULL,
    method_regexp   text NOT NULL DEFAULT '^(GET|POST|PUT|DELETE|PATCH)$',
    version_regexp  text NOT NULL DEFAULT '.*',
    handler         regprocedure NOT NULL,
    requires_auth   boolean NOT NULL DEFAULT true,
    read_only       boolean NOT NULL DEFAULT false,
    min_isolation   text
        CHECK (min_isolation IS NULL
               OR min_isolation IN ('read committed', 'repeatable read', 'serializable')),
    use_deferrable  boolean NOT NULL DEFAULT false
        CONSTRAINT route_deferrable_requires_serializable_read_only
        CHECK (NOT use_deferrable
               OR (read_only AND min_isolation IS NOT DISTINCT FROM 'serializable')),
    input_schema    jsonb,
    output_schema   jsonb,
    description     text NOT NULL
);
```

One flattening to acknowledge at first sight: this row carries both the *transactional operation* — handler, authorization, transaction policy, contract — and its REST *binding* — the path, method, and version patterns — collapsed into one table because one table teaches. Part III returns to the split; the mature shape keeps an operation catalog with per-protocol binding tables around it.

Routing is now a query, and everything a query implies follows for free. The catalog is transactional: a deploy that registers twelve routes and fails on the thirteenth leaves no half-registered router. It is queryable: "which routes declare a serializable floor, which serve writes, which still lack an output schema?" is a `SELECT`, not a code audit. It is provable: for a declared route grammar, a deploy-time assertion can verify that no two routes' patterns overlap for any method — turning match precedence from a load-bearing accident of registration order into a checked invariant. (The dispatch in Part IV breaks ties by newest registration; the proof's job is to make ties impossible wherever they would matter.) The proof is engineerable in proportion to the grammar: declared as structured path templates (`/orders/{id}`), with raw regex as a marked escape hatch, overlap and parameter extraction are decidable by construction; over arbitrary regex alone, the check degrades to pairwise probes against literals. Declare in the grammar, derive the regex.

And it scales the way data scales, provided it is treated as data: route *matching* is a query whose plan is yours to engineer — and it needs engineering, because the three-regex scan above is linear in the catalog: on the order of 165 milliseconds per request against ten thousand routes, measured, which is fine at a hundred routes and disqualifying at ten thousand. A large catalog therefore narrows before it matches — static routes hit an index on `canonical_path` directly; parameterized routes are pre-filtered by indexed generated columns (segment count, literal first segment) so the pattern match runs against a handful of candidates instead of the table. That is ordinary hot-query discipline, and it is available precisely because the router is a table with a planner behind it rather than a list rebuilt in every process.

`handler regprocedure` deserves a paragraph, including its limit. The route row references the handler function by identity, not by name string: registration resolves the name against the catalog, so a route can never be *registered* against a function that does not exist, and `CREATE OR REPLACE` preserves the function's OID, so redeploying a handler never strands its routes. What the type does not give you is a standing dependency — PostgreSQL does not track OIDs stored in table columns in `pg_depend` — so a later `DROP FUNCTION` succeeds silently and leaves a dangling reference that fails at request time. Closing that gap belongs in the same place as the overlap proof: a deploy-time assertion that every registered handler still resolves (`to_regprocedure(handler::text) IS NOT NULL`), run before the deploy commits.

This is not a novel idea, which is a strength. Oracle's mod_plsql served HTTP from stored procedures two decades ago, and its successor ORDS — shipping quarterly through 2026 — stores its route modules, templates, and handlers in database tables. On the PostgreSQL side, PostgREST's maintainers have said they *want* route ownership: "Relying solely on the proxy for this hurts our extensibility" ([issue #1909](https://github.com/PostgREST/postgrest/issues/1909)). The route-catalog-as-table has history behind it; what has been missing in the PostgreSQL world is treating it as the primary model rather than a generated artifact.

### Versioning is a predicate, not a fork of the codebase

Once routes are rows, API versioning stops being an architectural event. A version is one more matching predicate:

```sql
-- v1 continues to serve clients that send no version header.
-- v2 is a second row, matched when the client asks for it.
INSERT INTO api.route
    (canonical_path, path_regexp, method_regexp, version_regexp,
     handler, read_only, description)
VALUES
    ('/orders/{id}', '^/orders/([^/]+)$', '^GET$', '^2$',
     'api.get_order_v2(api.http_request)'::regprocedure, true,
     'Fetch one order, v2 response shape');
```

Two handler functions, two rows, one catalog. One discipline keeps the pair honest: the v1 row's catch-all `version_regexp` also matches `2`, so as written v2 would win only by being registered later — precedence-by-registration, the exact accident the overlap proof exists to eliminate. When a catch-all coexists with explicit versions, give the catch-all an explicit pattern (`^(1|)$`) so no route's selection depends on ordering. With that, the old version is never migrated, wrapped, or shimmed — it keeps running as the same function it always was, against the same data, under the same tests.

Header-selected representations then carry one HTTP obligation and one design choice. The obligation: a response chosen by a request header must say so — `Vary: x-api-version` — or a shared cache will hand a v2 body to a v1 client. The choice: path versioning (`/v2/orders` as another pattern row) trades header versioning's clean URI space for cacheability and linkability with no `Vary` at all. The catalog expresses both; the OpenAPI projection favors the path form, because a version selected by header has no standard OpenAPI representation — the one place this catalog is richer than the document derived from it.

Retiring a version is `DELETE FROM api.route WHERE version_regexp = '^1$' AND …` inside a deploy transaction, after the access log (also a table — see Part III) shows its traffic at zero. Version coexistence becomes explicit and local: each version is a row, and each is exactly as much code as its behavioral difference requires. What the catalog removes is the infrastructure-level branching; it does not remove the cost of maintaining genuinely different behavior — old field meanings, old response shapes, old invariants — it makes that cost visible, per row, and cheap to retire. For deeper divergence, the same pattern scales up a level: `api_v1` and `api_v2` as schemas, with handler resolution through the catalog — the mechanism [pgroll](https://xata.io/blog/pgroll-schema-migrations-postgres) proved for schema migrations (two live versions through per-version schemas of views) applies to API surfaces unchanged.

### Authorization executes where the data is

The framework tier authorizes in middleware: a predicate over claims, checked before the handler runs, enforcing rules *about* rows it has not yet fetched. PostgreSQL authorizes in the storage engine: [row-level security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html) attaches the predicate to the table, and no query path — no handler, no ad-hoc report, no future endpoint added by someone who forgot the middleware — can return a row the policy excludes. Concretely:

```sql
ALTER TABLE app.sales_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.sales_order FORCE ROW LEVEL SECURITY;

CREATE POLICY sales_order_by_org ON app.sales_order
    USING (org_id = ANY (api.current_member_org_ids()));
```

The second line matters as much as the third: table owners bypass RLS unless `FORCE ROW LEVEL SECURITY` says otherwise — the most common production mistake in this area, and one no middleware analogy prepares you for.

Identity arrives as data, like everything else at this boundary. A fronting proxy validates the credential (more on that division in Part IV) and forwards a verified subject; the dispatch layer stores it in a transaction-local setting, and everything downstream reads it from there:

```sql
SELECT set_config('auth.idp_subject', 'oidc|4f8a…', true);   -- true = transaction-local
```

One precision the security argument depends on: a settable session variable is an identity *channel*, not an authorization boundary. A custom parameter like `auth.idp_subject` is USERSET — any role holding a connection can assert any subject, and PostgreSQL's parameter ACLs do not govern such placeholders. The boundary is the *role*: callers you do not trust connect as a role whose RLS policies bind to `current_user`, and the verified-subject variable is trusted only on connections the gateway owns. What no caller can do from SQL is talk itself into a different role's privileges — and that is the property the rest of this section leans on.

API keys stay in the same model with pgcrypto: store `digest(key, 'sha256')`, never the key; compare hashes; attach expiry and revocation as columns, because a credential is a row with a lifecycle, not a string in a config file.

The strongest recent argument for database-resident authorization came from an incident, not a design doc. The Supabase MCP prompt-injection chain ([analyzed by Simon Willison, July 2025](https://simonwillison.net/2025/Jul/6/supabase-mcp-lethal-trifecta/)) worked because the agent's connection ran as a role that bypasses RLS by design: the privilege lived in the agent's configuration, and a malicious support ticket talked the agent into using it. An agent that reaches the data only through handlers executing under a real, constrained role holds no such privilege to be talked out of — the injected instruction cannot escalate the role, because the role is not in the prompt's reach. Stated with its limit: RLS bounds the blast radius to what the caller could already legitimately do. An injected "cancel every order" within the agent's own authority is a tool-authorization problem above the database, and no row policy touches it. This is the request-time form of an argument I have made elsewhere about agents generally: give them more context and less authority — and enforce the authority below the caller.

RLS has a performance reputation, and predicate shape is most of it: the difference between a naive policy and a well-shaped one is not marginal. Supabase's [published measurements](https://supabase.com/docs/guides/troubleshooting/rls-performance-and-best-practices-Z5Jjwv) show `auth.uid()` wrapped in a scalar subquery taking a policy from 179 milliseconds to 9 — and a security-definer role check in the same guide from 178 *seconds* to 12 milliseconds. Policies are query fragments; they deserve the same plan discipline as any other predicate.

### The transaction is declared per operation

Here is the primitive no application-tier framework can offer natively, because it does not own one: the unit of work. PostgREST — the best-known database-first API layer — states its contract in one sentence: after its user-impersonation step, "every request to an API resource runs inside a transaction" ([transactions reference](https://docs.postgrest.org/en/latest/references/transactions.html)). That is the floor. The interesting structure sits above it: *which* transaction does each operation deserve?

A route that reads a report can run `READ ONLY` at `REPEATABLE READ`. A route that moves money can declare `SERIALIZABLE` as a floor. A route that appends an event can accept the default. A long serializable read has a stronger option still: opened `READ ONLY DEFERRABLE`, it can never abort with a serialization failure once it has started — at the price that *acquiring* that guarantee may block at snapshot time for as long as concurrent serializable writers remain in flight; this is the one case where serializable blocks and repeatable read does not, and under write load the wait is measured in seconds. That trade suits a report, not a latency-budgeted request, so `DEFERRABLE` belongs as a per-route declaration the resolver honors, never one it silently adds. In the catalog above, the policy is three columns — `read_only`, `min_isolation`, `use_deferrable` — and the third carries its own constraint: the catalog refuses a deferrable declaration on any route that is not serializable and read-only, so the only storable combination is the one the guarantee requires. And it composes with everything else that is now data: the OpenAPI document can publish each route's isolation contract, and a replica-routing proxy can read *from the contract* which routes are safe to serve from a standby. With one exclusion the catalog itself can prove: a hot standby refuses serializable mode entirely, so a serializable floor and standby-routability are mutually exclusive properties of the same row. That this conflict is a checkable predicate at deploy time, rather than a runtime surprise, is the whole argument in miniature.

One PostgreSQL constraint shapes the whole design — and getting it wrong produces a subtly broken framework. A function cannot raise its own transaction's isolation level: by the time the function runs, the transaction has begun and its snapshot semantics are fixed. So the declared policy must be *resolved before* `BEGIN` — by whatever opens the transaction — and *enforced after*, by the database, which checks `current_setting('transaction_isolation')` against the route's floor and refuses to proceed (HTTP 428, Precondition Required) if the caller opened too weak a transaction. (428 is a deliberate reuse: RFC 6585 defines it for conditional requests, and here the unmet precondition is the transaction the adapter opened, not a header the client controls — so a gateway seeing it should treat it as its own misconfiguration, not the client's.) Resolution is advisory and lives at the connection layer; enforcement is fail-closed and lives in the data. Neither layer trusts the other, and the contract holds even when a misconfigured client connects directly. PostgreSQL 17's `transaction_timeout` completes the picture: one request, one transaction, one bounded lifetime, enforced by the engine — which terminates the overstaying *session*, not just its transaction, a detail pooled deployments should plan for.

Assemble the inventory and notice what is missing: nothing that belongs to the transactional boundary. Message types, router, negotiation surface, validation, authorization, transaction policy, serialization, contract publication — each one native, each one data. What is deliberately absent — sockets, TLS, credential verification, rate limits — was assigned to the gateway in Part I and stays there. The remaining question is what the placement buys beyond symmetry.

## Part III — What consolidation gives, and what placement adds

Two classes of benefit need separating — Part I named them, and only one of them is this article's claim. *Consolidation* benefits — one planner, one snapshot, multi-modal indexes, triggers firing in the writer's transaction — belong to PostgreSQL and are available to any tier that connects to it. *Placement* benefits are the ones the boundary's location creates: the contract deployed with the state it governs, transaction policy enforced where it is declared, no network await expressible inside the transaction, tests that reach response and state in one snapshot. The sections below draw on both, and the accounting says which is which.

### One snapshot, all dimensions

The consolidated database from Part I holds embeddings, geometry, text indexes, and relational state. An API handler standing *inside* that database can join all of them in one plan under one MVCC snapshot:

```sql
SELECT l.listing_id, l.title, l.price
FROM app.listing l
WHERE l.status = 'active'
  AND ST_DWithin(l.location, ST_MakePoint($2, $3)::geography, $4)
  AND l.valid @> now()
  AND l.org_id = ANY (api.current_member_org_ids())
ORDER BY l.embedding <=> $1
LIMIT 20;
```

Semantic similarity, geographic containment, temporal validity, and tenant authorization — one query, one planner, one moment in time, one answer to "is it available?". The decomposed version of this endpoint queries a vector store, a geo service, and the relational database separately, then intersects candidate sets in process memory: an ad-hoc distributed query planner with no shared snapshot, whose combined read describes several moments in time at once. I have written about that failure pattern at architecture scale in *The Lakehouse Publishes. Applications Decide Locally.*; the API handler is where it bites at request scale. This fused, multi-modal, permission-scoped read *is* the valuable API — the endpoint callers actually want.

It is, in the accounting above, a consolidation benefit: an application-tier handler can send this exact statement and receive this exact plan, and the enemy was never application code — it is decomposing one business predicate across independently queried stores and reassembling the pieces in process memory. What placement adds sits around the statement: nothing between this read and its commit can await a network, and the test that proves the predicate sees the same snapshot as the transition it gates. (The standard caveat travels with it: filtered approximate vector search can return fewer candidates than requested unless iterative scanning compensates — pgvector documents this. Locality removes the consistency problem; it does not remove query design.)

### The cache participates in the transaction

Caching is a clean test of the consolidation-versus-placement distinction this part opened with — and the honest accounting is stricter than it first looks: putting a cache table in PostgreSQL is consolidation, and so is the invalidation trigger below, because a handler in *any* tier that writes the source table fires the same trigger in the same transaction. What placement adds here is what it always adds: the route's cache policy lives in the same catalog row the deploy gates, and the test that proves invalidation happened reaches the cache and the state in one snapshot. PostgreSQL has a table class built for cache semantics: [unlogged tables](https://www.postgresql.org/docs/current/sql-createtable.html) skip WAL, which makes them markedly cheaper to write, at the cost of being truncated after crash recovery and unreadable on standbys (their contents are not replicated). For durable state those costs are disqualifying; for a cache they are the *definition* — a cache is precisely the data you are allowed to lose:

```sql
CREATE UNLOGGED TABLE api.read_model_cache (
    resource_type text NOT NULL,
    resource_id   uuid NOT NULL,
    scope         text NOT NULL DEFAULT '',
    etag          text NOT NULL,
    content       jsonb NOT NULL,
    computed_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (resource_type, resource_id, scope)
);
```

The key is relational, not an opaque string, and that is its own small argument: a representation that is tenant- or version-scoped carries that scope as a column, so "every cached representation of this entity" is an indexed predicate rather than a `LIKE` over concatenated identifiers — structure the database can query, in the article's own spirit.

What the co-location buys is not speed — Redis is faster at being Redis — but a property no external cache can have at any speed: **invalidation in the same transaction as the write that makes it stale.**

```sql
CREATE FUNCTION app.invalidate_order_cache() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM api.read_model_cache
    WHERE resource_type = 'order'
      AND resource_id = COALESCE(NEW.order_id, OLD.order_id);
    RETURN NULL;
END;
$$;

CREATE TRIGGER order_cache_invalidation
    AFTER INSERT OR UPDATE OR DELETE ON app.sales_order
    FOR EACH ROW EXECUTE FUNCTION app.invalidate_order_cache();
```

The mutation and the invalidation commit together or not at all: there is no window where the write is visible while its invalidation is still in flight, and nothing to retry when a broker is down. Precision matters here, because cache anomalies come in two families and Meta's writeup names both mutation paths — fill and invalidation. The *invalidation* race is what co-location makes unexpressible: the cache and the source of truth share a commit point. The *fill* race — a reader computing an entry from a snapshot a concurrent write has just superseded, then committing the stale fill after the invalidation already ran — remains possible here exactly as it is with Redis. The difference is where the fix lives: stamping the source row's version into the entry, or taking `FOR SHARE` on the source row during the fill, are single-engine, single-transaction techniques — not a distributed-invalidation program. (Isolation level alone is not one of them: a repeatable-read filler can still commit an entry computed from a snapshot a concurrent writer has already superseded.) Note the trigger's shape: one indexed relational predicate removes *every* scoped representation of the entity — the invalidation cannot forget a tenant's copy, because scope is a column, not a substring convention.

And the boundary speaks HTTP's own caching protocol, with more authority than an application tier usually musters. The web already ships a distributed read-through cache — every browser, reverse proxy, and CDN implements [RFC 9111](https://www.rfc-editor.org/rfc/rfc9111.html) — and it is driven entirely by response metadata: `Cache-Control` with `max-age` and `s-maxage`, [`stale-while-revalidate`](https://www.rfc-editor.org/rfc/rfc5861) for graceful refresh, `ETag` for revalidation. The layer best placed to emit that metadata is the one that knows the data's change cadence — and here that knowledge is a column away. A route serving reference data that changes nightly can declare `public, max-age=300, stale-while-revalidate=60` as part of its contract — one more attribute in the catalog, one more derivation — and every standards-compliant cache between the database and the caller becomes its read-offload tier, absorbing repeats that never reach PostgreSQL at all. Staleness stops being an accident of some middleware's default and becomes a declared, per-route tolerance: the same freshness-budget idea that governs accepted context in [the companion architecture article](https://alexeyevlampiev.github.io/posts/lakehouse-publishes-applications-decide-locally/), applied outbound. Revalidation stays cheap on the database side — `If-None-Match` against the stored `etag` is one indexed read and a 304, not a recomputation. The reach of this mechanism is worth stating exactly: these headers *declare* staleness tolerance, they do not enforce invalidation. A copy that left with `max-age=300` may legitimately be served stale for up to that window — which is precisely why the tolerance belongs in a reviewable contract rather than a middleware default, and why identity-scoped responses ship `Cache-Control: private` and `Vary`.

Two limits. An unlogged table cannot be read on a hot standby at all — a query during recovery errors out rather than missing — so a route that consults this cache is not replica-safe regardless of its `read_only` flag; the cache lives on the primary, or in a logged table where replicas must read it. And the boundary authentik's experience draws applies here too: this pattern covers read-model and response caching, which is most of what APIs cache; it does not make PostgreSQL a pub/sub fabric, and workloads that need one should keep one.

### The catalog describes itself

The boundary's architecture is complete at this point; this section and the next are consequences of one decision inside it — the catalog is data, so its projections are queries. Because the route catalog is a table, the API contract is a query over it. An OpenAPI 3.1 document is one aggregate over routes joined to their schemas — `jsonb_object_agg` keyed by `canonical_path`, methods nested per path; the contract's cache validator is a digest over exactly the columns the document is built from — change a route's schema and the ETag changes, touch nothing and conditional requests answer 304 forever. The spec cannot drift from the router, because the spec is a *projection of* the router. The same catalog answers runtime questions ("which deployed routes changed since the last release?") and powers deploy-time gates ("every route must declare an output schema, or the deploy fails").

The industry is converging on this from the other direction, and the strongest signal comes from the flagship of schema reflection itself. Supabase — whose model was "your schema is automatically your API" — is [ending automatic table exposure](https://supabase.com/changelog/45329-breaking-change-tables-not-exposed-to-data-and-graphql-api-automatically) across 2026, on the reasoning that "agents, CLI scripts, and AI platforms create tables too, and many of those operations do not have a human reviewing the diff," and that explicit grants make access "reviewable, diffable, and greppable." The conclusion generalizes: an API surface should be an explicit, versioned declaration, not a reflection of whatever the schema happens to contain. A route catalog in SQL, deployed through source control, is exactly that declaration — and the two reference implementations that went the *other* way are instructive: Hasura, which kept its permission metadata outside the database, pivoted away from auto-generated APIs entirely in 2025; PostgREST, which kept everything in the database, shipped three releases in the month this article was written.

### The same operations become agent tools

An MCP tool is a name, a description, an input schema, an output schema, and an invocation — which is to say, an MCP tool is one more binding to the same transactional operation the route row describes. The catalog serializes it directly:

```sql
SELECT COALESCE(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
    'name',        trim(both '_' from lower(regexp_replace(r.canonical_path,
                                             '[^a-zA-Z0-9]+', '_', 'g'))),
    'description', r.description,
    'inputSchema', COALESCE(r.input_schema,
                            '{"type":"object","additionalProperties":false}'::jsonb),
    'outputSchema', r.output_schema))), '[]'::jsonb)
FROM api.route r
WHERE NOT r.requires_auth OR api.current_user_id() IS NOT NULL;
```

Two spec obligations already shape the projection: MCP restricts tool names to letters, digits, `_`, `-`, and `.`, so the path becomes a slug while the row stays the one declaration — a derivation that collides once two methods or versions share a path, one more reason for the operation-first split noted below; and `inputSchema` is mandatory and non-null, so a parameterless tool declares the closed empty-object schema (and an empty catalog serializes as `[]`, never SQL null).

One declaration, several protocol surfaces — the REST route, the OpenAPI operation, and the agent tool derive from the same row, and the `WHERE` clause scopes discovery to the caller — the sketch checks only authentication; a production predicate extends it to invocation rights, because the principle is that an agent should not be shown a tool it cannot invoke, and the predicate deciding both visibility and invocation is one clause over one catalog. The tool executes as a database transaction under a real role with RLS active, which is what separates "give the agent seven audited, transactional, scoped operations" from "give the agent SQL access" — not the same security posture, as the Supabase incident demonstrated.

Then the catalog's being a table pays once more. A deep operation catalog — hundreds of governed transactions accumulated over years — exceeds what fits in an agent's context window, and the current industry answer is tool search. Anthropic's [Tool Search Tool](https://www.anthropic.com/engineering/advanced-tool-use) ships regex- and BM25-based search out of the box and explicitly delegates the semantic version — "you can also implement custom search tools using embeddings or other strategies" — leaving the embedding store, its freshness, and its access control as your problem. But the route catalog is a table, and tables take columns:

```sql
ALTER TABLE api.route ADD COLUMN description_embedding vector(1024);

SELECT r.canonical_path, r.description
FROM api.route r
ORDER BY r.description_embedding <=> $1     -- the task, embedded
LIMIT 5;
```

Semantic tool discovery over the operation catalog, in the catalog, filtered by the same authorization predicate as discovery itself. The embedding is *derived* state — computed outside any transaction (a model call is network egress, per Part V's rule) and refreshed when the description changes, like any derived column. The row guarantees locality, governance, and one authorization surface; freshness is a maintenance obligation the catalog makes visible, not one it dissolves. And since native BM25 ranking [arrived in the PostgreSQL ecosystem in 2026](https://www.postgresql.org/about/news/pg_textsearch-v10-3264/), the lexical search Anthropic ships and the semantic search it delegates can hybridize in one SQL query, under RLS, inside a transaction. Once the operation catalog is relational data, semantic discovery is an ordinary derived index over it — and years of accumulated transactional operations become a searchable tool inventory for agents, at the cost of one column.

One structural note before the walk, completing Part II's acknowledgment. At scale, the flattened route row wants to split along Part I's line: a *transactional operation* catalog — contract, authorization, transaction policy, handler — with per-protocol *bindings* referencing it (a REST route, an MCP tool name, an RPC method), so that two protocol surfaces never compete for one identifier and a path template stops doubling as a tool name. That operation-first shape is exactly how the reference implementation in Part VI structures its registry: one handler table, separate route tables per protocol.

## Part IV — A working model in plain SQL

Concepts earn nothing until they run. What follows is a deliberately minimal but complete vertical slice — types, catalog, dispatch, two handlers, registration, and a test — in the order a deployment would create them. It is a teaching model, not a product: the final section names a complete implementation, and the differences are listed at the end of this part. The prose between the blocks carries the argument, so a reader who already trusts the SQL can skim the listings and slow down at the two test transactions — they are the destination.

Helpers first. Canonicalization maps every legitimate spelling of a URL to one identity — the full treatment (RFC 3986 §6.2.2: percent-case, unreserved decoding, dot-segments) is a page of code; the teaching version states the intent:

```sql
CREATE FUNCTION api.canonical_path(p_url text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT
AS $$
    SELECT '/' || trim(both '/' from split_part(p_url, '?', 1))
$$;

CREATE FUNCTION api.problem(p_status api.http_status, p_title text, p_detail text)
RETURNS api.http_response
LANGUAGE sql STABLE
AS $$
    SELECT ROW(
        p_status,
        'content-type=>application/problem+json'::hstore,
        jsonb_build_object('status', p_status, 'title', p_title, 'detail', p_detail)
    )::api.http_response
$$;

CREATE FUNCTION app.try_cast_uuid(p text)
RETURNS uuid
LANGUAGE plpgsql IMMUTABLE
AS $$
BEGIN
    RETURN p::uuid;
EXCEPTION WHEN others THEN
    RETURN NULL;
END;
$$;

CREATE DOMAIN app.positive_finite_amount AS numeric
    CHECK (VALUE > 0 AND VALUE < 'Infinity');

CREATE FUNCTION app.try_cast_amount(p text)
RETURNS app.positive_finite_amount
LANGUAGE plpgsql IMMUTABLE
AS $$
BEGIN
    RETURN p::app.positive_finite_amount;
EXCEPTION WHEN others THEN
    RETURN NULL;
END;
$$;
```

The domain is the article's dual-validation answer made concrete: the business predicate — positive, finite — is declared exactly once, and the table column and the handler's materialization share it; the rejection message can then explain the predicate precisely without reimplementing it. The handler may still answer *before* the constraint would, so the client gets a precise problem detail; the constraint remains the authority; and neither can drift from the other, because there is only one predicate to drift from.

(Two notes on the helpers. The `try_cast` shape is the PostgreSQL 15 idiom — each call opens a subtransaction for its exception scope; PostgreSQL 16's `pg_input_is_valid` performs the same test without one. And the canonical-path helper is deliberately blunter than RFC 3986: trailing-slash stripping is normalization *policy*, not a §6.2.2 equivalence — a production implementation adds the RFC's own rules (percent-case, unreserved decoding, dot-segments) and states its policy, duplicate-slash collapse included, separately from them.)

Identity resolution reads the transaction-local setting Part II established:

```sql
CREATE TABLE app.user_identity (
    user_id     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    idp_subject text NOT NULL UNIQUE,
    active      boolean NOT NULL DEFAULT true
);

CREATE FUNCTION api.current_user_id()
RETURNS uuid
LANGUAGE sql STABLE
AS $$
    SELECT user_id
    FROM app.user_identity
    WHERE idp_subject = NULLIF(current_setting('auth.idp_subject', true), '')
      AND active
$$;
```

The domain tables and the transition function — the function that owns the business operation, assuming validated inputs, returning the full entity row; it is the fourth phase of the handler discipline named below, extracted into its own function so no protocol type ever appears in domain code:

```sql
CREATE TABLE app.customer (
    customer_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL
);

CREATE TABLE app.sales_order (
    order_id    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES app.customer,
    created_by  uuid NOT NULL REFERENCES app.user_identity,
    total       app.positive_finite_amount NOT NULL,
    status      text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'shipped', 'cancelled')),
    created_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE app.sales_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.sales_order FORCE ROW LEVEL SECURITY;

CREATE POLICY sales_order_owner ON app.sales_order
    USING (created_by = (SELECT api.current_user_id()))
    WITH CHECK (created_by = (SELECT api.current_user_id()));

CREATE FUNCTION app.create_order(
    p_customer_id uuid,
    p_total       app.positive_finite_amount
) RETURNS app.sales_order
LANGUAGE sql
AS $$
    INSERT INTO app.sales_order (customer_id, total, created_by)
    VALUES (p_customer_id, p_total, api.current_user_id())
    RETURNING *
$$;
```

(The policy wraps the identity call in a scalar subquery — Part II's own InitPlan advice applied to the article's own policy: evaluated once per statement, not once per row.)

Now the handlers. A handler is a function from request to response, and its body follows a discipline worth naming, because it travels beyond this article: **materialize → validate → probe → transition**. Materialize every input into a typed local; validate what materialized; probe the state the operation depends on; then execute the transition. Each phase owns its status codes, and no exception handling appears, because every expected outcome is a *returned response*, not a caught error:

```sql
CREATE FUNCTION api.post_order(request api.http_request)
RETURNS api.http_response
LANGUAGE plpgsql
AS $$
DECLARE
    v_body        jsonb := (request).content;
    v_customer_id uuid  := app.try_cast_uuid(v_body ->> 'customerId');
    v_total       app.positive_finite_amount := app.try_cast_amount(v_body ->> 'total');
    v_order       app.sales_order;
BEGIN
    IF v_body IS NULL THEN
        RETURN api.problem(400, 'Bad Request', 'A JSON body is required');
    END IF;
    IF v_customer_id IS NULL THEN
        RETURN api.problem(400, 'Bad Request', 'customerId must be a UUID');
    END IF;
    IF v_total IS NULL THEN
        RETURN api.problem(422, 'Unprocessable Content', 'total must be a positive finite number');
    END IF;

    PERFORM 1 FROM app.customer c
    WHERE c.customer_id = v_customer_id
    FOR KEY SHARE;
    IF NOT FOUND THEN
        RETURN api.problem(422, 'Unprocessable Content', 'customer does not exist');
    END IF;

    v_order := app.create_order(v_customer_id, v_total);

    RETURN ROW(
        201,
        hstore('location', '/orders/' || v_order.order_id),
        to_jsonb(v_order)
    )::api.http_response;
END;
$$;

CREATE FUNCTION api.get_order(request api.http_request)
RETURNS api.http_response
LANGUAGE plpgsql STABLE
AS $$
DECLARE
    v_order_id uuid := app.try_cast_uuid(
        (regexp_match(api.canonical_path((request).url), '^/orders/([^/]+)$'))[1]);
    v_order    app.sales_order;
BEGIN
    IF v_order_id IS NULL THEN
        RETURN api.problem(400, 'Bad Request', 'order id must be a UUID');
    END IF;

    SELECT o.* INTO v_order
    FROM app.sales_order o
    WHERE o.order_id = v_order_id;

    IF NOT FOUND THEN
        RETURN api.problem(404, 'Not Found', 'no such order');
    END IF;

    RETURN ROW(200, ''::hstore, to_jsonb(v_order))::api.http_response;
END;
$$;
```

Note the status-code discipline the phases encode: 400 for a request that is malformed on its face, 404 for the thing the URL names, 422 for a thing the body points to that does not exist. The taxonomy lives in one place and every handler inherits it. What the sketch's helper omits, a production version adds: RFC 9457's `type` URI, so distinct rejections sharing a status stay machine-distinguishable. One predicate above also deserves its story told: `VALUE > 0` alone would admit `NaN`, because numeric `NaN` sorts greater than every number — which is why the domain says `AND VALUE < 'Infinity'`. A constraint is only as good as the predicate you wrote; here the predicate is written exactly once, and the column and the materialization share the one copy — the error message narrates it without restating it.

The probe phase carries a concurrency rule of its own. A probe establishes a fact the transition depends on, and between check and act another transaction can remove that fact. So the handler locks what it checked — `FOR KEY SHARE` holds the customer row against deletion without blocking other order writers (row locks count as writes in the privilege model, so the executing role needs `UPDATE` on the probed table — a real, unscoped `UPDATE`, since PostgreSQL has no lock-only privilege; that grant is part of the probe's price) — or it skips the probe and lets the foreign key arbitrate, translating the expected SQLSTATE into the same 422. What it must not do is check without locking and call the answer authoritative. (One naming trap worth a sentence: RFC 9457 problem bodies carry a numeric `status`, and the example entity carries a lifecycle `status` of its own. The assertions below survive the collision; a production contract should not ask clients to disambiguate a field name by its type.)

Dispatch resolves the catalog and invokes the handler — the entire "framework runtime" is one function:

```sql
CREATE FUNCTION api.invoke(
    p_method  api.http_method,
    p_url     text,
    p_headers hstore DEFAULT ''::hstore,
    p_content jsonb  DEFAULT NULL
) RETURNS api.http_response
LANGUAGE plpgsql
AS $$
DECLARE
    v_path     text := api.canonical_path(p_url);
    v_version  text := COALESCE(p_headers -> 'x-api-version', '');
    v_route    api.route;
    v_request  api.http_request;
    v_response api.http_response;
    v_levels   constant text[] :=
        ARRAY['read uncommitted', 'read committed', 'repeatable read', 'serializable'];
BEGIN
    SELECT r.* INTO v_route
    FROM api.route r
    WHERE v_path ~ r.path_regexp
      AND p_method ~ r.method_regexp
      AND v_version ~ r.version_regexp
    ORDER BY r.sequence_number DESC
    LIMIT 1;

    IF NOT FOUND THEN
        RETURN api.problem(404, 'Not Found', 'no route matches ' || v_path);
    END IF;

    IF v_route.requires_auth AND api.current_user_id() IS NULL THEN
        RETURN api.problem(401, 'Unauthorized', 'authentication required');
    END IF;

    IF v_route.min_isolation IS NOT NULL
       AND COALESCE(array_position(v_levels, current_setting('transaction_isolation')), 0)
         < array_position(v_levels, v_route.min_isolation)
    THEN
        RETURN api.problem(428, 'Precondition Required',
            'route requires at least ' || v_route.min_isolation);
    END IF;

    IF v_route.read_only
       AND current_setting('transaction_read_only') = 'off'
    THEN
        RETURN api.problem(428, 'Precondition Required',
            'route requires a read-only transaction');
    END IF;

    v_request := ROW(p_method, p_url, p_headers, p_content)::api.http_request;

    EXECUTE format('SELECT * FROM %s($1)', v_route.handler::oid::regproc)
    INTO v_response
    USING v_request;

    RETURN v_response;
END;
$$;
```

Registration is `INSERT` — which means registration is *deployable, reviewable, and transactional*, like every other DDL-adjacent act in the project:

```sql
INSERT INTO api.route
    (canonical_path, path_regexp, method_regexp, handler, read_only, description)
VALUES
    ('/orders', '^/orders$', '^POST$',
     'api.post_order(api.http_request)'::regprocedure, false,
     'Create an order for an existing customer'),
    ('/orders/{id}', '^/orders/([^/]+)$', '^GET$',
     'api.get_order(api.http_request)'::regprocedure, true,
     'Fetch one order by id');
```

And now the payoff the whole article has been building toward. The entire boundary — routing, auth, validation, business transition, response shaping — is a function call, so a test exercises it end to end *inside a transaction*, asserts on the response and the state together, and rolls the world back:

```sql
BEGIN;

INSERT INTO app.user_identity (idp_subject) VALUES ('oidc|alice');
SELECT set_config('auth.idp_subject', 'oidc|alice', true);

INSERT INTO app.customer (name) VALUES ('Acme Corp');
SELECT set_config('test.customer_id',
    (SELECT customer_id::text FROM app.customer WHERE name = 'Acme Corp'), true);

DO $$
DECLARE
    v_customer uuid := current_setting('test.customer_id')::uuid;
    v_response api.http_response;
    v_order_id uuid;
    v_state    text;
BEGIN
    v_response := api.invoke('POST', '/orders', ''::hstore,
        jsonb_build_object('customerId', v_customer, 'total', 99.95));
    IF (v_response).status_code IS DISTINCT FROM 201 THEN
        RAISE EXCEPTION 'expected 201, got %: %',
            (v_response).status_code, (v_response).content;
    END IF;

    v_order_id := ((v_response).content ->> 'order_id')::uuid;

    SELECT o.status INTO v_state
    FROM app.sales_order o WHERE o.order_id = v_order_id;
    IF v_state IS DISTINCT FROM 'pending' THEN
        RAISE EXCEPTION 'state assertion: expected a pending row, got %', v_state;
    END IF;

    v_response := api.invoke('POST', '/orders', ''::hstore,
        jsonb_build_object('customerId', v_customer, 'total', -5));
    IF (v_response).status_code IS DISTINCT FROM 422 THEN
        RAISE EXCEPTION 'negative total must be rejected, got %',
            (v_response).status_code;
    END IF;
    IF (SELECT count(*) FROM app.sales_order) IS DISTINCT FROM 1::bigint THEN
        RAISE EXCEPTION 'state assertion: the rejected request must not create a row';
    END IF;

    v_response := api.invoke('GET', '/orders/' || v_order_id);
    IF (v_response).status_code IS DISTINCT FROM 428 THEN
        RAISE EXCEPTION 'read-only route in a read-write transaction must 428, got %',
            (v_response).status_code;
    END IF;

    PERFORM set_config('transaction_read_only', 'on', true);

    v_response := api.invoke('GET', '/orders/' || v_order_id);
    IF (v_response).content ->> 'status' IS DISTINCT FROM 'pending' THEN
        RAISE EXCEPTION 'expected a pending order, got %', (v_response).content;
    END IF;

    v_response := api.invoke('GET', '/orders/' || gen_random_uuid());
    IF (v_response).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'missing order must 404, got %', (v_response).status_code;
    END IF;
END;
$$;

ROLLBACK;
```

Read what that block is: an HTTP conversation — a create followed by a state assertion on the row it made, a rejected write proven to have written nothing, the read-only gate firing against a read-write transaction and then satisfied after the test flips the transaction read-only mid-flight (legal in that direction), a fetch, and a missing-resource miss — executed against the real schema, the real constraints, the real identity gate, and the real handler code, leaving nothing behind. No test server, no port, no mock, no fixture database that is almost-but-not-quite production. The response assertions and the state assertions live in the same snapshot.

The authorization story deserves its own transaction, under a role that row-level security actually constrains — superusers bypass RLS, so the proof must not run as one. The role's name says what it is: `api_runtime` is the role the gateway connects as — the only principal whose `auth.idp_subject` assertions are trusted, per Part II — never a role handed to API clients:

```sql
BEGIN;

INSERT INTO app.user_identity (idp_subject) VALUES ('oidc|alice'), ('oidc|bob');
INSERT INTO app.customer (name) VALUES ('Acme Corp');
SELECT set_config('test.customer_id',
    (SELECT customer_id::text FROM app.customer WHERE name = 'Acme Corp'), true);

CREATE ROLE api_runtime;
GRANT USAGE ON SCHEMA api, app TO api_runtime;
GRANT SELECT ON api.route TO api_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA app TO api_runtime;
GRANT INSERT ON app.sales_order TO api_runtime;
GRANT UPDATE ON app.customer TO api_runtime;

DO $$
DECLARE
    v_customer uuid := current_setting('test.customer_id')::uuid;
    v_response api.http_response;
    v_order_id uuid;
BEGIN
    SET LOCAL ROLE api_runtime;

    PERFORM set_config('auth.idp_subject', 'oidc|alice', true);
    v_response := api.invoke('POST', '/orders', ''::hstore,
        jsonb_build_object('customerId', v_customer, 'total', 10));
    IF (v_response).status_code IS DISTINCT FROM 201 THEN
        RAISE EXCEPTION 'alice expected 201, got %: %',
            (v_response).status_code, (v_response).content;
    END IF;
    v_order_id := ((v_response).content ->> 'order_id')::uuid;

    PERFORM set_config('transaction_read_only', 'on', true);

    v_response := api.invoke('GET', '/orders/' || v_order_id);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'alice must read her own order, got %',
            (v_response).status_code;
    END IF;

    PERFORM set_config('auth.idp_subject', 'oidc|bob', true);
    v_response := api.invoke('GET', '/orders/' || v_order_id);
    IF (v_response).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'row security must hide alice''s order from bob, got %',
            (v_response).status_code;
    END IF;

    IF EXISTS (SELECT FROM app.sales_order) THEN
        RAISE EXCEPTION 'direct SELECT as bob must see no rows';
    END IF;
END;
$$;

ROLLBACK;
```

Alice creates and reads her order. Bob — same dispatch path, same route, a perfectly valid identity — receives a 404 indistinguishable from nonexistence, because the policy filters the row before the handler's `SELECT` ever sees it. And the last assertion is the one that matters most: a direct query under the same role also sees nothing, so the guarantee does not depend on going through the API. This is what "testing the API" means when the API is a database program: identity propagation, authorization, validation, and the state transition, proven in the same snapshot they will share in production — credential verification itself stays with the gateway, per Part I's line, and gets tested there — and the whole exercise, `CREATE ROLE` included, vanishes at `ROLLBACK`. (One idiom note for harnesses that run many tests in one transaction: `SET LOCAL ROLE` persists past the `DO` block until the transaction ends, so a savepoint-per-test harness should `RESET ROLE` as each test closes.)

**What the teaching model omits**, so nobody mistakes the sketch for the building. Every entry is more rows and more functions in the same model; none requires a different architecture:

| Omitted concern | Production treatment |
|---|---|
| Content negotiation | `Accept`/`Content-Type` against declared `produces`/`accepts`; 406/415 |
| Method mismatch | 405 with a correct `Allow` header |
| Authentication challenge | `WWW-Authenticate` on every 401 — RFC 9110 makes it mandatory |
| Cache metadata | `Vary` and `Cache-Control` stamping per Part III |
| URL identity | full RFC 3986 canonicalization plus a named normalization policy |
| Header-name case | lowercase hstore keys at ingress — HTTP/2 lowercases names on the wire, HTTP/1.1 clients send any case; skip this and the version lookup above silently misses `X-Api-Version` |
| Wire naming | explicit field projection — `to_jsonb` exposes physical column names, and a production contract names its fields instead of leaking the schema |
| Route ambiguity | deploy-time overlap proofs |
| Audit | a request/response exchange log, itself a queryable table |
| Multi-tenancy | the membership tables behind `api.current_member_org_ids` — the walk's policy is owner-scoped |
| Privilege boundaries | `SECURITY DEFINER` with pinned `search_path` |
| Function privilege hardening | `REVOKE EXECUTE … FROM PUBLIC` plus explicit grants — PostgreSQL grants `EXECUTE` on new functions to `PUBLIC` by default |
| Identity lifecycle | JIT user provisioning |
| Rate-limit metadata | declared per route, enforced by the gateway |
| Other bindings | the OpenAPI and MCP projections of Part III |

**What stays outside the database, permanently:** TLS termination, HTTP socket handling, JWT signature validation, CORS, rate limiting, DoS absorption — the concerns that are *about the network*, not about the data. The division is principled: a gateway kept *semantically narrow* — however many lines its TLS, pooling, and retry machinery take, it contains no business rule — authenticates the caller, resolves the route's declared transaction policy with one pre-`BEGIN` query, opens the transaction accordingly, calls `api.invoke`, and relays the response.

Said plainly, resolve-then-open evaluates the declaration twice per request — once before `BEGIN` to choose the transaction, once inside it to dispatch. One source of truth, two evaluations, and the second exists to police the first: if a deploy changes the route between them, the fail-closed gates refuse rather than run under the wrong contract. The same distinction licenses caching: the gateway may hold a versioned projection of route transaction metadata and skip the pre-`BEGIN` query entirely — a *copy*, and copies are fine. What Part I's argument forbids is duplicate *authority*; a cached copy is safe precisely because the in-transaction evaluation stays authoritative and fail-closed behind it. It owns retries on serialization failures — a new transaction needs a new snapshot, which only the connecting layer can provide. It adds no business semantics. It is, precisely, an adapter — and the database's fail-closed checks (the 428 above) mean that even a wrong adapter cannot violate a route's declared contract.

## Part V — Where this fits, and where it does not

The counterarguments deserve their strongest form. One of them is structural — the physics of a single write path; the rest are operational — language, tooling, observability, process. The structural one comes first.

**The primary write path remains a capacity boundary.** It has a current, credible spokesman: PostGraphile V5 — facing the same inputs as pg_graphql and choosing the opposite architecture — [states its reasoning plainly](https://postgraphile.org/news/2026-03-24-v5-published/) as "moving work from the hard-to-scale database tier to the easy-to-scale JS tier." Every cycle a handler burns on the primary is a cycle the storage engine, WAL writer, and autovacuum do not get, and when the primary saturates there is no "add another instance" for the write path. Three things bound the concession without dissolving it.

First, the read path has two systematic relief valves the contract itself declares. Per-route `READ ONLY` declarations — published in the contract, enforced fail-closed — make replica offload deliberate rather than hopeful (with standby-safety *derived* from the row, not equated with `read_only`: Part II's serializable exclusion and Part III's unlogged-cache restriction are both checkable predicates over the same catalog). And per-route cache policy pushes further out: reads whose staleness tolerance the route declares are absorbed by every RFC 9111 cache between the database and the caller, and never arrive at all. Read-heavy is what most APIs are.

Second, the canonical benchmark against in-database processing measures something else: Microsoft's [Busy Database antipattern](https://learn.microsoft.com/en-us/azure/architecture/antipatterns/busy-database/) shows a 33× throughput difference — for a workload doing XML serialization and string formatting in the database, which their own guidance concludes belongs in the application, while conceding that data-intensive logic should *not* move out. Presentation logic out, transactional logic in: the antipattern and this article agree.

Third, the arithmetic of what was removed. Every authoritative read and write terminates in the same database whichever tier hosts the handler, so the application tier does not intrinsically reduce that work. What it can do is reshape it — and when it decomposes one operation into a chatty sequence, each round trip pays network latency *inside an open transaction*. The handler above adds validation and dispatch to a query the application tier would have issued anyway, while removing the round trips, the double validation pass, and the idle-in-transaction time that were the tier's contribution to database load — and shorter transactions are the cheapest concurrency win PostgreSQL offers: locks release sooner, the snapshot horizon advances, vacuum keeps up. A disciplined tier can issue the same chunky call from outside; what placement removes is not the statement count — a handler may run twenty statements — but the very possibility of a network wait between the statements of one transaction. The most recent [head-to-head benchmark](https://npgsqlrest.github.io/blog/postgresql-rest-api-benchmark-2026) of database-first API layers against a hand-written Go server carries the telling detail: at realistic payload sizes the throughput numbers converge, because database I/O dominates and the tier in front stops mattering — "easy to scale" applies to the layer that was not the bottleneck.

The boundary this design refuses to cross is the same one the industry keeps rediscovering: nothing nondeterministic, rate-limited, or network-bound belongs inside a transaction. (The leading Postgres-AI vendor [deprecated its own in-database LLM-call functions](https://www.tigerdata.com/docs/deploy/tiger-cloud/vectorizer-deprecation) in 2026 — the right call, and no contradiction: a model call is network egress, not a state transition.) When a workload genuinely exceeds one primary, the answer is the one from [the companion architecture article](https://alexeyevlampiev.github.io/posts/lakehouse-publishes-applications-decide-locally/): partition by operational cell, not by smearing one transaction across tiers.

**PL/pgSQL becomes application code — a technology choice, not a detail.** Choosing this architecture means a significant share of the application is written in SQL and PL/pgSQL, with everything that implies: code review, static analysis, team skill, debugging, deployment. That choice deserves to be made explicitly, because for many teams the tooling question decides it. The perennial "no linter, no debugger" complaint has a real kernel: PL/pgSQL resolves identifiers at runtime, so a typo in a rarely taken branch is a production error, not a compile error. The tooling answer exists — [plpgsql_check](https://github.com/okbob/plpgsql_check) does static analysis (catching exactly that), profiling, and statement/branch coverage; pldebugger does breakpoints — but a fact about managed platforms keeps the complaint alive: on RDS and Aurora, pgTAP and PL/Rust are on the extension allow-list, while plpgsql_check, pldebugger, and pg_tracing are not (all three need `shared_preload_libraries`). The gap is not in the ecosystem; it is in [the allow-list](https://docs.aws.amazon.com/AmazonRDS/latest/PostgreSQLReleaseNotes/postgresql-extensions.html), and it should inform platform choice: self-managed and allow-list-generous platforms get the full toolchain; the most restrictive managed offerings get tests without the linter.

**Connections are finite.** The numbers most often quoted here are stale. The `GetSnapshotData()` bottleneck that made thousands of idle connections expensive was [measured](https://www.citusdata.com/blog/2020/10/08/analyzing-connection-scalability/) and then [fixed by the same engineer in PostgreSQL 14](https://www.citusdata.com/blog/2020/10/25/improving-postgres-connection-scalability-snapshots/) — roughly doubling single-query throughput (sixteen to thirty-three thousand TPS) with 10,000 idle connections open. Above that, poolers carry the load ([Supavisor holds a million client connections against ~400 backends](https://supabase.com/blog/supavisor-1-million), at a stated cost of ~2ms per query). And the shape of this design is the *most* pooler-compatible one available: each request is one transaction on one connection, released at commit — no session state, no idle-in-transaction network waits, `set_config(..., true)` transaction-local by construction. The IBM incident in Part I was 65% of a pool stuck idle-in-transaction; this architecture cannot express that failure.

**Observability inside the handler is weaker.** Conceded, mostly. Every OpenTelemetry trace ends at the SQL statement; phases *inside* a handler are not spans, and there is no supported way to emit one from PL/pgSQL. The compensations are real but partial: the request/exchange log is a queryable table (timing, status, route, SQLSTATE per request — which is more than most app-tier logs can join against business state); `RAISE ... USING` with JSON logging correlates; [pg_tracing](https://github.com/DataDog/pg_tracing) produces genuine spans for nested statements but is early, version-lagged, and unavailable on major managed platforms. If per-phase distributed tracing is a hard requirement today, weight this section accordingly.

**Deployment discipline is on you.** `CREATE OR REPLACE FUNCTION` is a global cutover — there is no 5% canary for a function (versioned schemas per Part II are the workaround, not a native gradual rollout). Nothing *forces* git-first discipline; one ad-hoc `psql` session desyncs production from source control. The teams that run this model well are unambiguous about the remedy — the PostgREST maintainer who runs everything in the database [states it as a rule](https://github.com/PostgREST/postgrest/discussions/2509): "Git is the only source of truth — not the database." Zalando's decade-long arc with 3,000+ stored procedures is the cautionary tale read correctly: what broke was versioning automation and five-hour deployments — tooling and process, not correctness. Their failure mode is a solved problem *if and only if* the SQL deploys like software: from source control, transactionally, gated by tests. Which is the subject of the last part.

**The shared-database objection lands on a different design.** Fowler's [Integration Database](https://martinfowler.com/bliki/IntegrationDatabase.html) — many applications coupling through one schema — deserves its bad reputation, and nothing here requires it: this is an *application database* with one owning team, whose external consumers get the API, never the tables. The related, real question is team scale. Thirty engineers in one `api` schema need ownership boundaries (schema per domain, with the catalog spanning them), SQL-native code review, and hiring depth in PL/pgSQL — costs this part has already priced. And [Fowler's own assessment of in-database logic](https://martinfowler.com/articles/dblogic.html) concedes the other side of that ledger: once you accept that changing databases is unlikely, "you might as well start taking advantage of the special features your database provides."

**When not to build this.** A CRUD surface with no invariants worth a transaction — use PostgREST and be done. A team without PL/pgSQL fluency and without the intent to acquire it — the language is the platform here, and pretending otherwise fails slowly. Streaming and long-lived connections (WebSockets, SSE fan-out) — wrong tool; keep a stateful tier for those and let it call governed transactions. Compute-heavy request transformation (image processing, report rendering) — application tier, per the Busy Database boundary. And organizations whose deployment culture cannot commit to SQL-in-git should not put load-bearing logic in any database, this design included. Compressed to a rule an architect can carry into a design review: adopt this boundary when the application's valuable behavior is transactional decision-making over PostgreSQL state and the team will own SQL as application code; decline it when the dominant work is orchestration, streaming, or compute — or when database code cannot deploy from source control, transactionally, gated by tests.

## Part VI — The test gate is the argument

The test gate is not a stage in a pipeline that runs near the deploy — it is the deploy's own commit condition. Everything above describes a capability; that sentence is what turns it into a discipline, and this part earns it.

The conventional stack tests its API from outside: boot a server, mock or containerize the database, drive HTTP, assert on JSON. The coverage that survives this is interface coverage — and the paths that matter most, the *transactional* paths, are exactly the ones it cannot reach: does the constraint violation map to the right status *and* leave the state untouched? Does the authorization predicate hold for this role *on this row*? Do two operations that must commit together actually share a transaction? A mocked database answers none of these. A containerized one can answer them — send the HTTP request, then query the database and assert on both — but it answers through two interfaces stitched together by orchestration outside the operation: the HTTP effect and the state effect are observed at different moments, by different clients, with the commit already irrevocable between them.

When the boundary is a database program, the relationship inverts. Tests are SQL, executed in the deployment's own transaction, against the real schema at the exact version being deployed. Each test runs inside a savepoint and rolls back; the suite leaves every piece of transactional state exactly as it found it (sequences, by design, advance regardless — one more reason the walk keys its rows by UUID); and a failing test aborts the surrounding deploy transaction, so the schema change, the seed data, the route registration, and the handler code that failed together *vanish together*. (One PostgreSQL boundary travels with that guarantee: operations that refuse to run inside a transaction block — `CREATE INDEX CONCURRENTLY` is the canonical one — remain separate deployment phases after the gated transaction commits, with their own idempotency and verification story.)

That is what makes total coverage a sane target rather than a slogan — where "total" means every *declared transactional outcome*, not branch enumeration. An operation's outcomes are declarable and countable: its validation rejections, its probe failures, its success shapes, its authorization refusals, its version variants. Each is one `DO` block invoking the same function the production gateway invokes. There is no database test double to maintain, no fixture drift to chase, no schema-version gap between the code under test and the transaction deciding whether that deployment commits — so the marginal cost of the next test is the cost of writing it, and "every declared outcome of every operation" stops being aspirational.

The claim has edges, and they are worth naming. What the harness reaches is every *single-session* declared outcome — including deferred constraints, which `SET CONSTRAINTS ALL IMMEDIATE` pulls forward inside the savepoint so a rollback-only run cannot report green through a violation that would have surfaced at commit. What it does not reach: commit-time serialization conflicts, races that need two live sessions (Part III's cache-fill race is one), and the gateway's own behavior, retry loop included — those need the driver-level tests they have always needed. The claim is not that one transaction proves everything; it is that the paths a business reviewer would enumerate — validations, probes, transitions, authorization — all live inside it.

Stated as a testing architecture: exhaustive transactional operation tests inside the database; a thin band of driver-level tests for concurrency, retries, and commit-time behavior; a thinner band of wire tests for the gateway's own contract. The point is not to eliminate the outer bands — it is that moving the business state-space into the innermost band leaves the expensive outer layers too thin to need combinatorial coverage.

The systems I deliver on this design treat transactional coverage of every route — every path, not a sample — as part of the definition of done, and it is the single practice I would defend last: it is what lets a small team change a twelve-year-old schema on a Friday, and what lets an enterprise team prove to an auditor exactly which paths a deployment verified. The same property serves both, because the property is not a process — it is a consequence of where the boundary lives.

The consolidation argument of the last five years was right, and it stopped early. The queue moved in, the cache moved in, the vectors moved in — and the tier that governs them stayed outside, holding its second copy of everything. The types are there. The router is a table. The transaction was always the point.

Model the message. Register the operation. Declare the transaction. Prove every path.

The transactional half of the framework you were about to build already ships with your database.


## Appendix — A reference implementation

One complete implementation of everything this article describes exists as scaffolding: `pgmi init --template advanced` produces a working PostgreSQL project — registry, dispatch, four-phase handlers, per-route transaction policy resolved before `BEGIN` and enforced fail-closed after it, an OpenAPI 3.1 document derived live from the catalog with a registry-digest ETag, MCP tools registered through the same registry and machinery, API keys, RLS-backed multi-tenant membership, and a test suite that exercises routes end to end inside the deploy transaction, exactly as sketched here. The scaffolded SQL is application code you own, not a framework you depend on: pgmi itself is an execution fabric — it loads your files into session temp tables, hands control to your `deploy.sql`, and your SQL orchestrates everything, tests included. (It is also where the simplifications of this article are un-simplified: REST and RPC bodies are protocol-neutral `bytea`, decoded to JSON, XML, or text at the handler's edge; canonicalization is RFC 3986 normalization plus explicitly named policy; self-consistency and overlap proofs run at route-registration time. PostgreSQL 15 or later.) The tool matters less than the shape: whatever deploys your SQL should be able to run your API's entire test suite inside the deployment transaction and refuse to commit a version whose declared outcomes don't all hold.
