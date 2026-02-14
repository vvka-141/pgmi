---
name: pgmi-mcp
description: "MCP handler implementation, types, context7 integration"
user_invocable: false
---


**Purpose**: Documents MCP (Model Context Protocol) implementation patterns in pgmi's advanced template.

**Used By**:
- mcp-expert-reviewer (primary - code review)
- general-purpose (when writing MCP handlers)
- change-planner (when planning MCP features)

**Depends On**: pgmi-api-architecture (HTTP-first principle), pgmi-postgres-review (SQL patterns)

**Auto-Load With**:
- File patterns: `**/mcp*.sql`, `**/*_mcp_*.sql`, `**/05-mcp-routes.sql`, `**/10-mcp-protocol.sql`
- Keywords: "MCP", "tool", "resource", "prompt", "mcp_call_tool", "mcp_response", "mcp_handle_request"

---

## CRITICAL: Context7 Requirement

> **Before implementing or reviewing MCP handlers, use context7 to fetch the current MCP specification.**
>
> MCP (Model Context Protocol) is actively evolving. Do not rely on cached knowledge.
> Verify tool schemas, response formats, error handling, and context parameter semantics against the live spec.

```
Use context7 → "MCP Model Context Protocol specification tools resources prompts"
```

**Why this matters**:
- MCP is a new protocol under active development by Anthropic
- Spec details may change between versions
- Hardcoded knowledge becomes stale fast
- Grounded research prevents assumption errors

---

## MCP Protocol Overview

MCP defines three handler types for AI agent communication:

| Type | Purpose | Key Metadata |
|------|---------|--------------|
| **Tool** | Executable actions | `name`, `description`, `inputSchema` |
| **Resource** | Data access | `name`, `uriTemplate`, `mimeType` |
| **Prompt** | Message templates | `name`, `description`, `arguments` |

**Always consult context7 for complete spec details.**

---

## Protocol Layer (10-mcp-protocol.sql)

The protocol layer provides the core MCP server functionality:

### Server Info & Capabilities

```sql
-- Returns server identity (configurable via session settings)
api.mcp_server_info() RETURNS jsonb
-- {"name": "database_name", "version": "1.0.0"}

-- Configure via:
SET mcp.server_name = 'my-server';
SET mcp.server_version = '2.0.0';

-- Returns declared capabilities
api.mcp_server_capabilities() RETURNS jsonb
-- {"tools": {}, "resources": {}, "prompts": {}}
```

### Initialize Handshake

```sql
-- Handles MCP handshake (MUST be called first by clients)
api.mcp_initialize(p_params jsonb, p_request_id text) RETURNS api.mcp_response

-- Supported protocol versions: '2024-11-05', '2025-03-26'

-- Success response:
-- {jsonrpc: "2.0", id: "...", result: {protocolVersion, serverInfo, capabilities}}

-- Error (invalid version):
-- {jsonrpc: "2.0", id: "...", error: {code: -32602, message: "..."}}
```

### Ping Keepalive

```sql
-- Responds to liveness checks
api.mcp_ping(p_request_id text) RETURNS api.mcp_response
-- Returns: {jsonrpc: "2.0", id: "...", result: {}}
```

### Unified Dispatcher

```sql
-- Main entry point - routes all MCP requests
api.mcp_handle_request(p_request jsonb, p_context jsonb DEFAULT NULL) RETURNS api.mcp_response
```

**Method routing table:**

| Method | Handler |
|--------|---------|
| `initialize` | `api.mcp_initialize()` |
| `notifications/initialized` | No-op (returns empty success) |
| `ping` | `api.mcp_ping()` |
| `tools/list` | `api.mcp_success(api.mcp_list_tools(), id)` |
| `tools/call` | `api.mcp_call_tool()` |
| `resources/list` | `api.mcp_success(api.mcp_list_resources(), id)` |
| `resources/read` | `api.mcp_read_resource()` |
| `prompts/list` | `api.mcp_success(api.mcp_list_prompts(), id)` |
| `prompts/get` | `api.mcp_get_prompt()` |
| Unknown | Error -32601 (Method not found) |

**Usage from HTTP gateway:**
```python
result = conn.execute(
    "SELECT (api.mcp_handle_request(%s, %s)).envelope",
    [request_json, context_json]
)
```

**Usage from psql:**
```sql
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}'::jsonb
)).envelope;
```

---

## pgmi's MCP Types

### Request Type

```sql
CREATE TYPE api.mcp_request AS (
    arguments jsonb,      -- Tool arguments or prompt arguments
    uri text,             -- Resource URI
    context jsonb,        -- Authentication: {"user_id": "...", "tenant_id": "..."}
    request_id text       -- Must be preserved in response
);
```

### Response Type

```sql
CREATE TYPE api.mcp_response AS (
    envelope jsonb        -- JSON-RPC 2.0 compliant: {jsonrpc, id, result} or {jsonrpc, id, error}
);
```

**Envelope structure for success:**
```json
{"jsonrpc": "2.0", "id": "request-id", "result": {...}}
```

**Envelope structure for error:**
```json
{"jsonrpc": "2.0", "id": "request-id", "error": {"code": -32603, "message": "..."}}
```

---

## Handler Registration

### Tool Registration

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'uuid-here',
        'type', 'tool',
        'name', 'my_tool',
        'description', 'What this tool does',
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'param1', jsonb_build_object('type', 'string', 'description', '...')
            ),
            'required', jsonb_build_array('param1')
        ),
        'requiresAuth', true  -- Default: true
    ),
    $body$
DECLARE
    v_param1 text;
BEGIN
    v_param1 := (request).arguments->>'param1';

    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('Result: ' || v_param1)),
        (request).request_id
    );
END;
    $body$
);
```

### Resource Registration

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'uuid-here',
        'type', 'resource',
        'name', 'my_resource',
        'description', 'Resource description',
        'uriTemplate', 'postgres:///{schema}/{table}',
        'mimeType', 'application/json',
        'requiresAuth', true
    ),
    $body$
DECLARE
    v_data jsonb;
BEGIN
    -- Parse URI and fetch data
    -- ...

    RETURN api.mcp_resource_result(
        jsonb_build_array(jsonb_build_object(
            'uri', (request).uri,
            'mimeType', 'application/json',
            'text', v_data::text
        )),
        (request).request_id
    );
END;
    $body$
);
```

### Prompt Registration

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'uuid-here',
        'type', 'prompt',
        'name', 'my_prompt',
        'description', 'Prompt description',
        'arguments', jsonb_build_array(
            jsonb_build_object('name', 'task', 'description', 'Task description', 'required', true)
        ),
        'requiresAuth', true
    ),
    $body$
DECLARE
    v_task text;
BEGIN
    v_task := (request).arguments->>'task';

    RETURN api.mcp_prompt_result(
        jsonb_build_array(
            jsonb_build_object(
                'role', 'user',
                'content', jsonb_build_object(
                    'type', 'text',
                    'text', 'Help me with: ' || v_task
                )
            )
        ),
        (request).request_id
    );
END;
    $body$
);
```

---

## Gateway Functions

### Tool Invocation

```sql
api.mcp_call_tool(
    p_name text,           -- Tool name
    p_arguments jsonb,     -- Tool arguments
    p_context jsonb,       -- Auth: {"user_id": "...", "tenant_id": "..."}
    p_request_id text      -- Request ID to echo
) RETURNS api.mcp_response
```

### Resource Read

```sql
api.mcp_read_resource(
    p_uri text,            -- Resource URI
    p_context jsonb,       -- Auth context
    p_request_id text
) RETURNS api.mcp_response
```

### Prompt Expansion

```sql
api.mcp_get_prompt(
    p_name text,           -- Prompt name
    p_arguments jsonb,     -- Prompt arguments
    p_context jsonb,       -- Auth context
    p_request_id text
) RETURNS api.mcp_response
```

### Discovery Functions

```sql
api.mcp_list_tools() RETURNS jsonb      -- {"tools": [...]}
api.mcp_list_resources() RETURNS jsonb  -- {"resources": [...]}
api.mcp_list_prompts() RETURNS jsonb    -- {"prompts": [...]}
```

---

## Authentication via Context

### The Pattern

MCP authentication uses the `context` parameter, NOT HTTP headers. This aligns with MCP spec conventions for AI agent communication.

```sql
-- Gateway extracts identity from context:
IF p_context IS NOT NULL THEN
    IF p_context->>'user_id' IS NOT NULL THEN
        PERFORM set_config('auth.user_id', p_context->>'user_id', true);
    END IF;
    IF p_context->>'tenant_id' IS NOT NULL THEN
        PERFORM set_config('auth.tenant_id', p_context->>'tenant_id', true);
    END IF;
END IF;

-- Then enforces auth if required:
IF v_handler.requires_auth AND current_setting('auth.user_id', true) IS NULL THEN
    RETURN api.mcp_error(-32001, 'Authentication required: user_id missing from context', p_request_id);
END IF;
```

### Accessing Identity in Handlers

```sql
DECLARE
    v_user_id text;
BEGIN
    v_user_id := current_setting('auth.user_id', true);

    -- Use for RLS, audit logging, etc.
END;
```

---

## Response Builders

### Success Builders

```sql
-- Tool success (content array in result)
api.mcp_tool_result(content jsonb, request_id text) RETURNS api.mcp_response

-- Resource success (contents array in result)
api.mcp_resource_result(contents jsonb, request_id text) RETURNS api.mcp_response

-- Prompt success (messages array in result)
api.mcp_prompt_result(messages jsonb, request_id text) RETURNS api.mcp_response

-- Generic success (any result)
api.mcp_success(result jsonb, request_id text) RETURNS api.mcp_response
```

### Error Builders

```sql
-- Generic JSON-RPC error (use for protocol errors)
api.mcp_error(code integer, message text, request_id text, data jsonb DEFAULT NULL) RETURNS api.mcp_response

-- Convenience wrappers (use -32603 Internal Error)
api.mcp_tool_error(message text, request_id text) RETURNS api.mcp_response
api.mcp_resource_error(message text, request_id text) RETURNS api.mcp_response
api.mcp_prompt_error(message text, request_id text) RETURNS api.mcp_response
```

### Text Content Helper

```sql
api.mcp_text(p_text text) RETURNS jsonb
-- Returns: {"type": "text", "text": "..."}
```

### Examples

**Successful tool result:**
```sql
RETURN api.mcp_tool_result(
    jsonb_build_array(
        api.mcp_text('Operation completed'),
        api.mcp_text('Processed 42 records')
    ),
    (request).request_id
);
```

**Error tool result:**
```sql
RETURN api.mcp_tool_error('Error: ' || SQLERRM, (request).request_id);
-- Or with specific error code:
RETURN api.mcp_error(-32603, 'Error: ' || SQLERRM, (request).request_id);
```

---

## HTTP Gateway

The Python gateway (`tools/mcp-gateway.py`) bridges HTTP to PostgreSQL:

```python
# Gateway calls:
result = conn.execute(
    "SELECT (api.mcp_handle_request(%s, %s)).envelope",
    [json.dumps(request), json.dumps(context) if context else None]
)
```

**Authentication mapping:**
| HTTP Header | Context Field |
|-------------|---------------|
| `X-User-Id` | `user_id` |
| `X-Tenant-Id` | `tenant_id` |

**Starting the gateway:**
```bash
cd tools
pip install -r requirements.txt
export DATABASE_URL="postgresql://user:pass@localhost:5432/mydb"
python mcp-gateway.py
```

---

## Error Handling

### MCP Error Convention

MCP uses JSON-RPC 2.0 error objects in the response envelope, NOT HTTP status codes:

```sql
-- ✅ CORRECT: MCP error response (JSON-RPC 2.0 error object)
RETURN api.mcp_error(-32601, 'Tool not found: ' || p_name, p_request_id);
-- Returns: {jsonrpc: "2.0", id: "...", error: {code: -32601, message: "..."}}

-- ✅ ALSO CORRECT: Convenience wrapper
RETURN api.mcp_tool_error('Tool not found: ' || p_name, p_request_id);

-- ❌ WRONG: HTTP error for MCP
RETURN api.problem_response(404, 'Not Found', 'Tool not found');
```

### JSON-RPC Error Codes

| Code | Meaning |
|------|---------|
| -32700 | Parse error (invalid JSON) |
| -32600 | Invalid Request (missing jsonrpc, method) |
| -32601 | Method not found |
| -32602 | Invalid params |
| -32603 | Internal error |
| -32001 | Authentication required (custom) |

### Exception Handling in Gateways

Gateways catch exceptions and convert to JSON-RPC error responses:

```sql
BEGIN
    EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;
    RETURN v_response;
EXCEPTION WHEN OTHERS THEN
    RETURN api.mcp_error(-32603, SQLERRM, p_request_id);
END;
```

---

## Request ID Preservation

> **CRITICAL: Always preserve and return the request_id from the request.**

The request_id allows AI agents to correlate responses with requests:

```sql
-- ✅ CORRECT: Preserve request_id
RETURN api.mcp_tool_result(content, (request).request_id);

-- ❌ WRONG: Hardcoded or missing request_id
RETURN api.mcp_tool_result(content, 'some-id');
RETURN api.mcp_tool_result(content, NULL);
```

---

## Testing MCP Handlers

### Self-Contained Test Pattern

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
            'requiresAuth', false  -- For framework tests
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

    -- Test invocation
    v_response := api.mcp_call_tool('test_tool', '{}'::jsonb, NULL, 'test-req-1');
    v_envelope := (v_response).envelope;

    -- Verify JSON-RPC 2.0 structure
    IF v_envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'Missing jsonrpc 2.0';
    END IF;

    IF v_envelope->>'id' != 'test-req-1' THEN
        RAISE EXCEPTION 'request_id not preserved';
    END IF;

    IF v_envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'Unexpected error: %', v_envelope->'error'->>'message';
    END IF;

    RAISE NOTICE '✓ Test passed';
END $$;
```

### Testing via Dispatcher

```sql
-- Full round-trip test
SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}'::jsonb
)).envelope;

SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"2","method":"tools/list"}'::jsonb
)).envelope;

SELECT (api.mcp_handle_request(
    '{"jsonrpc":"2.0","id":"3","method":"tools/call","params":{"name":"database_info","arguments":{}}}'::jsonb
)).envelope;
```

### Testing Authentication

```sql
-- Test auth enforcement (missing context)
v_response := api.mcp_call_tool('protected_tool', '{}'::jsonb, NULL, 'req-1');
IF (v_response).envelope->'error' IS NULL THEN
    RAISE EXCEPTION 'Should require auth (missing error)';
END IF;

-- Test with context
v_response := api.mcp_call_tool(
    'protected_tool',
    '{}'::jsonb,
    '{"user_id": "test-user"}'::jsonb,
    'req-2'
);
IF (v_response).envelope->'error' IS NOT NULL THEN
    RAISE EXCEPTION 'Should succeed with auth, got: %', (v_response).envelope->'error'->>'message';
END IF;
```

---

## Common Mistakes

### ❌ Using HTTP Headers for MCP Auth

```sql
-- WRONG
IF (p_headers->'x-user-id') IS NULL THEN ...

-- CORRECT
IF (p_context->>'user_id') IS NULL THEN ...
```

### ❌ Returning HTTP Status for MCP Errors

```sql
-- WRONG
RETURN api.problem_response(404, 'Not Found', ...);

-- CORRECT
RETURN api.mcp_error(-32601, 'Not Found', request_id);
-- Or convenience wrapper:
RETURN api.mcp_tool_error('Not Found', request_id);
```

### ❌ Forgetting request_id

```sql
-- WRONG (also wrong type structure)
RETURN (jsonb_build_object('content', ...))::api.mcp_response;

-- CORRECT
RETURN api.mcp_tool_result(content, (request).request_id);
```

### ❌ Not Validating inputSchema

Tools should validate arguments against their inputSchema. Use context7 to verify JSON Schema validation patterns if unsure.

---

## MCP Review Checklist

- [ ] Used context7 to verify against current MCP spec?
- [ ] Handler type correct (tool/resource/prompt)?
- [ ] Required metadata present (inputSchema for tools, uriTemplate for resources)?
- [ ] Authentication uses context parameter (not headers)?
- [ ] request_id preserved in all responses (in envelope.id)?
- [ ] Errors use JSON-RPC 2.0 error object (envelope.error), not HTTP status codes?
- [ ] Handler appears in discovery functions?
- [ ] Tests use `requiresAuth: false` for framework testing?

---

## See Also

- **`pgmi-api-architecture`**: HTTP-first protocol design principles
- **`pgmi-mcp-review`**: MCP code review checklist
- **`docs/MCP.md`**: Full MCP documentation for users
- **MCP Specification**: Fetch via context7 for current details

---

**Last Updated**: 2026-02-10
**Note**: MCP is evolving - always verify patterns against current spec via context7

