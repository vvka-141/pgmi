---
name: pgmi-api-architecture
description: "REST/RPC/MCP protocol design and HTTP template architecture"
user_invocable: false
---


**Purpose**: Documents the fundamental architectural principle that REST, RPC, and MCP in pgmi's advanced template are all HTTP-native protocols designed to run behind a web server/gateway.

**Auto-Load With**:
- `pgmi-http-review` (HTTP compliance)
- `pgmi-mcp` (MCP implementation)
- File patterns: `**/api/*.sql`, `**/gateways.sql`
- Keywords: "gateway", "protocol", "REST", "RPC", "MCP"

**When to Use**: Understanding protocol design, implementing gateways, reviewing authentication, debugging status codes.

---

## Deployment Architecture

**CRITICAL**: pgmi HTTP handlers run **behind a web server or API gateway**. PostgreSQL is NOT directly exposed to the internet.

```
Internet → [Web Server/Gateway] → PostgreSQL
              │
              ├─ TLS termination
              ├─ Rate limiting & DoS protection
              ├─ Header validation & size limits
              ├─ JWT/OAuth token validation
              ├─ CORS headers
              └─ Error sanitization
```

### Responsibility Boundary

| Concern | Gateway | PostgreSQL |
|---------|---------|------------|
| TLS/HTTPS | ✅ | ❌ |
| Rate limiting | ✅ | ❌ |
| DoS protection (header sizes, etc.) | ✅ | ❌ |
| Token validation (JWT/OAuth) | ✅ | ❌ |
| CORS | ✅ | ❌ |
| Error sanitization | ✅ | ❌ |
| Request routing | ✅ (to PostgreSQL) | ✅ (within handlers) |
| Business logic | ❌ | ✅ |
| Authorization (RLS) | ❌ | ✅ |
| Data validation | ❌ | ✅ (constraints) |
| Transaction management | ❌ | ✅ |
| Complex operations | ❌ | ✅ (single roundtrip) |

### Why This Separation?

1. **Best tool for each job**: Web servers excel at TLS, rate limiting, header parsing. PostgreSQL excels at transactions, data operations, RLS.

2. **Minimize roundtrips**: Gateway validates token once, then PostgreSQL handles entire business operation in single transaction.

3. **Defense in depth**: Multiple security layers (gateway + RLS + constraints).

4. **Flexibility**: Swap gateway implementation without changing PostgreSQL code.

### What This Means for Reviews

When reviewing pgmi HTTP code, do NOT flag as issues:
- Missing DoS protection (gateway responsibility)
- Missing header size validation (gateway responsibility)
- SQLERRM leakage (gateway sanitizes errors)
- Missing CORS headers (gateway responsibility)
- Missing rate limiting (gateway responsibility)

DO flag as issues:
- Incorrect HTTP status codes (affects gateway routing decisions)
- Missing RLS policies (PostgreSQL security)
- SQL injection vulnerabilities (PostgreSQL security)
- Incorrect transaction handling (PostgreSQL correctness)
- Business logic errors (PostgreSQL correctness)

---

## Core Principle

> **In pgmi's advanced template, REST, RPC, and MCP are all HTTP protocols.**
>
> They differ only in **routing mechanism** and **body format**.
> They share **HTTP transport**, **HTTP semantics**, and **authentication model**.

This means:
- HTTP status codes indicate transport-level success/failure for ALL protocols
- Authentication failures return HTTP 401 (not protocol-specific errors hidden in 200 OK)
- Security scanners, API gateways, and monitoring tools see correct HTTP semantics

---

## Protocol Comparison

| Aspect | REST | RPC | MCP |
|--------|------|-----|-----|
| **Routing** | URL pattern + HTTP method | Method name → UUID | Name + type (tool/resource/prompt) |
| **Request body** | Various (JSON, form, etc.) | JSON-RPC 2.0 | MCP protocol |
| **Response body** | Various / RFC 7807 errors | JSON-RPC 2.0 | MCP protocol |
| **Returns** | `api.http_response` | `api.http_response` | `api.mcp_response` |
| **Auth failure** | HTTP 401 | HTTP 401 + JSON-RPC -32001 | HTTP 200 + isError (MCP convention) |

---

## HTTP Status Code Rules

### The Fundamental Rule

> HTTP status codes describe **transport-level** success or failure. If the HTTP layer rejects a request (authentication, not found, bad request), the status code reflects that—regardless of application protocol.

### Status Code Mapping by Protocol

| Condition | REST | RPC | MCP |
|-----------|------|-----|-----|
| Success | 200/201/204 | 200 | 200 + result object |
| Not found | 404 | 404 (via -32601) | 200 + error object (-32601) |
| Auth required | **401** | **401** (via -32001) | 200 + error object (-32001) |
| Bad request | 400 | 400 (via -32600/-32602) | 200 + error object (-32602) |
| Server error | 500 | 500 (via -32603) | 200 + error object (-32603) |

**Note**: MCP always returns HTTP 200 with JSON-RPC 2.0 envelope containing either `result` or `error` object.

### Why RPC Uses HTTP 401 for Auth Failures

RPC handlers are HTTP endpoints. External systems observe HTTP status codes:
- **Security scanners**: Detect 401 as authentication failure
- **API gateways**: Can enforce auth policies based on status
- **Monitoring**: Track 4xx/5xx rates accurately

The JSON-RPC error body (`-32001`) provides application-level detail for clients.

### Why MCP Uses 200 + JSON-RPC Error Object

MCP is designed for AI agent communication using JSON-RPC 2.0:
- Transport always succeeds (HTTP 200)
- Errors are communicated via JSON-RPC 2.0 `error` object in envelope
- Response contains either `result` (success) or `error` (failure), never both
- AI agents parse the envelope to understand results

This is MCP/JSON-RPC 2.0 convention, not a pgmi deviation.

---

## Authentication Patterns

### The Principle

> Authentication is an **HTTP-layer concern**. Unauthenticated requests should be rejected at the transport layer before reaching application logic.

### Implementation by Protocol

**REST** (via HTTP headers):
```sql
IF v_route.requires_auth AND (p_headers->'x-user-id') IS NULL THEN
    RETURN api.problem_response(401, 'Unauthorized', 'x-user-id header missing');
END IF;
```

**RPC** (via HTTP headers):
```sql
IF v_handler.requires_auth AND (p_headers->'x-user-id') IS NULL THEN
    RETURN api.jsonrpc_error(-32001, 'x-user-id header missing', v_json_id);
    -- -32001 maps to HTTP 401
END IF;
```

**MCP** (via context parameter - MCP spec aligned):
```sql
IF p_context IS NOT NULL THEN
    PERFORM set_config('auth.user_id', p_context->>'user_id', true);
END IF;

IF v_handler.requires_auth AND current_setting('auth.user_id', true) IS NULL THEN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('user_id missing from context')),
        p_request_id,
        true  -- isError
    );
END IF;
```

### Unified Session Variables

All protocols populate the same session variables when authenticated:
- `auth.user_id` - Primary user identifier
- `auth.tenant_id` - Tenant/organization identifier
- `auth.user_email` - User email (REST/RPC only)
- `auth.token` - Authorization header (REST/RPC only)

These enable RLS/CLS regardless of which protocol the request used.

---

## Gateway Function Signatures

All gateways return HTTP-compatible types:

```sql
-- REST: Full HTTP semantics
api.rest_invoke(method, url, headers, content) → api.http_response

-- RPC: HTTP response with JSON-RPC body
api.rpc_invoke(route_id, headers, content) → api.http_response

-- MCP: Protocol-specific response (HTTP 200 implied)
api.mcp_call_tool(name, arguments, context, request_id) → api.mcp_response
api.mcp_read_resource(uri, context, request_id) → api.mcp_response
api.mcp_get_prompt(name, arguments, context, request_id) → api.mcp_response
```

---

## Rich Document Pattern

### Principle: Return Everything the UI Needs

API handlers should return complete, self-contained response documents. The caller should never need a follow-up query to render the result.

```sql
-- ❌ BAD: Returns only IDs, forces client to make N+1 requests
SELECT jsonb_build_object(
    'projectId', c_project.object_id,
    'ownerId', c_project.owner_object_id
);

-- ✅ GOOD: Returns complete document with resolved references
SELECT jsonb_build_object(
    'projectId', c_project.object_id,
    'title', c_project.title,
    'owner', jsonb_build_object(
        'id', c_owner.object_id,
        'name', c_owner.display_name,
        'email', c_owner.email
    ),
    'activityCount', (SELECT count(*) FROM project_design.activity a WHERE a.project_object_id = c_project.object_id),
    'createdAt', c_project.created_at
);
```

### Mutations Return the Complete Document

After CREATE, UPDATE, or DELETE, return the full entity state — not just an ID or status.

```sql
-- ❌ BAD: Returns only the new ID
INSERT INTO project_design.project (title, owner_object_id)
VALUES (p_title, v_user_id)
RETURNING jsonb_build_object('id', object_id);

-- ✅ GOOD: Returns the complete document (client can render immediately)
WITH c_inserted AS (
    INSERT INTO project_design.project (title, owner_object_id)
    VALUES (p_title, v_user_id)
    RETURNING *
)
SELECT jsonb_build_object(
    'projectId', c_inserted.object_id,
    'title', c_inserted.title,
    'owner', jsonb_build_object(
        'id', c_owner.object_id,
        'name', c_owner.display_name
    ),
    'createdAt', c_inserted.created_at
)
FROM c_inserted
INNER JOIN membership.user c_owner ON c_owner.object_id = c_inserted.owner_object_id;
```

### Why This Matters

- **Eliminates N+1 queries**: Client gets everything in one roundtrip
- **Transactional consistency**: All data in the response comes from the same transaction snapshot
- **Gateway simplicity**: Gateway forwards the response as-is, no post-processing
- **AI agent ergonomics**: Agents can act on the response without additional tool calls

---

## Common Mistakes

### ❌ Treating RPC as "JSON-RPC over HTTP"

**Wrong mental model**: "HTTP is just transport, always return 200, put errors in body"

**Correct**: pgmi RPC is "HTTP with JSON-RPC formatting" - HTTP status codes matter.

```sql
-- ❌ WRONG: Always HTTP 200
RETURN api.json_response(200, jsonrpc_error_body);

-- ✅ CORRECT: HTTP status reflects error type
RETURN api.jsonrpc_error(-32001, 'Auth required', id);  -- Returns HTTP 401
```

### ❌ Using HTTP 401 for MCP Auth Failures

**Wrong**: Returning HTTP 401 for MCP tool auth failure

**Correct**: MCP uses JSON-RPC 2.0 error object with HTTP 200 (JSON-RPC convention)

```sql
-- ❌ WRONG: HTTP error for MCP
RETURN api.problem_response(401, 'Unauthorized', ...);

-- ✅ CORRECT: MCP error pattern (JSON-RPC 2.0)
RETURN api.mcp_error(-32001, 'user_id missing from context', p_request_id);
```

### ❌ Checking Headers for MCP Authentication

**Wrong**: Using HTTP headers for MCP identity

**Correct**: MCP uses `context` parameter (spec-aligned)

```sql
-- ❌ WRONG: HTTP header pattern
IF (p_headers->'x-user-id') IS NULL THEN ...

-- ✅ CORRECT: MCP context pattern
IF (p_context->>'user_id') IS NULL THEN ...
```

### ❌ Different Session Variables per Protocol

**Wrong**: REST uses `auth.user_id`, MCP uses `mcp.user_id`

**Correct**: All protocols populate the same `auth.*` session variables

---

## JSON-RPC Error Code to HTTP Status Mapping

The `api.jsonrpc_error()` function maps JSON-RPC codes to HTTP status:

| JSON-RPC Code | Meaning | HTTP Status |
|---------------|---------|-------------|
| -32700 | Parse error | 400 |
| -32600 | Invalid request | 400 |
| -32601 | Method not found | 404 |
| -32602 | Invalid params | 400 |
| -32603 | Internal error | 500 |
| -32001 | **Unauthorized** | **401** |
| Other | Server error | 500 |

---

## Integration with Other Skills

- **`pgmi-mcp`**: MCP-specific implementation patterns
- **`pgmi-http-review`**: HTTP compliance review guidelines
- **`pgmi-sql-change-protocol`**: Mandatory workflow for SQL changes

---

**Last Updated**: 2025-12-31
**Incident Reference**: RPC auth failures returning HTTP 200 instead of 401
**Lesson**: REST/RPC are HTTP-first (status codes matter); MCP is JSON-RPC 2.0 (uses error objects)

