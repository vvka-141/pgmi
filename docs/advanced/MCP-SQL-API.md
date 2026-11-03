---
title: "MCP SQL API reference"
description: "Reference for the advanced template's MCP SQL surface: dispatcher functions, method routing, and response builders."
weight: 60
---

# MCP SQL API Reference

> **Scope: advanced template only.** These functions are scaffolded into the
> `api` schema of your project. New to this subsystem? Start with the
> [overview](MCP.md).

## Core Functions

| Function | Purpose |
|----------|---------|
| `api.mcp_handle_request(request, context)` | Unified dispatcher for both eras — returns `NULL` envelope for JSON-RPC notifications (no response sent) |
| `api.mcp_supported_versions()` | The versions a 2026-07-28-era request may name in `_meta`: `{2026-07-28}` |
| `api.mcp_discover()` | The `server/discover` result (before `api.mcp_success` adds `resultType` and serverInfo) |
| `internal.mcp_server_instructions()` | Hook: the server instructions; NULL (default) publishes none |
| `api.mcp_initialize(params, request_id jsonb)` | Legacy handshake handler |
| `api.mcp_ping(request_id jsonb)` | Legacy keepalive response |
| `api.mcp_server_info()` | The server identity in every result's `_meta` |
| `api.mcp_cache_policy()` | `ttlMs` and `cacheScope` of the four lists and `resources/read` |
| `api.mcp_call_tool(name, args, context, id jsonb)` | Invoke a tool; handler exceptions → `result.isError=true` (not JSON-RPC error) |
| `api.mcp_read_resource(uri, context, id jsonb)` | Read a resource |
| `api.mcp_get_prompt(name, args, context, id jsonb)` | Expand a prompt |
| `api.mcp_list_tools(p_tags text[] DEFAULT NULL)` | Tool discovery; lists only the tools the caller may call (`internal.current_access_denial`); surfaces tags as `_meta.tags`; `p_tags` filter matches by overlap (NULL or empty array = no filter) |
| `api.mcp_list_resources()` | Concrete-resource discovery (`resources/list`); emits `uri` objects only; same auth-hide semantics |
| `api.mcp_list_resource_templates()` | Template discovery (`resources/templates/list`); emits `uriTemplate` objects (RFC 6570); same auth-hide semantics |
| `api.mcp_list_prompts()` | Prompt discovery; same auth-hide semantics |
| `api.mcp_request_policy(request, requested_isolation, protocol_version, mcp_method, mcp_name)` | Before dispatch: the refusal for a header, version or `_meta` problem, the era (`modern`) and the transaction policy — see [transport behavior](MCP-PROTOCOL.md#transport-behavior) and [transaction policy](MCP-GATEWAY.md#transaction-policy) |

`request_id` is **jsonb** across the MCP API so JSON-RPC 2.0 id types (string,
integer, null) round-trip verbatim. Passing a raw text literal (`'req-1'`)
fails domain parsing — use `'"req-1"'::jsonb` or `'42'::jsonb`.

## Method Routing

The dispatcher (`api.mcp_handle_request`) routes requests by method. Any
method in the `notifications/*` family (or any request missing an `id`
member) returns a NULL envelope — callers MUST NOT write anything to the wire
for notifications per JSON-RPC 2.0. Which era a request belongs to is on the
[protocol page](MCP-PROTOCOL.md#two-eras-on-one-endpoint).

| Method | Handler |
|--------|---------|
| `server/discover` | `api.mcp_discover()` |
| `initialize` | `api.mcp_initialize(params, id)` (legacy era; `-32601` on 2026-07-28) |
| `notifications/initialized` / any `notifications/*` | returns NULL envelope (no response) |
| `ping` | `api.mcp_ping(id)` (legacy era; `-32601` on 2026-07-28) |
| `tools/list` | `api.mcp_list_tools()` |
| `tools/call` | `api.mcp_call_tool(name, args, context, id)` |
| `resources/list` | `api.mcp_list_resources()` |
| `resources/templates/list` | `api.mcp_list_resource_templates()` |
| `resources/read` | `api.mcp_read_resource(uri, context, id)` |
| `prompts/list` | `api.mcp_list_prompts()` |
| `prompts/get` | `api.mcp_get_prompt(name, args, context, id)` |

## Response Builders

### Success Responses

```sql
-- Tool result (content array); optional is_error and structured_content
api.mcp_tool_result(content jsonb, request_id jsonb) RETURNS api.mcp_response
api.mcp_tool_result(content jsonb, request_id jsonb, is_error boolean, structured_content jsonb) RETURNS api.mcp_response

-- Resource result (contents array)
api.mcp_resource_result(contents jsonb, request_id jsonb) RETURNS api.mcp_response

-- Prompt result (messages array)
api.mcp_prompt_result(messages jsonb, request_id jsonb) RETURNS api.mcp_response

-- Generic success
api.mcp_success(result jsonb, request_id jsonb) RETURNS api.mcp_response
```

Every success builder adds `resultType: "complete"` and
`_meta["io.modelcontextprotocol/serverInfo"]`, keeping the result's own
`_meta` keys. `api.mcp_resource_result` adds `ttlMs` and `cacheScope`. Nulls
inside `structured_content` are kept.

### Error Responses

```sql
-- Generic JSON-RPC error; data is kept as given, nulls inside it included
api.mcp_error(code integer, message text, request_id jsonb, data jsonb DEFAULT NULL) RETURNS api.mcp_response

-- Tool error: a successful tool result with isError = true, not a JSON-RPC error
api.mcp_tool_error(message text, request_id jsonb) RETURNS api.mcp_response

-- Convenience wrappers (use -32603 Internal Error)
api.mcp_resource_error(message text, request_id jsonb) RETURNS api.mcp_response
api.mcp_prompt_error(message text, request_id jsonb) RETURNS api.mcp_response
```

The JSON-RPC error codes and when each applies are specified on the
[protocol page](MCP-PROTOCOL.md#json-rpc-error-codes).

### Content Helpers

```sql
-- Text content block
api.mcp_text('Hello, world!')
-- Returns: {"type": "text", "text": "Hello, world!"}
```

## See Also

- [Author MCP handlers](MCP-HANDLERS.md) — the request/response types and registration metadata these functions consume
- [Protocol compliance & limitations](MCP-PROTOCOL.md) — versions, transport, error semantics
