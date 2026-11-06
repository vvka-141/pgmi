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
| **Tool** | Executable actions | `name`, `description`, `effect`, `inputSchema` |
| **Resource** | Data access via URI | `name`, `uriTemplate`, `mimeType` |
| **Prompt** | Message templates | `name`, `description`, `arguments` |

## Request and Response Types

```sql
-- Request type (passed to your handler)
CREATE TYPE api.mcp_request AS (
    arguments jsonb,      -- Tool/prompt arguments
    uri text,             -- Resource URI
    context jsonb,        -- {"client": {...}} (a verified OAuth client) or {"connection": "cn_..."} (an approved handle)
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

## What a Tool Does: `effect`

Every tool declares what calling it does, so an agent knows before the call.
Registration refuses a tool without one, naming it:

| Metadata | Meaning |
|---|---|
| `"effect": "read"` | changes nothing |
| `"effect": "write", "reversible": true` | makes a change that can be undone afterwards |
| `"effect": "write", "reversible": false` | makes a change that cannot (a delete, a cancellation) |
| `"openWorldHint": true` | optional, default false: the tool reaches outside this database |

The MCP annotations are derived from it and never written by hand
(`"annotations"` in the metadata is refused): a read is `readOnlyHint` and
`idempotentHint`, a write that cannot be undone is `destructiveHint`.
`tools/list` publishes them as `annotations`, and the effect itself under
`_meta["pgmi/effect"]`. A resource or a prompt declares none of these: reading
one changes nothing.

## Schema Contracts and Tags

Handler registration metadata accepts the following optional fields:

- **`inputSchema`** — JSON Schema (`api.json_schema` domain) describing arguments. MCP requires a root `"type": "object"`; registration rejects anything else. Before your handler runs, the gateway reads the arguments leniently where the intent is unambiguous — a lone string where only an array of strings is declared becomes a one-item array, an integral number (`10.0`) where only an integer is declared becomes `10`, through nested properties, array items and `#/$defs` — and then checks them at every depth (`properties`, `items`, `#/$defs`): `type`, `enum`, `const`, `required`, `additionalProperties: false`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `minLength`, `maxLength`, `minItems`, `maxItems`, `uniqueItems` and the schema `false`. A violation comes back as an `isError` result naming the argument, which the model can correct. `format` and `pattern` are published as hints for the model and not checked; validate them in the handler (see [the tool-body recipe](#tool-bodies-refuse-never-raise)). Registration refuses an `inputSchema` that uses any other keyword (`oneOf`, `if`, `multipleOf`, `prefixItems`, an object `additionalProperties`, ...) or a `#/$defs` reference that names no definition, so what `tools/list` publishes is what is enforced; REST and RPC registration refuse the same.
- **`$ref`** in a tool's `inputSchema` or `outputSchema` must be local to the schema (`"#/$defs/<name>"`): an MCP client resolves the schema on its own and cannot follow `#/components/...` or another document. Registration refuses anything else.
- **`outputSchema`** — JSON Schema describing results; root `"type": "object"` required. For MCP tools, surfaces in `tools/list` and enables spec-compliant `structuredContent` emission (see below). For REST/RPC, triggers `$schema` merge when `responseHeaders.x-include-schema='true'` — REST merges into body (wrapping arrays/scalars as `{data, $schema}`), RPC merges into `result.$schema` (never top-level, to keep JSON-RPC 2.0 compliant).
- **`responseHeaders`** (jsonb) — arbitrary headers merged into the wire response (keys lowercased). The key `x-include-schema` is a directive, not a header; it controls `$schema` merge and is stripped before the response reaches the client.
- **`tags`** (MCP only, `text[]`) — surfaces in `tools/list` under `_meta.tags` (MCP spec extension slot). `mcp_list_tools(p_tags)` filters by overlap; NULL or empty array = no filter.

```sql
-- Tool with outputSchema and tags, emitting structuredContent
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id',   'a0000001-0001-4000-8000-000000000001'::uuid,
        'type', 'tool',
        'effect', 'read',
        'name', 'weather_at',
        'description', 'Get weather for a location',
        'access', 'authenticated',
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

Declaring `outputSchema` obliges the handler to pass `structured_content` on
every successful result. MCP says a tool with an `outputSchema` returns content
that conforms to it, and the reference SDKs reject a result that omits
`structuredContent`. Registration cannot see what the body returns, so the
check runs on every call: a success without `structuredContent`, or one that
breaks the `outputSchema`, becomes the sanitized `Tool execution failed` result
and its writes roll back. Pin it with a test that calls the tool and checks
`result.structuredContent`. Error results are exempt.

Handler names are validated against `^[a-zA-Z][a-zA-Z0-9_.-]{0,48}$` at
registration. Names over 49 chars are rejected to prevent PostgreSQL 63-byte
identifier truncation collisions on the generated function
(`mcp_tool_<name>`, `rpc_<name>`, etc.).

A handler may also declare a per-route transaction policy
(`minTransactionIsolation`; `readOnly` is refused, since every MCP exchange is logged) — see
[the gateway's transaction-policy contract](MCP-GATEWAY.md#transaction-policy).

## Tool Example

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0001-4000-8000-000000000001',
        'type', 'tool',
        'effect', 'read',
        'name', 'database_info',
        'description', 'Get database version and connection info',
        'access', 'authenticated',
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

## Tool Bodies: Refuse, Never Raise

A tool answers every call, a wrong one included: a refusal is an `isError`
result the model reads and acts on, never an exception. The gateway turns an
exception into an `isError` result too, but only with a class message
("A submitted value is malformed"), which tells the model nothing about which
argument to fix. Four habits keep a body honest:

1. **Read every argument into a typed local first,** with `?>` (the try-cast
   operator), never a bare `::` cast. A value that does not cast is NULL and
   becomes a problem, not an error.
2. **Collect every problem at once,** as `{name, reason}` pairs (reasons
   `required`, `invalid`, `out_of_range`, `unknown`), and refuse with all of
   them. One round trip per mistake wastes the model's turns.
3. **Refuse in one shape:** an `isError` result whose text is a JSON object
   `{code, detail, invalidParams}`, with the same codes REST uses
   (`pgmi.invalid_value`, `pgmi.invalid_pagination`, or your own
   `<schema>.<entity>.<reason>` codes).
4. **Bound what you return:** a `detail` argument (`summary` by default,
   `full` on request) and `limit` / `offset` paging, with `hasMore`, so a list
   never floods the context.

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0004-4000-8000-000000000001',
        'type', 'tool', 'effect', 'read',
        'name', 'list_tasks',
        'description', 'Tasks of one project, newest first',
        'access', 'authenticated',
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'required', jsonb_build_array('project'),
            'properties', jsonb_build_object(
                'project', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'states',  jsonb_build_object('type', 'array', 'items', jsonb_build_object('enum', jsonb_build_array('open', 'done'))),
                'detail',  jsonb_build_object('enum', jsonb_build_array('summary', 'full')),
                'limit',   jsonb_build_object('type', 'integer', 'minimum', 1, 'maximum', 100),
                'offset',  jsonb_build_object('type', 'integer', 'minimum', 0)))),
    $body$
DECLARE
    v_args     jsonb := (request).arguments;
    v_project  uuid := (v_args->>'project') ?> NULL::uuid;
    v_detail   text := COALESCE(v_args->>'detail', 'summary');
    v_limit    integer := COALESCE((v_args->>'limit') ?> NULL::integer, 20);
    v_offset   integer := COALESCE((v_args->>'offset') ?> NULL::integer, 0);
    v_problems jsonb;
    v_rows     jsonb;
BEGIN
    SELECT jsonb_agg(api.invalid_param(name, reason) ORDER BY name) INTO v_problems
    FROM (VALUES
        ('project', 'invalid',      v_project IS NULL),
        ('states',  'invalid',      EXISTS (SELECT FROM jsonb_array_elements_text(COALESCE(v_args->'states', '[]')) s
                                            WHERE s NOT IN ('open', 'done'))),
        ('detail',  'invalid',      v_detail NOT IN ('summary', 'full')),
        ('limit',   'out_of_range', v_limit NOT BETWEEN 1 AND 100),
        ('offset',  'out_of_range', v_offset < 0)
    ) p(name, reason, failed)
    WHERE failed;
    IF v_problems IS NOT NULL THEN
        RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text(jsonb_build_object(
            'code', 'pgmi.invalid_value',
            'detail', 'Fix the arguments named in invalidParams and call again',
            'invalidParams', v_problems)::text)), (request).request_id, true);
    END IF;

    SELECT jsonb_agg(CASE v_detail WHEN 'full' THEN to_jsonb(t)
                     ELSE jsonb_build_object('id', t.id, 'title', t.title, 'state', t.state) END
                     ORDER BY t.created_at DESC, t.id)
    INTO v_rows
    FROM (SELECT * FROM core.task
          WHERE project_id = v_project
            AND (v_args->'states' IS NULL OR state = ANY (ARRAY(SELECT jsonb_array_elements_text(v_args->'states'))))
          ORDER BY created_at DESC, id
          LIMIT v_limit + 1 OFFSET v_offset) t;

    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text(format('%s task(s)', LEAST(jsonb_array_length(COALESCE(v_rows, '[]')), v_limit)))),
        (request).request_id, false,
        jsonb_build_object(
            'tasks', COALESCE((SELECT jsonb_agg(e) FROM jsonb_array_elements(v_rows) WITH ORDINALITY x(e, n) WHERE n <= v_limit), '[]'),
            'hasMore', jsonb_array_length(COALESCE(v_rows, '[]')) > v_limit));
END;
    $body$
);
```

`core.task` stands for your own table. The framework already refused a
missing `project` and a `states` that is not an array before the body ran,
and read a lone `"open"` as `["open"]`; the body owns everything inside the
values. `lib/__test__/test_mcp_tool_contract.sql` calls every registered tool
with wrongly typed arguments generated from its own `inputSchema`, and fails
the deploy when a body raises instead of refusing.

## Resource Example

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0002-4000-8000-000000000001',
        'type', 'resource',
        'name', 'table_schema',
        'description', 'Get table schema information',
        'access', 'authenticated',
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
        'access', 'authenticated',
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

MCP uses the `context` parameter for authentication, not HTTP headers. It
carries one of two things the gateway puts there: `client`, the issuer,
subject and client id of an OAuth access token the gateway verified, or
`connection`, the handle of a connection the person approved on the web
(`POST /connections`, signed in, naming the organization and an agent role).
The database resolves either into the connection's agent, acting for that
person in the organization chosen at approval, with the role's permissions
intersected with the person's. Every other context key is ignored, and
nothing on MCP provisions anyone. See
[MCP identity](MCP-GATEWAY.md#mcp-identity).

```sql
-- Gateway forwards the connection handle (Authorization: Bearer cn_... or X-Connection) as context:
SELECT api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"1","method":"tools/call",...}'::jsonb,
    '{"connection": "cn_..."}'::jsonb
);

-- Inside handlers, use the typed readers:
DECLARE
    v_principal uuid := api.current_principal_id();         -- the person (effective principal)
    v_agent     uuid := api.current_actual_principal_id();  -- the connection's agent (actual principal)
    v_kind      text := api.current_actor_kind();           -- 'agent' on MCP; 'human' for a person's own call
    v_org       uuid := api.current_organization_id();
BEGIN
    -- Use for kernel arguments, or to word an answer for an agent
END;
```

Row-level security and permissions use the person and the organization.
The audit log, change tracking and the exchange log already record both
principals and the actor kind; a handler does not log them itself.

Handlers never read an `auth.*` setting or a context key by name; a deploy test
fails on any handler that does.

### Requiring Authentication

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        ...
        'access', 'authenticated'
    ),
    $body$...
);
```

If the handler is not declared `'access', 'public'` and the context resolves to no live
connection of an enabled, current member — missing, unknown, expired, or revoked — the gateway answers exactly as
it does for a name that does not exist, so an anonymous caller cannot probe for
protected tools (they are already hidden from its `tools/list`):
```json
{"jsonrpc": "2.0", "id": "...", "error": {"code": -32602, "message": "Tool not found: my_tool (or it requires authentication)"}}
```

A client the database knows but cannot let act yet (a pairing not yet
approved, a paused connection, agents switched off) gets `-31001` with
`data.code` instead, before any handler runs, and a handle that names no
live connection is HTTP 401 from the gateway; see
[Troubleshooting](MCP-GATEWAY.md#troubleshooting).

## Testing

### Direct SQL Testing

```sql
-- Discover the server (MCP 2026-07-28; legacy clients send initialize)
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"1","method":"server/discover"}'::jsonb
)).envelope;

-- Test tools/list
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"2","method":"tools/list"}'::jsonb
)).envelope;

-- Test tools/call
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"3","method":"tools/call","params":{"name":"database_info","arguments":{}}}'::jsonb
)).envelope;

-- Test as a person: approve a connection for their membership (in a
-- __test__ file, pg_temp.test_mcp_context does that and returns the context)
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"4","method":"tools/call","params":{"name":"execute_query","arguments":{"query":"SELECT 1"}}}'::jsonb,
    pg_temp.test_mcp_context('dev|alice', '<organization id>')
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
            'id', 'ffffffff-0661-4000-8000-000000000001',
            'type', 'tool',
            'effect', 'read',
            'name', 'test_tool',
            'description', 'Test',
            'inputSchema', '{"type":"object"}'::jsonb,
            'access', 'public'
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
    v_response := api.mcp_call_tool('test_tool', '{}'::jsonb, NULL, '"test-1"'::jsonb);
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
- [Testing](../TESTING.md) — database testing with savepoint isolation and deploy gates
