# MCP protocol and builders

`lib/api/10-mcp-protocol.sql` is the dispatcher, `lib/api/07-helpers.sql`
the builders; their function comments are the reference. Before relying on a
detail of the MCP specification itself, read the current version of it: the
protocol moves.

## Contents

1. Two eras on one endpoint
2. Method routing
3. Envelopes
4. Builders
5. Discovery
6. From psql

## 1. Two eras on one endpoint

`api.mcp_handle_request(request jsonb, context jsonb)` picks the era per
request and keeps no connection state:

| Request | Era |
|---|---|
| `params._meta` names `io.modelcontextprotocol/protocolVersion`, or method `server/discover` | 2026-07-28, stateless |
| anything else | legacy: `initialize` negotiates `2024-11-05` to `2025-11-25` |

On 2026-07-28: a version other than `2026-07-28` answers `-32022` with
`data {supported, requested}`; a request without
`io.modelcontextprotocol/clientCapabilities` answers `-32602`
(`server/discover` may omit `_meta`); `initialize`, `ping`,
`logging/setLevel` and `subscriptions/listen` answer `-32601`.

Over HTTP the gateway first calls `api.mcp_request_policy` with the
`MCP-Protocol-Version`, `Mcp-Method` and `Mcp-Name` headers. It returns the
same `-32022` and `-32602` refusals, `-32020` with `data.header` when a header
disagrees with the body, and the era (`modern`). A refusal is answered
with 400 and never dispatched; a dispatched answer's status follows
`gateways/PROTOCOL.md`, where a tool's `-32602` stays 200.

Every success carries `resultType: "complete"` and
`_meta["io.modelcontextprotocol/serverInfo"]`. A tool answers in one round
trip: the request is the transaction. Server instructions come from
`internal.mcp_server_instructions()`, NULL by default; replace it in a file
sorted after `lib/`.

## 2. Method routing

| Method | Answered by |
|---|---|
| `server/discover` | `api.mcp_discover()` |
| `initialize`, `ping` | legacy era only |
| `notifications/*`, any request without `id` | nothing is sent |
| `tools/list`, `tools/call` | `api.mcp_list_tools()`, `api.mcp_call_tool(name, args, context, id)` |
| `resources/list`, `resources/templates/list`, `resources/read` | `api.mcp_list_resources()`, `api.mcp_list_resource_templates()`, `api.mcp_read_resource(uri, context, id)` |
| `prompts/list`, `prompts/get` | `api.mcp_list_prompts()`, `api.mcp_get_prompt(name, args, context, id)` |
| anything else | `-32601` |

## 3. Envelopes

`api.mcp_response` has one field, `envelope`: `{jsonrpc, id, result}` or
`{jsonrpc, id, error: {code, message, data?}}`. The dispatcher checks every
handler's envelope: the id becomes the request's; a NULL envelope, or a tool
result without a `content` array, becomes an `isError` result; a resource or
prompt envelope that is neither result nor error becomes `-32603`. An
uncaught error becomes a sanitized error; `40001` and `40P01` propagate so
the client gateway retries the whole transaction.

## 4. Builders

```sql
api.mcp_text(text) -> jsonb                                  -- {"type": "text", "text": ...}
api.mcp_tool_result(content jsonb, request_id jsonb,
                    is_error boolean DEFAULT false,
                    structured_content jsonb DEFAULT NULL)    -- a tool's answer, success or refusal
api.mcp_tool_error(message text, request_id jsonb)           -- isError result with one text item
api.mcp_resource_result(contents jsonb, request_id jsonb)    -- resources/read; adds ttlMs and cacheScope
api.mcp_prompt_result(messages jsonb, request_id jsonb)      -- prompts/get
api.mcp_resource_not_found(uri text, request_id jsonb)      -- -32602 "Resource not found", data.uri
api.mcp_resource_error(message, request_id), api.mcp_prompt_error(message, request_id)  -- -32603
api.mcp_error(code integer, message text, request_id jsonb, data jsonb DEFAULT NULL)    -- protocol errors only
```

A resource content item is `{uri, mimeType, text}`; echo `(request).uri`.
A resource that does not exist answers `api.mcp_resource_not_found`; an empty
`contents` array is turned into the same `-32602`.

A tool success with an `outputSchema` is checked before it leaves: a missing
`structuredContent`, a missing required key or a top-level property of the
wrong type becomes the sanitized `Tool execution failed` result, and the
exchange log keeps why. Refusals (`isError`) are not checked.
A prompt message is `{role, content: {type: "text", text}}`.

## 5. Discovery

- The lists show only what `internal.current_access_denial` lets the caller
  call, the same decision a call makes, ordered bytewise by name or URI, and
  carry `api.mcp_cache_policy()` (`cacheScope` private: lists differ per
  caller).
- `api.mcp_list_tools(tags)` filters by tag overlap; tags appear under
  `_meta.tags`. `tools/list` publishes `annotations` and `_meta["pgmi/effect"]`.
- A static resource is listed with `uri`, a templated one (RFC 6570) with
  `uriTemplate`.

## 6. From psql

```sql
SELECT (api.mcp_handle_request('{"jsonrpc": "2.0", "id": 1, "method": "server/discover"}'::jsonb)).envelope;
SELECT (api.mcp_handle_request('{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'::jsonb)).envelope;
```

Outside a request no one is signed in, so the list shows only `public`
handlers.
