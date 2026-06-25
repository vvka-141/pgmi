# pgmi API Client Guidance

> This is the **application** API (deployed handlers served by the gateway).
> For the **session** API (temp views/functions used inside deploy.sql), see `pgmi ai contract`.

## Decision Tree

| Consumer | Approach |
|----------|----------|
| Browser / SPA | Fetch the OpenAPI spec at `GET /openapi.json`, generate a typed client with `openapi-typescript` or your framework's codegen |
| Backend service | Direct PostgreSQL connection (best perf) OR HTTP client against the REST/RPC surface |
| AI agent | Connect via MCP (`tools/list` is self-describing) |
| Third-party / external | Generate a client from `/openapi.json` using any OpenAPI codegen tool |

## Invariants Every Client Needs

### Authentication

```
Authorization: Bearer <token>
```

Handlers with `requires_auth = true` reject unauthenticated requests. The token is validated by the gateway and mapped to an `auth.idp_subject` session variable.

### Response Envelope

Successful responses return JSON. The shape depends on the handler's `output_json_schema`.

### Error Shape (RFC 9457)

All errors follow the Problem Details format:

```json
{
  "type": "about:blank",
  "title": "Not Found",
  "status": 404,
  "detail": "handler 'foo' not found"
}
```

### Pagination

Paginated endpoints accept `?limit=N&offset=M` query parameters. Defaults and bounds are handler-defined.

## Live Contract

The deployment serves its own OpenAPI 3.1 spec at:

```
GET /openapi.json
```

This is the **single source of truth** for available endpoints, request/response schemas, and auth requirements. Always fetch it from the running deployment rather than hardcoding endpoints.

## Language-Specific Guidance

Run `pgmi ai client <lang>` for a transport-core skeleton and recommended generator:

```bash
pgmi ai client typescript
pgmi ai client python
pgmi ai client go
pgmi ai client csharp
pgmi ai client rust
```

## Anti-Copy Directive

**DO NOT copy the skeleton verbatim.** The transport core below is a starting pattern for auth + error handling. Endpoint methods MUST be generated from the live `/openapi.json` spec, specialized to the user's project, HTTP library, and conventions. Hand-rolling endpoint methods from training data produces stale, incorrect clients.
