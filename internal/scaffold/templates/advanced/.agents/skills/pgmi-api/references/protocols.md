# REST, RPC and MCP

All three are HTTP. They share transport, the handler registry, identity, the
access decision and the transaction; they differ in how a request finds its
handler and in the body around the answer.

## Contents

1. Comparison
2. Entry points
3. Status and error codes
4. An RPC handler

## 1. Comparison

Choose REST for resources a client reads and changes, RPC for a named action
that changes several rows in one step (`organization.transfer_ownership` in
`api/ownership.sql`), and MCP for what an AI agent calls. The Go gateway
routes RPC at `POST /rpc`.

| | REST | RPC | MCP |
|---|---|---|---|
| Routing | path template + method | method name | tool or prompt name, resource URI template |
| Body | any content type, RFC 9457 problems | JSON-RPC 2.0 | JSON-RPC 2.0, MCP results |
| Handler returns | `api.http_response` | `api.http_response` | `api.mcp_response` |
| Identity from | session headers | session headers | the `client` context key (a verified OAuth token) or `connection` (a handle) |
| Not signed in | 401 | 401, `-32001` | `-32602`, the same as an unknown name; HTTP 401 from the gateway when OAuth is on (`MCP_RESOURCE`) |
| Unknown, revoked or expired handle | 401 | 401, `-32001` | HTTP 401 + `WWW-Authenticate` from the gateway (the database answers `-31001` `pgmi.connection_unknown`) |
| Known but cannot act yet | | | `-31001` + `data.code` `membership.connection.<reason>` (`pending` carries the pairing code), HTTP 200 |
| No permission | 403 + `code` | 403, `-32003` + `data.code` | `-31003` + `data.code` |

## 2. Entry points

The gateway calls these and nothing else; each decides access before any
handler runs:

```sql
api.rest_invoke(method, url, headers extensions.hstore, content) -- -> api.http_response
api.rpc_invoke(api.rpc_resolve(method_name), headers, content)   -- -> api.http_response
api.mcp_handle_request(request jsonb, context jsonb)             -- -> api.mcp_response
api.rest_route_policy / api.rpc_route_policy / api.mcp_request_policy  -- transaction policy, before BEGIN (MCP: also the header refusal)
```

Tests call `api.rest_invoke` and, for MCP, `api.mcp_call_tool(name,
arguments, context, request_id)` directly. The wire rules a gateway keeps
(headers, retries, transaction policy) are `gateways/PROTOCOL.md`.

## 3. Status and error codes

REST and RPC answers carry the HTTP status the outcome deserves: scanners,
proxies and monitoring read it. `api.jsonrpc_error` maps the JSON-RPC code:

| JSON-RPC code | Meaning | HTTP |
|---|---|---|
| -32700 | parse error | 400 |
| -32600 | invalid request | 400 |
| -32601 | method not found | 404 |
| -32602 | invalid params | 400 |
| -32603 | internal error | 500 |
| -32001 | authentication required | 401 |
| -32010 | the state refuses (a handler body); `data.code` | 409 |
| -32011 | the input is invalid (a handler body); `data.invalid-params` | 422 |

MCP follows JSON-RPC inside one HTTP exchange: the error is in the envelope.
An unknown tool, prompt or resource, and a protected one asked for
anonymously, both answer `-32602` "not found (or it requires
authentication)", so an anonymous caller cannot learn what exists. A tool
that ran and failed answers a result with `isError: true`, not a JSON-RPC
error (pgmi-mcp). pgmi's own MCP codes sit outside the reserved range
`-32768`..`-32000`: `-31001` the client cannot act (yet), `-31003` access
refused, `-31000` try again later; a transaction below the isolation floor
is `-32603`. The table is in `gateways/PROTOCOL.md` ("MCP error codes").

## 4. An RPC handler

```sql
SELECT api.create_or_replace_rpc_handler(
    jsonb_build_object(
        'id', '<uuid>',
        'methodName', 'tasks.reassign',
        'description', 'Move every open task of one person to another',
        'permissions', jsonb_build_array('task.write')),
    $body$
DECLARE
    v_params jsonb := api.content_json((request).content)->'params';
    v_id     jsonb := api.content_json((request).content)->'id';
BEGIN
    -- materialize, validate, probe, execute, as in a REST body
    RETURN api.jsonrpc_success(jsonb_build_object('moved', 3), v_id);
END;
    $body$);
```

The body answers a refusal with `api.jsonrpc_error(code, message, id, data)`,
the REST problem's fields in `data`; the four phases of pgmi-handlers apply
unchanged:

```sql
RETURN api.jsonrpc_error(-32010, 'The task is closed', v_id,
                         jsonb_build_object('code', 'core.task.closed'));
RETURN api.jsonrpc_error(-32011, 'Invalid params', v_id,
                         jsonb_build_object('invalid-params', jsonb_build_array(
                             jsonb_build_object('name', 'assignee', 'reason', 'not_found'))));
```

A body runs only for a request with an `id` member, `null` included. A
notification, a body with no `id`, is answered `202` with no body and never
reaches the handler: an RPC method is a business action, and a
notification's outcome would be invisible.
