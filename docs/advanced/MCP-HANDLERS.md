---
title: "Author MCP handlers"
description: "Write MCP tools, resources, and prompts in SQL: types, schema contracts, tags, authentication, and testing."
weight: 50
---

# Author MCP Tools, Resources, and Prompts

> **Scope: advanced template only.** Handlers are SQL functions in your
> project, registered with `api.create_or_replace_mcp_handler`. New to this
> subsystem? Start with the [overview](MCP.md).

## MCP Types

| Type | Purpose | Key Metadata |
|------|---------|--------------|
| **Tool** | Executable actions | `name`, `description`, `inputSchema` |
| **Resource** | Data access via URI | `name`, `uriTemplate`, `mimeType` |
| **Prompt** | Message templates | `name`, `description`, `arguments` |

## Request and Response Types

```sql
-- Request type (passed to your handler)
CREATE TYPE api.mcp_request AS (
    arguments jsonb,      -- Tool/prompt arguments
    uri text,             -- Resource URI
    context jsonb,        -- Auth: {"user_id": "...", "tenant_id": "..."}
    request_id jsonb      -- JSON-RPC id (string, integer, or null); must echo in response
);

-- Response type (JSON-RPC 2.0 envelope)
CREATE TYPE api.mcp_response AS (
    envelope jsonb        -- {jsonrpc, id, result} or {jsonrpc, id, error}
);
```

`request_id` is **jsonb** across the MCP API so JSON-RPC 2.0 id types (string,
integer, null) round-trip verbatim. Passing a raw text literal (`'req-1'`)
fails domain parsing — use `'"req-1"'::jsonb` or `'42'::jsonb`.

## Schema Contracts and Tags

Handler registration metadata accepts the following optional fields:

- **`inputSchema`** — JSON Schema (`api.json_schema` domain) describing arguments. Rejected if empty `{}` or malformed.
- **`outputSchema`** — JSON Schema describing results. For MCP tools, surfaces in `tools/list` and enables spec-compliant `structuredContent` emission (see below). For REST/RPC, triggers `$schema` merge when `responseHeaders.x-include-schema='true'` — REST merges into body (wrapping arrays/scalars as `{data, $schema}`), RPC merges into `result.$schema` (never top-level, to keep JSON-RPC 2.0 compliant).
- **`responseHeaders`** (jsonb) — arbitrary headers merged into the wire response (keys lowercased). The key `x-include-schema` is a directive, not a header; it controls `$schema` merge and is stripped before the response reaches the client.
- **`tags`** (MCP only, `text[]`) — surfaces in `tools/list` under `_meta.tags` (MCP spec extension slot). `mcp_list_tools(p_tags)` filters by overlap; NULL or empty array = no filter.

```sql
-- Tool with outputSchema and tags, emitting structuredContent
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id',   'a0000001-0001-4000-8000-000000000001'::uuid,
        'type', 'tool',
        'name', 'weather_at',
        'description', 'Get weather for a location',
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object('location', jsonb_build_object('type', 'string')),
            'required', jsonb_build_array('location')
        ),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object('temp_c', jsonb_build_object('type', 'number'))
        ),
        'tags', jsonb_build_array('weather', 'read-only')
    ),
    $body$
DECLARE v_structured jsonb;
BEGIN
    v_structured := jsonb_build_object('temp_c', 21.5);
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text(v_structured::text)),
        (request).request_id,
        false,
        v_structured
    );
END;
    $body$
);
```

`api.mcp_tool_result(content, request_id, is_error, structured_content)` — when
a `structured_content` jsonb is passed, MCP clients that support outputSchema can
validate the structured payload directly instead of re-parsing the text content.

Handler names are validated against `^[a-zA-Z][a-zA-Z0-9_.-]{0,48}$` at
registration. Names over 49 chars are rejected to prevent PostgreSQL 63-byte
identifier truncation collisions on the generated function
(`mcp_tool_<name>`, `rpc_<name>`, etc.).

A handler may also declare a per-route transaction policy
(`minTransactionIsolation`, `readOnly`) — see
[the gateway's transaction-policy contract](MCP-GATEWAY.md#transaction-policy).

## Tool Example

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0001-4000-8000-000000000001',
        'type', 'tool',
        'name', 'database_info',
        'description', 'Get database version and connection info',
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(),
            'required', jsonb_build_array()
        )
    ),
    $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text(format(
            'Database: %s, Version: %s, User: %s',
            current_database(),
            version(),
            current_user
        ))),
        (request).request_id
    );
END;
    $body$
);
```

## Resource Example

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0002-4000-8000-000000000001',
        'type', 'resource',
        'name', 'table_schema',
        'description', 'Get table schema information',
        'uriTemplate', 'postgres:///{schema}/{table}',
        'mimeType', 'application/json'
    ),
    $body$
DECLARE
    v_uri_parts text[];
    v_schema text;
    v_table text;
    v_columns jsonb;
BEGIN
    -- Parse URI: postgres:///public/users -> ['public', 'users']
    v_uri_parts := string_to_array(
        regexp_replace((request).uri, '^postgres:///', ''), '/'
    );
    v_schema := v_uri_parts[1];
    v_table := v_uri_parts[2];

    -- Query column metadata
    SELECT jsonb_agg(jsonb_build_object(
        'column_name', column_name,
        'data_type', data_type,
        'is_nullable', is_nullable
    ))
    INTO v_columns
    FROM information_schema.columns
    WHERE table_schema = COALESCE(v_schema, 'public')
      AND table_name = v_table;

    RETURN api.mcp_resource_result(
        jsonb_build_array(jsonb_build_object(
            'uri', (request).uri,
            'mimeType', 'application/json',
            'text', COALESCE(v_columns, '[]'::jsonb)::text
        )),
        (request).request_id
    );
END;
    $body$
);
```

## Prompt Example

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0003-4000-8000-000000000001',
        'type', 'prompt',
        'name', 'sql_assistant',
        'description', 'Generate a SQL query assistant prompt',
        'arguments', jsonb_build_array(
            jsonb_build_object('name', 'task', 'description', 'Task description', 'required', true),
            jsonb_build_object('name', 'tables', 'description', 'Relevant tables', 'required', false)
        )
    ),
    $body$
DECLARE
    v_task text;
    v_tables text;
BEGIN
    v_task := (request).arguments->>'task';
    v_tables := COALESCE((request).arguments->>'tables', 'any relevant tables');

    RETURN api.mcp_prompt_result(
        jsonb_build_array(
            jsonb_build_object(
                'role', 'user',
                'content', jsonb_build_object(
                    'type', 'text',
                    'text', format(
                        'You are a PostgreSQL expert. Help me write a SQL query for: %s. Consider using: %s.',
                        v_task, v_tables
                    )
                )
            )
        ),
        (request).request_id
    );
END;
    $body$
);
```

## Authentication

MCP uses the `context` parameter for authentication, not HTTP headers:

```sql
-- Gateway extracts from HTTP headers and passes as context:
SELECT api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"1","method":"tools/call",...}'::jsonb,
    '{"user_id": "auth0|12345", "tenant_id": "org_abc"}'::jsonb
);

-- Inside handlers, access via session variables:
DECLARE
    v_user_id text := current_setting('auth.user_id', true);
    v_tenant_id text := current_setting('auth.tenant_id', true);
BEGIN
    -- Use for RLS, audit logging, etc.
END;
```

### Requiring Authentication

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        ...
        'requiresAuth', true  -- Default: true
    ),
    $body$...
);
```

If `requiresAuth` is true and the context's `user_id` does not resolve to an active
user — missing, malformed, unknown, or deactivated — the gateway returns:
```json
{"jsonrpc": "2.0", "id": "...", "error": {"code": -32001, "message": "Authentication required"}}
```

## Testing

### Direct SQL Testing

```sql
-- Test initialize
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}'::jsonb
)).envelope;

-- Test tools/list
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"2","method":"tools/list"}'::jsonb
)).envelope;

-- Test tools/call
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"3","method":"tools/call","params":{"name":"database_info","arguments":{}}}'::jsonb
)).envelope;

-- Test with authentication context
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"4","method":"tools/call","params":{"name":"execute_query","arguments":{"query":"SELECT 1"}}}'::jsonb,
    '{"user_id":"test|123"}'::jsonb
)).envelope;
```

### Testing Handlers in Isolation

```sql
DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    -- Register test handler
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', 'ffffffff-test-4000-8000-000000000001',
            'type', 'tool',
            'name', 'test_tool',
            'description', 'Test',
            'inputSchema', '{}'::jsonb,
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('success')),
        (request).request_id
    );
END;
        $body$
    );

    -- Invoke
    v_response := api.mcp_call_tool('test_tool', '{}'::jsonb, NULL, 'test-1');
    v_envelope := (v_response).envelope;

    -- Verify
    IF v_envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'Missing jsonrpc 2.0';
    END IF;
    IF v_envelope->>'id' != 'test-1' THEN
        RAISE EXCEPTION 'request_id not preserved';
    END IF;
    IF v_envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'Unexpected error';
    END IF;

    RAISE NOTICE 'Test passed';
END $$;
```

## See Also

- [MCP SQL API reference](MCP-SQL-API.md) — every dispatcher function and response builder
- [Run the MCP gateway](MCP-GATEWAY.md) — serve what you authored
- [Testing](../TESTING.md) — database testing with automatic rollback
