---
title: "MCP protocol compliance & limitations"
description: "Supported MCP protocol versions, transport behavior, error semantics, and what the advanced template deliberately does not implement."
weight: 70
---

# MCP Protocol Compliance and Limitations

> **Scope: advanced template only.** This page specifies how the scaffolded
> MCP implementation behaves at the protocol level. New to this subsystem?
> Start with the [overview](MCP.md).

## Protocol Versions

pgmi implements MCP using JSON-RPC 2.0:

- **Supported protocol versions**: `2024-11-05`, `2025-03-26`, `2025-06-18`, `2025-11-25` (an unknown-newer proposal negotiates down to the server's best supported version)
- **Transport**: HTTP POST to `/mcp` endpoint
- **Authentication**: Context parameter (not HTTP headers)

## Transport Behavior

The gateway implements the Streamable HTTP transport's request/response path:

- A POST whose `MCP-Protocol-Version` header names an unsupported revision is
  rejected with `400`; the negotiated version is echoed on every response.
- An `Accept` header that admits neither `application/json` nor a wildcard is
  rejected with `406`.
- A server→client SSE stream on `GET /mcp` is intentionally not implemented —
  the gateway emits no server-initiated notifications, and `GET /mcp` answers
  `405 Method Not Allowed`.

## Notifications

Any method in the `notifications/*` family — and any request missing an `id`
member — returns a NULL envelope from the SQL dispatcher, and the gateway
answers `202` with no body. Callers MUST NOT write anything to the wire for
notifications per JSON-RPC 2.0.

## Error Semantics

Tool *execution* failures return `result.isError = true` per the MCP spec —
the JSON-RPC `error` envelope is reserved for protocol-level failures
(unknown method, invalid params, auth). Resource and prompt failures stay
protocol errors (`-32603`).

An unknown tool/resource/prompt *name* returns `-32602` (Invalid params): the
method itself was found and dispatched correctly — only the name identifies
nothing. `-32601` (Method not found) is reserved for genuinely-unknown
JSON-RPC methods.

### JSON-RPC Error Codes

| Code | Meaning |
|------|---------|
| -32700 | Parse error (invalid JSON) |
| -32600 | Invalid Request (missing jsonrpc, method; transaction-policy shortfalls carry `data.code`) |
| -32601 | Method not found |
| -32602 | Invalid params (including unknown tool/resource/prompt names) |
| -32603 | Internal error |
| -32001 | Authentication required (custom) |

### Example Error Response

```json
{
  "jsonrpc": "2.0",
  "id": "req-1",
  "error": {
    "code": -32602,
    "message": "Tool not found: unknown_tool"
  }
}
```

## Known Limitations

- **No pagination**: `mcp_list_*` return the full list in one call. Clients
  MUST NOT rely on cursor behaviour. Keyset pagination is planned post-v1.
- **No `listChanged` notifications**: pgmi does not emit
  `notifications/{tools,resources,prompts}/list_changed` when the registry
  mutates. `mcp_server_capabilities` stays silent on `listChanged` in
  lockstep. The LISTEN/NOTIFY integration path is sketched at
  `lib/api/10-mcp-protocol.sql:85-90` (the capability declaration itself) and
  `lib/api/09-gateways.sql:1107-1112` (the discovery functions).
- **No server-initiated SSE stream**: see transport behavior above.

## See Also

- [Run the MCP gateway](MCP-GATEWAY.md) — the transport that implements this behavior
- [MCP SQL API reference](MCP-SQL-API.md) — dispatcher and response builders
