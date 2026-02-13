# Model Context Protocol (MCP) Integration

pgmi's advanced template includes a complete MCP server implementation, enabling AI assistants (Claude Desktop, VS Code Copilot, etc.) to interact with your PostgreSQL database through tools, resources, and prompts.

## Overview

The Model Context Protocol (MCP) is an open standard that allows AI applications to connect to external systems. pgmi implements MCP entirely in PostgreSQL, with a thin HTTP gateway for transport.

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────────────────────┐
│   AI Client     │────▶│  HTTP Gateway   │────▶│         PostgreSQL              │
│ (Claude, etc.)  │     │ (Python/Go)     │     │                                 │
│                 │◀────│                 │◀────│  api.mcp_handle_request()       │
└─────────────────┘     └─────────────────┘     │  ├── tools (execute actions)    │
                                                │  ├── resources (read data)      │
                                                │  └── prompts (message templates)│
                                                └─────────────────────────────────┘
```

## Quick Start

### 1. Deploy the Advanced Template

```bash
pgmi init --template advanced myproject
cd myproject
pgmi deploy --connection "postgresql://user:pass@localhost:5432/mydb"
```

### 2. Start the HTTP Gateway

```bash
cd tools
pip install -r requirements.txt
export DATABASE_URL="postgresql://user:pass@localhost:5432/mydb"
python mcp-gateway.py
```

### 3. Test the Connection

```bash
# Initialize handshake
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}'

# List available tools
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"2","method":"tools/list"}'

# Call a tool
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"3","method":"tools/call","params":{"name":"database_info","arguments":{}}}'
```

### 4. Configure Your AI Client

For Claude Desktop, add to `~/.config/claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "my-database": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

## Architecture

### Protocol Compliance

pgmi implements MCP using JSON-RPC 2.0:

- **Supported protocol versions**: `2024-11-05`, `2025-03-26`
- **Transport**: HTTP POST to `/mcp` endpoint
- **Authentication**: Context parameter (not HTTP headers)

### Core Functions

| Function | Purpose |
|----------|---------|
| `api.mcp_handle_request(request, context)` | Unified dispatcher - routes all MCP methods |
| `api.mcp_initialize(params, request_id)` | Handshake handler |
| `api.mcp_ping(request_id)` | Keepalive response |
| `api.mcp_call_tool(name, args, context, id)` | Invoke a tool |
| `api.mcp_read_resource(uri, context, id)` | Read a resource |
| `api.mcp_get_prompt(name, args, context, id)` | Expand a prompt |
| `api.mcp_list_tools()` | Discovery: list all tools |
| `api.mcp_list_resources()` | Discovery: list all resources |
| `api.mcp_list_prompts()` | Discovery: list all prompts |

### Method Routing

The dispatcher (`api.mcp_handle_request`) routes requests by method:

| Method | Handler |
|--------|---------|
| `initialize` | `api.mcp_initialize()` |
| `notifications/initialized` | No-op (acknowledgment) |
| `ping` | `api.mcp_ping()` |
| `tools/list` | `api.mcp_list_tools()` |
| `tools/call` | `api.mcp_call_tool()` |
| `resources/list` | `api.mcp_list_resources()` |
| `resources/read` | `api.mcp_read_resource()` |
| `prompts/list` | `api.mcp_list_prompts()` |
| `prompts/get` | `api.mcp_get_prompt()` |

## Creating Handlers

### MCP Types

| Type | Purpose | Key Metadata |
|------|---------|--------------|
| **Tool** | Executable actions | `name`, `description`, `inputSchema` |
| **Resource** | Data access via URI | `name`, `uriTemplate`, `mimeType` |
| **Prompt** | Message templates | `name`, `description`, `arguments` |

### Request and Response Types

```sql
-- Request type (passed to your handler)
CREATE TYPE api.mcp_request AS (
    arguments jsonb,      -- Tool/prompt arguments
    uri text,             -- Resource URI
    context jsonb,        -- Auth: {"user_id": "...", "tenant_id": "..."}
    request_id text       -- Must echo in response
);

-- Response type (JSON-RPC 2.0 envelope)
CREATE TYPE api.mcp_response AS (
    envelope jsonb        -- {jsonrpc, id, result} or {jsonrpc, id, error}
);
```

### Tool Example

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

### Resource Example

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

### Prompt Example

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

## Response Builders

### Success Responses

```sql
-- Tool result (content array)
api.mcp_tool_result(content jsonb, request_id text) RETURNS api.mcp_response

-- Resource result (contents array)
api.mcp_resource_result(contents jsonb, request_id text) RETURNS api.mcp_response

-- Prompt result (messages array)
api.mcp_prompt_result(messages jsonb, request_id text) RETURNS api.mcp_response

-- Generic success
api.mcp_success(result jsonb, request_id text) RETURNS api.mcp_response
```

### Error Responses

```sql
-- Generic JSON-RPC error
api.mcp_error(code integer, message text, request_id text) RETURNS api.mcp_response

-- Convenience wrappers (use -32603 Internal Error)
api.mcp_tool_error(message text, request_id text) RETURNS api.mcp_response
api.mcp_resource_error(message text, request_id text) RETURNS api.mcp_response
api.mcp_prompt_error(message text, request_id text) RETURNS api.mcp_response
```

### Content Helpers

```sql
-- Text content block
api.mcp_text('Hello, world!')
-- Returns: {"type": "text", "text": "Hello, world!"}
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

If `requiresAuth` is true and `user_id` is missing from context, the gateway returns:
```json
{"jsonrpc": "2.0", "id": "...", "error": {"code": -32001, "message": "Authentication required"}}
```

## Error Handling

### JSON-RPC Error Codes

| Code | Meaning |
|------|---------|
| -32700 | Parse error (invalid JSON) |
| -32600 | Invalid Request (missing jsonrpc, method) |
| -32601 | Method not found |
| -32602 | Invalid params |
| -32603 | Internal error |
| -32001 | Authentication required (custom) |

### Example Error Response

```json
{
  "jsonrpc": "2.0",
  "id": "req-1",
  "error": {
    "code": -32601,
    "message": "Tool not found: unknown_tool"
  }
}
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

## HTTP Gateway

The advanced template includes a Python gateway (`tools/mcp-gateway.py`) that bridges HTTP to PostgreSQL. This file is generated when you run `pgmi init --template advanced`.

**Requirements:**
- Python 3.8+
- `psycopg2` or `psycopg` (PostgreSQL adapter)
- `flask` (HTTP server)

### Starting the Gateway

```bash
cd myproject/tools
pip install psycopg2-binary flask
export DATABASE_URL="postgresql://user:pass@localhost:5432/mydb"
python mcp-gateway.py
```

### Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/mcp` | POST | MCP JSON-RPC endpoint |
| `/health` | GET | Health check (for load balancers) |

### Authentication Headers

The gateway extracts authentication from HTTP headers:

| Header | Maps to |
|--------|---------|
| `X-User-Id` | `context.user_id` |
| `X-Tenant-Id` | `context.tenant_id` |

### Production Deployment

For production, consider:

1. **Reverse Proxy**: Place behind nginx/Caddy that validates JWTs and injects X-User-Id
2. **Connection Pooling**: Use PgBouncer for connection management
3. **WSGI Server**: Run with gunicorn for multiple workers
4. **TLS**: Terminate SSL at the load balancer

```bash
# Example with gunicorn
gunicorn -w 4 -b 0.0.0.0:8080 'mcp-gateway:app'
```

## Built-in Example Tools

The advanced template includes these example handlers:

| Name | Type | Description |
|------|------|-------------|
| `database_info` | Tool | Get database version and connection info |
| `list_tables` | Tool | List tables in a schema with row counts |
| `describe_table` | Tool | Get column definitions for a table |
| `table_schema` | Resource | Get table column metadata via URI |
| `sql_assistant` | Prompt | Generate SQL query assistant prompts |

## Server Configuration

Configure server identity via session settings:

```sql
SET mcp.server_name = 'my-database-server';
SET mcp.server_version = '2.0.0';
```

Or set in `postgresql.conf` for persistence:
```
mcp.server_name = 'production-db'
mcp.server_version = '1.0.0'
```

## Troubleshooting

### Common Issues

**"Method not found" error**
- Check that your handler is registered: `SELECT * FROM api.mcp_route;`
- Verify the handler type matches the method (tool for tools/call, etc.)

**"Authentication required" error**
- Pass context with user_id: `'{"user_id":"test|123"}'::jsonb`
- Or set `requiresAuth: false` for public tools

**Handler not appearing in discovery**
- Check the handler was created: `SELECT * FROM api.handler WHERE handler_type LIKE 'mcp_%';`
- Verify MCP route exists: `SELECT * FROM api.mcp_route;`

### Debugging

Enable debug logging:
```sql
SET client_min_messages = DEBUG;
SELECT (api.mcp_handle_request('{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"my_tool","arguments":{}}}'::jsonb)).envelope;
```

Check exchange logs:
```sql
SELECT * FROM api.mcp_exchange ORDER BY created_at DESC LIMIT 10;
```

## See Also

- [session-api.md](session-api.md) — Session tables and helper functions
- [SECURITY.md](SECURITY.md) — Secrets handling and security patterns
- [TESTING.md](TESTING.md) — Database testing with automatic rollback
