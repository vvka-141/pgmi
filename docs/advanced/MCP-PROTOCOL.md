---
title: "MCP protocol compliance & limitations"
description: "The two MCP eras the advanced template serves, transport behavior, error semantics, and what it deliberately does not implement."
weight: 70
---

# MCP Protocol Compliance and Limitations

> **Scope: advanced template only.** This page specifies how the scaffolded
> MCP implementation behaves at the protocol level. New to this subsystem?
> Start with the [overview](MCP.md).

## Two eras on one endpoint

The dispatcher (`api.mcp_handle_request`) serves both MCP eras. It picks the
era per request, from the request itself, and keeps no connection state:

| A request that | Is served as |
|---|---|
| names `io.modelcontextprotocol/protocolVersion` in `params._meta`, or is `server/discover` | **MCP 2026-07-28**: stateless, no handshake |
| does neither | **Legacy**: `initialize` negotiates `2024-11-05` up to `2025-11-25` |

Legacy clients keep working because the mainstream ones still open with
`initialize`. Removing the legacy era is tracked separately; nothing in a
2026-07-28 request depends on it.

### MCP 2026-07-28

Ask `server/discover` first. A bare probe without `_meta` is answered too:

```bash
curl -s http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: server/discover' \
  -d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientCapabilities":{}}}}'
```

```json
{"jsonrpc": "2.0", "id": 1, "result": {
  "resultType": "complete",
  "supportedVersions": ["2026-07-28"],
  "capabilities": {"tools": {}, "resources": {}, "prompts": {}},
  "ttlMs": 300000, "cacheScope": "public",
  "_meta": {"io.modelcontextprotocol/serverInfo": {"name": "mydb", "version": "1.0.0"}}}}
```

Then every request names its version and the client's capabilities in
`params._meta`:

```bash
curl -s http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' -H 'Mcp-Name: list_tasks' \
  -H 'X-Connection: cn_...' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_tasks","arguments":{},
        "_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",
                 "io.modelcontextprotocol/clientCapabilities":{}}}}'
```

| Rule | Answer |
|---|---|
| `_meta` names a version other than `2026-07-28` (or a non-string) | `-32022`, `data: {"supported": ["2026-07-28"], "requested": <as sent>}` |
| `_meta` lacks `io.modelcontextprotocol/clientCapabilities` (not `server/discover`) | `-32602` |
| `initialize`, `ping`, `logging/setLevel`, `subscriptions/listen` | `-32601` |

Server instructions come from the hook `internal.mcp_server_instructions()`,
which returns NULL (no `instructions` member). Replace it in a file sorted
after `lib/` to publish them; legacy `initialize` publishes the same text.

### Legacy (initialize)

```bash
curl -s http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}'
```

An unknown `protocolVersion` is answered with `2025-11-25`, the newest legacy
version, and the client decides whether to continue. `ping` answers `{}`.
Every other method works as in the 2026-07-28 era.

## Results

- Every success result carries `resultType: "complete"` and names the server
  in `_meta["io.modelcontextprotocol/serverInfo"]` (`SET mcp.server_name` /
  `mcp.server_version` to override the database name and `1.0.0`). A
  handler's own `_meta` keys are kept beside it.
- **pgmi tools answer in one round trip.** The request is the transaction
  boundary, so a tool cannot pause for more input and resume in the same
  transaction: pgmi never returns `input_required`.
- `tools/list`, `resources/list`, `resources/templates/list`, `prompts/list`
  and `resources/read` carry `ttlMs: 300000` and `cacheScope: "private"`: the
  lists are filtered by the caller's access, so a shared cache must not hand
  one caller's list to another (`api.mcp_cache_policy()`). `server/discover`
  is the same for every caller and is `public`. Tool call and prompt results
  carry no cache members.
- Lists are ordered bytewise: tools and prompts by name, resources by URI.
- Nulls inside `structuredContent` and `error.data` are kept; only absent
  members are left out.

## Transport Behavior

Every reference gateway in `gateways/` implements Streamable HTTP with JSON
answers only: no SSE stream, no session, nothing server-initiated. What a
gateway refuses on its own, before the body is read, is a problem
(`application/problem+json`) with a `code`:

| Request | Answer |
|---|---|
| any method but POST on `/mcp` | `405` `pgmi.method_not_allowed`, `Allow: POST` |
| `Content-Type` other than `application/json` | `415` `pgmi.unsupported_media_type` |
| an `Accept` that admits neither `application/json` nor a wildcard | `406` `pgmi.not_acceptable` |
| an `Origin` not on `ALLOWED_ORIGINS` | `403` `pgmi.origin_refused` |
| over a rate limit (`RATE_LIMIT_PER_IP`, `RATE_LIMIT_PER_CLIENT`) | `429` `pgmi.rate_limited`, `Retry-After` |
| with `MCP_RESOURCE` set: no token and no connection handle, or a token that fails verification | `401`, `WWW-Authenticate: Bearer resource_metadata="..."`, no body |
| a connection handle (`Authorization: Bearer cn_...` or `X-Connection`) the database knows no live connection by: unknown, revoked or expired | `401`, `WWW-Authenticate: Bearer error="invalid_token"` (with `resource_metadata` when `MCP_RESOURCE` is set), no body |
| a body that is not UTF-8 JSON | `400`, `-32700`, `id: null` |
| no pooled connection within 5 s | `503`, `Retry-After: 1`, `-31000` with `data.code` `pgmi.unavailable` |
| with `MCP_RESOURCE` set: the identity provider's keys cannot be read | `503`, `Retry-After`, `-31000` with `data.code` `pgmi.identity_provider_unavailable` |

Every other rule is the database's. Before dispatch the gateway calls
`api.mcp_request_policy(request, requested_isolation, protocol_version,
mcp_method, mcp_name)` with the three headers as sent (NULL when absent). Its
`refusal`, when not NULL, is the whole answer: the request is never
dispatched and logs no exchange.

| Request | Refusal |
|---|---|
| `MCP-Protocol-Version` names neither a legacy version nor 2026-07-28 or later | `-32022`, `data: {"supported": <every legacy version and 2026-07-28>, "requested": <the header>}` |
| a body whose `_meta` names a version, under a missing or legacy header | `-32020`, `data: {"header": "MCP-Protocol-Version"}` |
| 2026-07-28 or later: `Mcp-Method` is not `method` | `-32020`, `data: {"header": "Mcp-Method"}` |
| 2026-07-28 or later: `Mcp-Name` is not `params.name` (`tools/call`, `prompts/get`) or `params.uri` (`resources/read`); a `=?base64?...?=` value is decoded first | `-32020`, `data: {"header": "Mcp-Name"}` |
| 2026-07-28 or later: the header is not the `_meta` version (on a notification, only when it names one) | `-32020`, `data: {"header": "MCP-Protocol-Version"}` |
| then the `_meta` rules of the 2026-07-28 era above | `-32022`, `-32602` |

Every refusal carries the request's id and is answered with `400`. A
request with no `_meta` version under no or a legacy header is a legacy
client's and is not refused here. A dispatched answer takes its status from
the policy row's `modern` (its era) and the code:

| Answer | 2026-07-28 era | legacy era |
|---|---|---|
| a `refusal` from `api.mcp_request_policy` (`-32020`, `-32022`, `-32602`) | `400` | `400` |
| `-32601` from `api.mcp_handle_request` | `404` | `200` |
| `-32022` from `api.mcp_handle_request` | `400` | `200` |
| any other answer, a `-32602` for an unknown tool or resource included | `200` | `200` |
| a notification | `202`, no body | `202`, no body |

`Mcp-Session-Id` and `Last-Event-ID` are ignored.

A serialization failure or deadlock is retried as a whole new transaction.
When the retries run out, the answer is `200` with

```json
{"jsonrpc": "2.0", "id": 7, "error": {"code": -31000,
  "message": "The call conflicted with a concurrent change and was rolled back; repeat it unchanged",
  "data": {"code": "pgmi.transaction_retryable", "sqlstate": "40001", "attempts": 3}}}
```

and a `Retry-After` header: nothing was applied, and the same call may
succeed when repeated.

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

The dispatcher checks what a handler returns. The response id is always the
request's. A NULL envelope, or a `tools/call` result without a `content`
array, becomes an `isError` result; a resource or prompt envelope that is
neither a result nor an error becomes `-32603`.

### JSON-RPC Error Codes

| Code | Meaning |
|------|---------|
| -32700 | Parse error (invalid JSON) |
| -32600 | Invalid Request (missing jsonrpc, method, a malformed id) |
| -32601 | Method not found |
| -32602 | Invalid params, including unknown names and protected names asked for without a resolved identity |
| -32603 | Internal error; also an interceptor that failed (`data.code: pgmi.interceptor_failed`, HTTP 502) and a transaction weaker than the handler's isolation floor (`data.code: pgmi.transaction_isolation_too_weak`, with `requiredIsolation`) |
| -32022 | Unsupported protocol version, `data: {supported, requested}` (`api.mcp_request_policy`; HTTP 400) |
| -32020 | Header mismatch, `data.header` names the header (`api.mcp_request_policy`; HTTP 400) |
| -31000 | Retries exhausted after serialization failures (`data.code: pgmi.transaction_retryable`), or no database connection (`data.code: pgmi.unavailable`, HTTP 503), or the identity provider unreachable (`data.code: pgmi.identity_provider_unavailable`, HTTP 503) |
| -31001 | The client cannot act; `data.code` is `membership.connection.pending` (with `data.pairingCode` and `data.expiresAt`), `.paused`, `.disabled` or `.not_admitted` (HTTP 200), or `pgmi.connection_unknown`, which the gateway answers as HTTP 401 |
| -31003 | No permission, or no organization (`data.code: pgmi.forbidden`, `pgmi.organization_required`) |

pgmi's own codes (`-31000`, `-31001`, `-31003`) sit outside the JSON-RPC
reserved range `-32768`..`-32000`, as MCP 2026-07-28 asks of new codes.
Branch on `data.code`.

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

- **No pagination**: `mcp_list_*` return the full list in one call, with no
  `nextCursor`.
- **No `listChanged`**: there is no `subscriptions/listen` stream. A client
  lists again once a list's `ttlMs` has passed.
- **No server-initiated SSE stream** and no `input_required`: see above.
- **No authorization server.** With `MCP_RESOURCE` set, a gateway is an
  OAuth 2.1 resource server: it serves protected-resource metadata
  (RFC 9728) and verifies access tokens from your identity provider. The
  provider issues the tokens, and it must put `MCP_RESOURCE` in their
  audience (RFC 8707); one that cannot is not supported.

## See Also

- [Run the MCP gateway](MCP-GATEWAY.md) — the transport that implements this behavior
- [MCP SQL API reference](MCP-SQL-API.md) — dispatcher and response builders
