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
| `01-types.sql` | Request/response types + `api.json_schema` / `api.xml_schema` domains |
| `02-handler-registry.sql` | `api.handler` table for handler metadata (central registry) |
| `03-rest-routes.sql` | REST handler creation and resolution |
| `04-rpc-routes.sql` | JSON-RPC handler creation and resolution |
| `05-mcp-routes.sql` | MCP tool/resource/prompt handlers (with `tags text[]`) |
| `06-queue-infrastructure.sql` | Request/response exchange tables (`rest_exchange`, `rpc_exchange`, `mcp_exchange`) |
| `07-helpers.sql` | Response builders, error handling, tracing |
| `08-registration.sql` | Handler registration + name validation + random dollar-quote |
| `09-gateways.sql` | Gateway functions (`rest_invoke`, `rpc_invoke`, `mcp_call_tool`, `mcp_list_tools`, …) |
| `10-mcp-protocol.sql` | MCP protocol layer (`mcp_initialize`, `mcp_ping`, `mcp_handle_request`) |

**Key Types:**
- `api.rest_request` / `api.rpc_request` / `api.mcp_request` — protocol-specific request composites
- `api.http_response` / `api.mcp_response` — unified HTTP response + MCP JSON-RPC envelope
- `api.handler_type` — enum: `rest`, `rpc`, `mcp_tool`, `mcp_resource`, `mcp_prompt`
- `api.json_schema` — JSON Schema domain validated by `api.is_valid_json_schema` (rejects empty `{}`, validates keyword shapes)
- `api.xml_schema` — XSD document domain

**Key Functions:**
- `api.rest_invoke(method, url, headers, content)` — execute REST request; strips query string before regex match
- `api.rpc_invoke(route_id, headers, content)` — execute JSON-RPC request
- `api.mcp_call_tool(name, args, context, request_id jsonb)` — execute MCP tool; exceptions return `result.isError=true`
- `api.mcp_list_tools(p_tags text[] DEFAULT NULL)` — tool discovery; hides `requires_auth` tools from unauthenticated sessions; tags live under `_meta.tags`
- `api.mcp_handle_request(request, context)` — MCP JSON-RPC dispatcher; notifications receive no response (envelope NULL)
- `api.create_or_replace_rest_handler(spec, body)` / `_rpc_handler` / `_mcp_handler` — register handler; validates name against `^[a-zA-Z][a-zA-Z0-9_.-]{0,48}$`
- `api.json_response(status, body)` — build JSON HTTP response

**Handler registration contract:**
- Metadata JSONB may include `inputSchema` / `outputSchema` (validated by `api.json_schema` domain), `responseHeaders` (merged into wire headers; `x-include-schema` is a directive, not a header), `tags` (MCP only, surfaces as `_meta.tags`).
- Setting `responseHeaders.x-include-schema = 'true'` merges `outputSchema` into REST responses (as `$schema` at body root) or RPC responses (inside `result.$schema`, preserving JSON-RPC 2.0 envelope).
- Handler names are capped at 49 chars; longer names are rejected at registration to prevent PostgreSQL 63-byte identifier truncation collisions.

### `core/` - Entity Hierarchy

Base tables and patterns for domain modeling.

| File | Purpose |
|------|---------|
| `foundation.sql` | `core.entity_id` domain (opt-in marker) |
| `entity-standards.sql` | Deploy-end sweep that injects `created_at`/`deleted_at` on tables declaring `object_id core.entity_id`. No superuser required. |

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
| `cast.sql` | Safe type casting with `?>` operator and `common.try_cast(text, default)` overloads |
| `encoding.sql` | Bytea encoding domain types (`common.utf8`, `common.latin1`, `common.win1252`) and converters |
| `text.sql` | Text manipulation (regex helpers, semantic fingerprint) |

**Key Functions:**
- `common.try_cast(text, null::uuid)` - Returns NULL (or default) on invalid input, no exception
- `common.try_cast(text, null::int)` / `null::bigint` / `null::numeric` / `null::interval` / `null::timestamp` - Other typed overloads
- `common.utf8`, `common.latin1`, `common.win1252` - Bytea domains that validate encoding at cast time

### `__test__/` - Framework Tests

Tests for the framework itself (not your application).

| File | Tests |
|------|-------|
| `test_api_protocols.sql` | REST/RPC/MCP gateway functions |
| `test_auth_enforcement.sql` | Authentication requirements |
| `test_entity_standards.sql` | Entity standards reconcile (`created_at`/`deleted_at` injection, sweep + inline call) |
| `test_error_handling.sql` | Error classification and HTTP status mapping |
| `test_handler_lifecycle.sql` | Handler registration, name validation, query-string routing |
| `test_mcp_protocol.sql` | MCP `initialize` / `ping` / dispatcher + JSON-RPC 2.0 compliance |
| `test_migrations_tracking.sql` | Deployment script tracking |
| `test_schema_and_tags.sql` | `api.json_schema` domain, `$schema` injection, `_meta.tags`, auth hiding |

## Extension Points

**Don't edit lib/ internals in place.** Instead:

1. **Add handlers**: Create files in `api/` (root level)
2. **Add domain tables**: Create files in `core/` (root level, create if needed)
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
3. **Add indexes**: Create migration files for additional indexes

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
