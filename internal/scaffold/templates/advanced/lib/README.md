# Framework Library (`lib/`)

This directory contains the framework code that powers the advanced template. It provides HTTP routing, entity management, deployment tracking, and utility functions.

**Most users should NOT modify these files.** Instead, extend functionality in root-level directories (`api/`, `core/`, `utils/`).

## Directory Structure

```
lib/
├── api/                    # HTTP framework (REST, RPC, MCP)
├── core/                   # Entity hierarchy and domain patterns
├── internal/               # Deployment tracking infrastructure
├── utils/                  # Type casting and text utilities
└── __test__/               # Framework tests
```

## Subdirectories

### `api/` - HTTP Framework

Multi-protocol HTTP handling for REST, JSON-RPC, and MCP.

| File | Purpose |
|------|---------|
| `01-types.sql` | Request/response types for each protocol |
| `02-handler-registry.sql` | `api.http_route` table for handler metadata |
| `03-rest-routes.sql` | REST handler creation and resolution |
| `04-rpc-routes.sql` | JSON-RPC handler creation and resolution |
| `05-mcp-routes.sql` | MCP tool/resource/prompt handlers |
| `06-queue-infrastructure.sql` | Async request queue for background processing |
| `07-helpers.sql` | Response builders, error handling, tracing |
| `08-registration.sql` | Handler registration functions |
| `09-gateways.sql` | Gateway functions (`rest_invoke`, `rpc_invoke`, `mcp_call_tool`) |

**Key Types:**
- `api.http_request` / `api.http_response` - HTTP request/response structures
- `api.rest_request` / `api.rpc_request` / `api.mcp_request` - Protocol-specific requests
- `api.handler_type` - Enum: `rest`, `rpc`, `mcp_tool`, `mcp_resource`, `mcp_prompt`

**Key Functions:**
- `api.rest_invoke(method, url, headers, content)` - Execute REST request
- `api.rpc_invoke(route_id, headers, content)` - Execute RPC request
- `api.mcp_call_tool(name, args, context, request_id)` - Execute MCP tool
- `api.create_or_replace_rest_handler(spec, body)` - Register REST handler
- `api.json_response(status, body)` - Build JSON HTTP response

### `core/` - Entity Hierarchy

Base tables and patterns for domain modeling.

| File | Purpose |
|------|---------|
| `foundation.sql` | Entity hierarchy (`entity`, `managed_entity`) |
| `attached-properties.sql` | Key-value property attachment system |

**Entity Hierarchy:**
```
core.entity                 # Base: object_id, created_at
    └── core.managed_entity # Adds: deleted_at (soft-delete)
```

**Usage Pattern:**
```sql
-- Your domain table inherits from managed_entity
CREATE TABLE core.customer (
    email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL
) INHERITS (core.managed_entity);

-- Automatically has: object_id, created_at, deleted_at
```

### `internal/` - Deployment Tracking

Infrastructure for pgmi deployment and test execution.

| File | Purpose |
|------|---------|
| `foundation.sql` | Test script generation (`generate_test_script()`) |

**Key Functions:**
- `internal.generate_test_script(pattern)` - Generate executable test SQL
- `internal.pvw_unittest_script(pattern)` - Parameterized view of test scripts

**Key Tables:**
- `internal.deployment_script_execution_log` - Tracks deployed scripts
- `internal.unittest_script` - Persisted test execution plan

### `utils/` - Utility Functions

Pure utility functions with no side effects.

| File | Purpose |
|------|---------|
| `cast_utils.sql` | Safe type casting (`try_cast_uuid`, `try_cast_int`, etc.) |
| `text_utils.sql` | Text manipulation (`slugify`, `trim_whitespace`, etc.) |

**Key Functions:**
- `utils.try_cast_uuid(text)` - Returns NULL on invalid input (no exception)
- `utils.try_cast_int(text)` - Safe integer conversion
- `utils.try_cast_timestamp(text)` - Safe timestamp conversion

### `__test__/` - Framework Tests

Tests for the framework itself (not your application).

| File | Tests |
|------|-------|
| `test_api_protocols.sql` | REST/RPC/MCP gateway functions |
| `test_auth_enforcement.sql` | Authentication requirements |
| `test_error_handling.sql` | Error classification and HTTP status mapping |
| `test_handler_lifecycle.sql` | Handler registration and resolution |
| `test_migrations_tracking.sql` | Deployment script tracking |

## Extension Points

**DON'T modify lib/ files.** Instead:

1. **Add handlers**: Create files in `api/` (root level)
2. **Add domain tables**: Create files in `core/` (root level, create if needed)
3. **Add utilities**: Create files in `utils/` (root level, create if needed)

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
