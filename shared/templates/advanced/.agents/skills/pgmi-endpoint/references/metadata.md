# REST handler metadata and routes

`api.create_or_replace_rest_handler(metadata jsonb, body text)`. The metadata
keys are camelCase. The body is the inside of a PL/pgSQL function
(`DECLARE ... BEGIN ... END;`), dollar-quoted with `$body$`, whose one
parameter is `request` (`(request).content`, `(request).url`,
`(request).headers`, `(request).method`, `(request).path_params`). The
function comments in `lib/api/08-registration.sql` are the reference.

## Contents

1. Keys
2. Path templates
3. What the dispatcher answers before the body runs

## 1. Keys

| Key | Default | Notes |
|---|---|---|
| `id` | required | Stable uuid. The same id updates the handler in place, so a rename keeps it. |
| `name` | required | Becomes `api.<name>`; `^[a-zA-Z][a-zA-Z0-9_.-]{0,48}$`. |
| `path` | required | A template, section 2. |
| `httpMethod` | `^(GET\|POST\|PUT\|DELETE\|PATCH)$` | A POSIX regular expression, anchored with `^` and `$` or refused. |
| `access` / `permissions` | required, exactly one | `'access', 'public'`, `'access', 'authenticated'`, `'access', 'organization'`, or `'permissions', jsonb_build_array('task.read', ...)` (any one opens it). Neither, both, or an unknown permission fails the deploy, naming the handler. |
| `description` | none | One line: what it does, to which entity. Published in OpenAPI. |
| `outputSchema` | required on REST | JSON Schema of the success body. The OpenAPI test fails the deploy without it. |
| `inputSchema` | none | JSON Schema of the body; declare it on every write. `{}` is refused: give it a `type`. Checked before the body runs, at every depth and through `#/components/schemas/` references: `type`, `enum`, `const`, `required`, `properties`, `additionalProperties` `true` or `false`, one `items` schema, the bounds (`minimum`, `maximum`, the exclusive ones), the sizes (`minLength`, `maxLength`, `minItems`, `maxItems`), `uniqueItems` and the schema `false`; a violation answers 400 `pgmi.invalid_body`. `format` and `pattern` are not checked: the body checks them. Any other keyword (`oneOf`, `if`, `multipleOf`, ...) is refused at registration. |
| `querySchema` | none | The query parameters, as an object schema. Each property is `string`, `integer`, `number`, `boolean`, or an `array` of those, with `enum`, `minimum`, `maximum`, `format` (`uuid`, `date`, `date-time`); any other keyword is refused at registration. An empty value (`?q=`) counts as absent. List routes use `api.page_query_schema(properties)`. |
| `successStatus` | `200` | The 2xx published: `201` for a create, `204` for a delete with no body, an array such as `[201, 200]` when the route answers more than one. Registration refuses a body whose literal 2xx, in a response call (`api.json_response(201, ...)`) or a `CASE` branch (`THEN 201`, `ELSE 200`), is not declared; a status in a variable is not checked. |
| `locationTemplate` | none | Only with `successStatus` 201, and only where a GET reads the created resource: `'/projects/{projectId}'`. Each `{name}` is a whole segment and a required top-level property of `outputSchema`; the dispatcher (`api.rest_invoke`) fills it from the response body, percent-encoded, and answers it under the `api_base_path` mount (`/api/projects/...`) as the `Location` header; OpenAPI publishes the header. Registration refuses any other template. |
| `noLocation` | none | The reason a 201 names no `Location`, when no GET reads the created resource or the request URI already names it (a PUT that creates): `'no route reads one invitation'`. A non-empty string, only with `successStatus` 201 and never with `locationTemplate`. Registration refuses a 201 that declares neither. |
| `autoLog` | `true`; `false` on a `readOnly` route | Writes the exchange log: request and response bodies, in plain text. Set `'autoLog', false` on a route whose request or response carries a credential (a session or connection handle, a token, a password); the database keeps only a credential's hash. |
| `readOnly` | `false` | The HTTP gateway (`gateways/`) opens the transaction `READ ONLY`; a read-write one is refused 428 `pgmi.transaction_read_only_required`. The route is unlogged, since a read-only transaction cannot write the log; `'autoLog', true` with it is refused. |
| `minTransactionIsolation` | none | `read committed`, `repeatable read` or `serializable`. The HTTP gateway (`gateways/`) opens at least that; a shortfall is refused 428 `pgmi.transaction_isolation_too_weak`. A route that can hit `40001` must be safe to run twice (pgmi-handlers). |
| `accepts` | `application/json` with an `inputSchema`, else `*/*` | Request media types; any other `Content-Type` is 415 `pgmi.unsupported_media_type`. Published as the OpenAPI `requestBody` media type. |
| `produces` / `responseHeaders` | `application/json` / none | The response media types (an `Accept` outside them is 406 `pgmi.not_acceptable`) and fixed response headers. |

A schema written as a literal needs `::jsonb`:
`'inputSchema', '{"type": "object"}'::jsonb`. Without it `jsonb_build_object`
stores a JSON string, and registration fails with `json_schema_valid_shape`.

A schema used by several handlers is registered once,
`api.create_or_replace_schema_component('Task', schema)`, above the handlers
in the same file, and referenced as
`jsonb_build_object('$ref', '#/components/schemas/Task')`. A `$ref` naming no
component is refused; a component no handler uses fails the OpenAPI test.

`title` names the handler for people. Any other key not listed above is
refused with a hint: `readonly` gets "did you
mean "readOnly"?", and the retired REST keys `uri`, `version`, `example` and
`query` keep their own hints. Version in the path: `/v2/projects`.

## 2. Path templates

```
'path', '/projects/{projectId:uuid}/tasks/{taskNo:int}'
```

- A parameter takes a whole segment: `{name}` (text), `{name:uuid}`,
  `{name:int}`. `/files/{name}.json` is refused.
- The handler reads the values by name, percent-decoded:
  `((request).path_params->>'projectId')::uuid`. The raw cast is safe here
  and only here: the dispatcher checked the type.
- Write the path as clients see it: no `^`/`$`, no trailing `/`, no query.
  The dispatcher canonicalizes every request first (trailing and duplicate
  slashes, dot-segments, percent-encoding), so every spelling reaches the
  route.
- A literal segment outranks a parameter: `/projects/stats` and
  `/projects/{id:uuid}` coexist in any order. Two routes of one shape with a
  common method, or a parameter name used twice, fail the deploy.
- OPTIONS and 405 list every method of a path, to any caller.

## 3. What the dispatcher answers before the body runs

In this order, each with an RFC 9457 problem body:

| Status | Code | When |
|---|---|---|
| 404 / 405 | `pgmi.not_found` / `pgmi.method_not_allowed` | no route for the path / no route for the method |
| 401 | | not `public` and no signed-in person |
| 403 | `pgmi.organization_required`, `pgmi.forbidden` | an `organization` or `permissions` handler without an organization session, or without the permission |
| 400 | `pgmi.invalid_path_parameter` | a malformed `{:uuid}` or `{:int}` value |
| 400 | `pgmi.invalid_query` + `invalid-params` | a query string that breaks `querySchema` |
| 428 | `pgmi.transaction_*` | the transaction does not meet `readOnly` or `minTransactionIsolation` |
| 415 / 406 | `pgmi.unsupported_media_type` / `pgmi.not_acceptable` | a content type outside `accepts`, an `Accept` outside `produces` |
| 400 | `pgmi.malformed_body`, `pgmi.invalid_body` + `invalid-params` | a body that is not JSON, or that breaks `inputSchema` at any depth (type, enum, const, required, bounds, sizes) |

Everything inside the values (formats, ranges, references, state) is the
body's, which answers 404, 409 or 422 (pgmi-handlers).
