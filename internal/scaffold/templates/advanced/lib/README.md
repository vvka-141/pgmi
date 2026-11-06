# Framework Library (`lib/`)

This directory contains the framework code that powers the advanced template. It provides HTTP routing, entity management, deployment tracking, and utility functions.

**Don't edit these files in place** — they're coupled and tested. Extend functionality in root-level directories (`api/`, `core/`, `common/`). You *can* remove whole capabilities you don't need — see "Trimming the template" in the [template README](../README.md) for what's safe to delete and what's load-bearing.

## Directory Structure

```
lib/
├── api/                    # HTTP framework (REST, RPC, MCP)
├── core/                   # Entity hierarchy and domain patterns
├── common/                 # Cross-cutting primitives (casting, encoding, text)
└── __test__/               # Framework tests
```

## Subdirectories

### `api/` - HTTP Framework

Multi-protocol HTTP handling for REST, JSON-RPC, and MCP.

| File | Purpose |
|------|---------|
| `00-transaction-isolation.sql` | Transaction policy contract: normalizer, ordering, gateway validation primitives, resolve-then-open resolver |
| `01-types-validators.sql` | Validators behind the `api.json_schema` / `api.xml_schema` domains |
| `01-types.sql` | Request/response types + `api.json_schema` / `api.xml_schema` domains (run once) |
| `02-handler-registry.sql` | `api.handler` table for handler metadata (central registry, run once) |
| `02a-access-control.sql` | `api.handler_permission`, `api.access_denial` (declared access, run once) |
| `02a-access-control-actor.sql` | `api.access_denial.actor_kind` (run once) |
| `02a-access-control-functions.sql` | Permission hook, `internal.access_denial_reason`, refusal log and purge |
| `03-rest-routes.sql` | REST route table (run once) |
| `03-rest-routes-functions.sql` | Route ranking: `internal.route_specificity`, `internal.route_shape` (re-run; before the table that uses them) |
| `04-rpc-routes.sql` | JSON-RPC route table (run once) |
| `05-mcp-routes.sql` | MCP tool/resource/prompt route table, with `tags text[]` and a tool's derived `annotations` and `effect` (run once) |
| `06-queue-infrastructure.sql` | The exchange log: abstract `api.exchange` (with the replay `context`) and its `rest_exchange`, `rpc_exchange`, `mcp_exchange` children, each with its indexes: `object_id`, `enqueued_at`, `x-request-id` and the actual principal (run once) |
| `07-helpers.sql` | Response builders, error handling, tracing; the optional page helper (`api.page_response`, `api.page_output_schema`, `api.page_query_schema`) |
| `08-registration.sql` | Handler registration + name validation + random dollar-quote |
| `09-gateways.sql` | Gateway functions (`rest_invoke`, `rpc_invoke`, `mcp_call_tool`, `mcp_list_tools`, …) |
| `10-mcp-protocol.sql` | MCP protocol layer: both eras on one endpoint (`server/discover` and `_meta` versions for 2026-07-28, `mcp_initialize` / `mcp_ping` for legacy clients), `mcp_handle_request` |
| `11-openapi.sql` | OpenAPI 3.1 generator over the handler registry, served at `GET /openapi.json` |
| `12-maintenance.sql` | Background jobs: the driver `api.job_run` and the run-log writers `api.job_run_started` / `api.job_run_finished`. Exchange retention: `api.exchange_retention()` (deploy parameter `exchange_retention_days`), `api.purge_exchanges_batch`, the `purge_exchanges` job (`api.job_purge_exchanges`) |
| `views.sql` | Analysis and summary views over handlers, routes and exchanges |

**Key Types:**
- `api.rest_request` / `api.rpc_request` / `api.mcp_request` — protocol-specific request composites
- `api.http_response` / `api.mcp_response` — unified HTTP response + MCP JSON-RPC envelope
- `api.handler_type` — enum: `rest`, `rpc`, `mcp_tool`, `mcp_resource`, `mcp_prompt`
- `api.json_schema` — JSON Schema domain validated by `api.is_valid_json_schema` (rejects empty `{}`, validates keyword shapes)
- `api.xml_schema` — XSD document domain

**Key Functions:**
- `api.rest_invoke(method, url, headers, content)` — execute REST request; canonicalizes the path, matches the declared routes, enforces the declared querySchema
- `api.rpc_invoke(route_id, headers, content)` — execute JSON-RPC request
- `api.mcp_call_tool(name, args, context, request_id jsonb)` — execute MCP tool; exceptions return `result.isError=true`
- `api.mcp_list_tools(p_tags text[] DEFAULT NULL)` — tool discovery; lists only the tools `internal.current_access_denial` lets the caller call; tags live under `_meta.tags`
- `api.mcp_handle_request(request, context)` — MCP JSON-RPC dispatcher for both eras; notifications receive no response (envelope NULL); every handler envelope is checked (request id, a tool result's `content`)
- `api.create_or_replace_rest_handler(spec, body)` / `_rpc_handler` / `_mcp_handler` — register handler; validates name against `^[a-zA-Z][a-zA-Z0-9_.-]{0,48}$`
- `api.json_response(status, body)` — build JSON HTTP response

**Handler registration contract:**
- Every registration declares exactly one of `access` (`public` | `authenticated` | `organization`) or `permissions` (a non-empty array; any one opens the handler). Neither, both, an empty or unknown permission, or the retired `requiresAuth` key fails the registration and the deploy, naming the handler. Permissions come from the application's closed vocabulary through the hook `internal.is_platform_permission(id)` (the framework default knows none).
- The gateways decide once, before the handler runs, with `internal.current_access_denial(handler, access)` over `internal.access_denial_reason`: REST answers 401 (`unauthenticated`) or 403 (`pgmi.organization_required`, `pgmi.forbidden`) with fixed bodies; RPC answers -32001 or -32003; MCP hides what an anonymous caller cannot call and answers -31003 to a signed-in one. Each refusal is written to `api.access_denial`, collapsed per minute (server log only in a READ ONLY transaction); `api.purge_access_denials(interval)` is the retention. OpenAPI lists each operation's `x-pgmi-permissions`. REST routing is one function, `internal.rest_route_lookup(method, canonical_path)`, shared by `api.rest_invoke`, `api.rest_route_policy` and the access simulation (`membership.fn_simulate_access`).
- REST and RPC read identity only from two opaque headers, `x-login-session` and `x-organization-session`, and pass them to the hook `internal.resolve_session_context(login, organization)`. MCP reads one of two context keys through `internal.resolve_mcp_caller(context)`: `client` (`{issuer, subject, clientId}`, an access token the gateway verified) goes to the hook `internal.resolve_client_context(issuer, subject, client)`, which may return why the client cannot act yet (answered as JSON-RPC -31001 with `data.code`); otherwise `connection` (an approved connection's handle) goes to the hook `internal.resolve_connection_context(connection)`, which may return the same kind of refusal. A handle that resolves to no one and no refusal answers -31001 `pgmi.connection_unknown`, which the gateway sends as HTTP 401. Every other context key is ignored. The framework defaults resolve nothing. Header names that collide after lowercasing are refused with 400.
- The application supplies those hooks and the readers the decision calls: `api.current_principal_id()`, `api.current_actual_principal_id()`, `api.current_actor_kind()` (`human`, `agent`, `service`, or `system` when the request names no one), `api.current_organization_id()`, `api.current_permissions()`. The access-denial log and change tracking record the actual principal and the actor kind; the exchange log stores whatever `internal.request_log_context()` returns. Handlers use the readers, never an `auth.*` setting or a session header by name.
- Metadata JSONB may include `inputSchema` / `outputSchema` (validated by `api.json_schema` domain), `responseHeaders` (merged into wire headers; `x-include-schema` is a directive, not a header), `tags` (MCP only, surfaces as `_meta.tags`). An MCP tool declares `effect` (`read`, or `write` with `reversible`) and optionally `openWorldHint`; its annotations are derived, and a `$ref` in its schemas must be local (`#/$defs/...`).
- Setting `responseHeaders.x-include-schema = 'true'` merges `outputSchema` into REST responses (as `$schema` at body root) or RPC responses (inside `result.$schema`, preserving JSON-RPC 2.0 envelope).
- Handler names are capped at 49 chars; longer names are rejected at registration to prevent PostgreSQL 63-byte identifier truncation collisions.

**Routing contract (`03-rest-routes-functions.sql`, `08-registration.sql`, `09-gateways.sql`):**
- A REST route is a `path` template, `/projects/{id:uuid}`; a parameter takes a whole segment and is `{name}` (text), `{name:uuid}` or `{name:int}`; an unknown type is refused. `uri`, `example` and `version` are registration errors; version in the path, `/v2/projects`.
- The handler reads the values from `(request).path_params` by name, percent-decoded. A malformed `uuid` or `int` (`^-?[0-9]+$`) answers 400 `pgmi.invalid_path_parameter` with `invalid-params`, after the access decision. `api.rest_route.path_params` holds the declared types; it is in `api.catalog_version()`, and OpenAPI publishes `uuid` as string/uuid and `int` as integer.
- A literal segment outranks a parameter: `internal.rest_route_lookup` orders the matching routes by `specificity` (one digit per segment, 1 literal, 0 parameter), so `/projects/stats` beats `/projects/{id}` in any registration order. It tests the literal prefix with `starts_with` before the regex.
- Registration refuses what the ranking cannot order: a segment mixing text and a parameter (`{name}.json`), a parameter name used twice, a path not in canonical form, and a second route of the same shape (`internal.route_shape`) with an intersecting method.
- Method sets are public: OPTIONS (204, before identity) and 405 list every method of the path, whoever asks.

**Transaction policy contract (`00-transaction-isolation.sql`):**
Per-route isolation floor (`minTransactionIsolation`), read-only declaration (`readOnly`; REST and RPC only, and unlogged, because a READ ONLY transaction cannot write the exchange log), resolve-then-open, `DEFERRABLE` derivation, fail-closed DB-side check, and replica-safe hint. Full reference: [Transaction policy](https://vvka-141.github.io/pgmi/docs/advanced/transaction-policy/).

**Cacheable contract (`08-registration.sql`, `11-openapi.sql`):**
- `api.catalog_version()` digests exactly the registry columns the OpenAPI document is built from (`internal.catalog_digest()`), plus the generator's version (`internal.openapi_generator_version()`, bumped when the derivation changes), so it changes **iff** the published contract changes. A statement trigger moves `api.catalog_state.generation` on every registry write, and each connection caches the digest of the generation it last saw. `test_openapi_agreement.sql` fails when the generator reads a column the digest omits. Deliberately *not* `api.handler.def_hash` (that hashes the function body and would miss a changed path/auth/schema) and *not* a digest of the rendered document.
- Each operation publishes one 2xx, its `successStatus` (default 200; 204 has no content), and every client error the gateway derives from its metadata with the shared `Problem` body (RFC 9457): 400 for a body, a typed path parameter or a `querySchema`; 401/403 from its access; 428 from `readOnly` or an isolation floor. `requestBody.required` is set when `inputSchema` is. `test_openapi_agreement.sql` checks all of it against the registry.
- Named schemas: `api.create_or_replace_schema_component(name, schema)` publishes `components.schemas.<name>`; a handler schema references it as `{"$ref": "#/components/schemas/<name>"}`. A `$ref` naming no component is refused at registration, a component no operation uses fails the agreement test, and a bare-`$ref` `inputSchema` is checked as the component it names.
- `GET /openapi.json` returns a strong `ETag` and `Cache-Control: no-cache` — cache it, but revalidate. A conditional `If-None-Match` returns **304 with no body** (measured: 7 KB → 0). `If-None-Match` may be a list or `*`.
- **Every** REST/RPC response — success or error — stamps `x-pgmi-catalog-version` with the same digest. A client that preloads routes learns its contract went stale from a call it was already making, instead of polling the spec. Without that, a client-side route cache serves a false 404 for a route a later deploy added; the caching is what makes client-side routing *safe*, not merely fast.

**Serialization-failure retry contract (`09-gateways.sql`):**
`40001`/`40P01` propagate with SQLSTATE intact; catching them in PL/pgSQL is unsafe (commits a lost write); retry belongs to whoever owns `BEGIN`. Full reference: [Transaction policy — retry contract](https://vvka-141.github.io/pgmi/docs/advanced/transaction-policy/#serialization-failure-retry-contract).

### `internal/` - Environment Label, Job Run Log

| File | Purpose |
|------|---------|
| `environment.sql` | `internal.environment`: the one-row `dev`/`test`/`prod` label the first deploy writes and every redeploy is checked against (run once) |
| `job-run-log.sql` | `internal.job_run_log`: one row per background-job run, live, succeeded or abandoned (run once) |
| `views.sql` | Deploy history: `internal.vw_deployment` (one row per deploy) and `internal.vw_deployed_script` (one row per script) |

### `core/` - Entity Hierarchy

Base tables and patterns for domain modeling.

| File | Purpose |
|------|---------|
| `foundation-structure.sql` | `core.entity_id` domain (opt-in marker) and the `core.ensure_view` fingerprint table (run once) |
| `foundation.sql` | `core.ensure_view`, `core.apply_org_rls` and `core.execute_with_force_rls_lifted` |
| `entity-standards.sql` | Deploy-end sweep that injects `created_at`/`deleted_at` on tables declaring `object_id core.entity_id`. No superuser required. |
| `catalogs.sql` | `pg_temp.sync_catalog(table)`: makes a table hold exactly the rows of `data/catalogs/<schema>.<table>.json` |
| `change-tracking.sql`, `change-tracking-actor.sql`, `change-tracking-functions.sql` | Opt-in change tracking: `core.apply_change_tracking(table)`, the trigger `core.capture_change`, the log `core.change_event` (applied to no table by default) |
| `entity-registry.sql`, `entity-registry-functions.sql` | Opt-in entity registry: `core.register_entity(table, type, title_expression)` and the generated `core.vw_entity` (registers no table by default) |
| `organization-counter.sql`, `organization-counter-functions.sql` | `core.next_organization_number(counter)`: per-organization numbering |

**Opt-In Entity Standards (dual-mode):**

The deploy-end sweep automatically finds every table with `object_id core.entity_id` and injects `created_at`/`deleted_at`. No per-table boilerplate required.

```sql
CREATE TABLE core.customer (
    object_id core.entity_id PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL
);
-- created_at/deleted_at injected automatically by deploy-end sweep
```

When you need the lifecycle columns immediately (e.g., for a partial index), call the reconcile function inline:

```sql
CREATE TABLE core.customer (
    object_id core.entity_id PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL
);
DO $$ BEGIN PERFORM pg_temp.apply_entity_table_standards('core.customer'); END $$;
CREATE INDEX ix_customer_active ON core.customer(email) WHERE deleted_at IS NULL;
```

The inline call is idempotent — the later sweep re-touching the same table is free. Index strategy is left to you — the reconcile does not create any indexes.

### `common/` - Cross-Cutting Primitives

Pure utility functions and domain types shared across every schema.

| File | Purpose |
|------|---------|
| `cast.sql` | Safe type casting: `common.try_cast(text, default)` overloads (uuid, boolean, integer, bigint, numeric, interval, timestamp, timestamptz, date, inet); whitespace ignored, NaN/infinity/overflow give the default |
| `cast-operators.sql` | The `common.?>` try-cast operator over `try_cast` (run-once: operators have no `OR REPLACE`). Every pinned `search_path` names `common`, so it resolves everywhere (`lib/__test__/test_common_reach.sql`) |
| `encoding-structure.sql` | Bytea encoding wrapper types (`common.utf8`, `common.latin1`, `common.win1252`) (run once) |
| `encoding.sql` | Casts and converters for the encoding types |
| `text.sql` | Text manipulation (regex helpers, `like_escape`, semantic fingerprint) |

**Key Functions:**
- `common.try_cast(text, null::uuid)` - Returns NULL (or default) on invalid input, no exception
- `common.try_cast(text, null::int)` / `null::bigint` / `null::numeric` / `null::interval` / `null::timestamp` / `null::date` / `null::inet` - Other typed overloads
- `common.utf8`, `common.latin1`, `common.win1252` - Bytea domains that validate encoding at cast time

### `__test__/` - Framework Tests

Tests for the framework itself (not your application).

| File | Tests |
|------|-------|
| `_setup.sql` | Fixture: the development identity issuers the tests' people sign in from |
| `test_analytics_views.sql` | Handler, route and exchange analysis views |
| `test_api_protocols.sql` | REST/RPC/MCP gateway functions |
| `test_auth_enforcement.sql` | Authentication requirements |
| `test_catalog_sync.sql` | `pg_temp.sync_catalog`: insert, update, delete; refuses unknown columns, missing or duplicate keys, referenced rows |
| `test_entity_standards.sql` | Entity standards reconcile (`created_at`/`deleted_at` injection, sweep + inline call) |
| `test_error_handling.sql` | Error classification and HTTP status mapping |
| `test_exact_grants.sql` | The gateway executes exactly its entry points, the admin login and PUBLIC nothing directly, the kernel no owner SECURITY DEFINER routine, the jobs login (created and registered in the test when absent) every `api.job_*` routine and nothing else, an unregistered one nothing; every SECURITY DEFINER pins `search_path` |
| `test_exchange_redaction.sql` | Exchange logs never store credentials |
| `test_forced_rls.sql` | Every application table is forced to row-level security with a policy, or exempt with a reason |
| `test_force_rls_lifted.sql` | `core.execute_with_force_rls_lifted` sees every tenant and restores FORCE; no application role may call it |
| `test_handler_context_reads.sql` | No registered handler and no `api.job_*` routine reads an `auth.*` setting or a session header by name |
| `test_handler_lifecycle.sql` | Handler registration, name validation, query-string routing |
| `test_contract_conformance.sql` | Every declared contract element (auth, security scheme, produces, outputSchema, query, transaction policy) is enforced; MCP listings match the registry |
| `test_declared_access.sql` | Registration refuses a missing, doubled, malformed or unknown access declaration; REST/RPC/MCP refuse with 401/403, log collapsed per minute, never run the body; listings and OpenAPI agree |
| `test_header_case_collision.sql` | Header names that collide after lowercasing are refused (REST 400, RPC 400 + -32600) |
| `test_http_semantics.sql` | Status-code classes, mandatory headers, content negotiation |
| `test_jobs.sql` | The job driver refuses bad arguments before any lock; the run-log writers lock, abandon and finish only their own runs; a job inside a transaction block cannot commit |
| `test_maintenance.sql` | Exchange retention: the `purge_exchanges` job batch and the admin endpoint |
| `test_mcp_protocol.sql` | Both MCP eras, result builders, handler envelope checks, JSON-RPC 2.0 compliance |
| `test_mcp_tool_contract.sql` | Tool effects and derived annotations, local `$ref` only, lenient argument reading; no tool body raises on wrongly typed arguments |
| `test_migrations_tracking.sql` | Deployment script tracking |
| `test_openapi.sql` | OpenAPI 3.1 generator |
| `test_openapi_agreement.sql` | OpenAPI document agrees with the router |
| `test_query_schema.sql` | `querySchema` is enforced (400 `pgmi.invalid_query` with `invalid-params`) and published (`in: query`); unenforceable keywords are refused |
| `test_error_contract.sql` | SQLSTATE → status and `pgmi.*` code over REST/RPC/MCP; HEAD errors without content; gateway 400 shapes; readOnly refusals; framework code prefix |
| `test_redeploy_safety.sql` | Redeploy safety and logging suppression |
| `test_rls_helper.sql` | `core.apply_org_rls()` multi-tenant RLS helper |
| `test_route_anchoring.sql` | Routes do not catch each other's traffic |
| `test_route_spelling.sql` | Every valid spelling of a path reaches the right handler |
| `test_schema_and_tags.sql` | `api.json_schema` domain, `$schema` injection, `_meta.tags`, auth hiding |
| `test_transaction_isolation.sql` | Per-route isolation floor enforcement |
| `test_transaction_policy.sql` | Read-only routes and resolve-then-open |
| `test_approved_schemas.sql` | The application schemas and their grants are exactly `pg_temp.approved_schema_grant()` in deploy.sql |
| `test_change_tracking.sql` | Every table `core.apply_change_tracking` lists keeps its trigger, and records each change once with person and agent |
| `test_dev_entry_points.sql` | `api.open_dev_login_session` exists only while the database is dev |
| `test_entity_registry.sql` | The entity registry lists registered rows under their own row-level security |
| `test_error_answer_rollback.sql` | An error answer (REST/RPC 400+, an MCP error or `isError`) leaves no write behind; its exchange row still commits |
| `test_exchange_indexes.sql` | Every exchange-log lookup the template ships uses an index |
| `test_exchange_log.sql` | The exchange log finds a request by its id, stores its context, and replays REST, RPC and MCP calls as their caller inside a rollback |
| `test_fk_index_coverage.sql` | Every foreign key has an index leading with its columns, or an exemption with a reason |
| `test_handler_metadata_keys.sql` | A handler metadata key no registration function reads is refused, naming the key it was meant to be |
| `test_mcp_error_codes.sql` | MCP error codes stay out of the JSON-RPC reserved range |
| `test_mcp_request_policy.sql` | MCP refusals before dispatch (`api.mcp_request_policy`): header mismatch, version, missing `_meta` |
| `test_mcp_results.sql` | A tool's `structuredContent` conforms to its `outputSchema`; a missing resource is -32602 |
| `test_openapi_components.sql` | `successStatus`, schema components and the catalog version |
| `test_option_handler_reads_views.sql` | Option guard: handlers read `core.vw_*` views, not tables |
| `test_option_naming.sql` | Option guard: views, constraints and indexes follow one naming rule set |
| `test_option_plpgsql_check.sql` | Option guard: every PL/pgSQL routine passes `plpgsql_check` |
| `test_option_rls_index_method.sql` | Option guard: no lookup index but btree on a forced table |
| `test_option_sql_lint.sql` | Option guard: routine and view bodies avoid four known traps (NULL comparison, `NOT IN (SELECT`, swallowed errors, `money`) |
| `test_option_strict_fk_index.sql` | Option guard: a key with a referential action has an exact index |
| `test_option_trigger_allowlist.sql` | Option guard: every trigger on a core table is listed |
| `test_option_view_column_comments.sql` | Option guard: every core view and its columns carry a comment |
| `test_organization_counter.sql` | Each organization's counters count on their own |
| `test_policy_drift.sql` | Every row policy is as the deploy's files left it; a policy edited by hand fails the deploy |
| `test_response_contract.sql` | Every 409 and 422 carries a code; every page route keeps the page contract |
| `test_rls_initplan.sql` | Every identity reader in a row policy is wrapped as `(SELECT ...)`, so it runs once per query |
| `test_schema_enforcement.sql` | Declared schemas are enforced at every depth: MCP tool arguments, REST bodies, RPC `params`, with `accepts` and `produces` |
| `test_tenant_foreign_keys.sql` | A foreign key between two organizations' tables carries the organization column |
| `test_view_standards.sql` | Every application view is `security_invoker` |

## Extension Points

**Don't edit lib/ internals in place.** Instead:

1. **Add handlers**: Create files in `api/` (root level)
2. **Add domain tables**: Create run-once files (`idempotent="false"`) in `core/` (root level, create if needed)
3. **Add cross-cutting helpers**: Create files in `common/` (root level, create if needed)

**Execution Order:**
- Framework files use sortKeys `001`-`004`
- Your files should use sortKeys `005` or higher

```sql
/*
<pgmi-meta id="your-uuid" idempotent="true">
  <sortKeys>
    <key>005/001</key>  <!-- Executes after framework -->
  </sortKeys>
</pgmi-meta>
*/
```

## Customizing Framework Behavior

If you need to modify framework behavior:

1. **Override functions**: Create a file in root `api/` with the same function signature
2. **Extend types**: Create composite types in root directories
3. **Add indexes**: Create run-once files (`idempotent="false"`) for additional indexes

**Example - Adding a custom response helper:**

```sql
-- api/my_helpers.sql (sortKey: 005/001)
CREATE OR REPLACE FUNCTION api.xml_response(p_status INT, p_body XML)
RETURNS api.http_response AS $$
BEGIN
    RETURN ROW(
        p_status,
        extensions.hstore(ARRAY['Content-Type', 'application/xml']),
        p_body::text::bytea
    )::api.http_response;
END;
$$ LANGUAGE plpgsql IMMUTABLE;
```

## See Also

- [ARCHITECTURE.md](../ARCHITECTURE.md) - Design philosophy and layered architecture
- [README.md](../README.md) - Quick start and protocol examples
