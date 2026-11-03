---
name: pgmi-handlers
description: Writes and reviews REST handler bodies in this advanced-template project — the four phases (materialize, validate, probe, execute), safe casts with ?> and common.try_cast, api.malformed_params, status codes and problem codes (400, 404, 409, 422), the handler-to-kernel contract, PATCH and PUT semantics, optimistic concurrency and retry-safe routes. Use when writing or changing the body of api.create_or_replace_rest_handler (an RPC body follows the same phases), when an endpoint answers the wrong status or a 500, or when reviewing api/*.sql.
---

# Handler bodies

A handler turns one request into one answer. It **returns** an
`api.http_response` for every outcome and never raises to say "not found" or
"invalid". The gateway decides access and checks the declared shapes before
the body runs; the body decides everything that needs the data.

Neighbouring skills: pgmi-endpoint (the files, registration metadata and the
gateway's own answers), pgmi-layers (kernels, views and the `*_problem`
functions a body calls), pgmi-api (protocols, identity), pgmi-mcp (MCP tool
bodies).

## The four phases

| Phase | Does | Answers |
|---|---|---|
| Materialize | read every input into a typed local, with safe casts | nothing |
| Validate | required fields, formats, enums, business rules on the input | 422 |
| Probe | ask the entity's `*_problem` / `*_problems` functions | 404, 409, 422 |
| Execute | call the kernel, format its row | 200, 201, 204 |

Validation of the values is cheap and runs before any query. The probe is
one call: `core.fn_<entity>_problem(id, operation)` answers a code
(`core.task.not_found` first, then the state rules) and
`core.fn_<entity>_problems(...)` every `(field, reason)` at once. The kernel
reads the same functions, so the rule exists once (pgmi-layers). A chain of
`IF NOT EXISTS (SELECT ... FROM core.<table>)` in a handler is that function
not written yet. [Worked bodies](references/examples.md) show create, PATCH
and delete.

## Rules

- **Never a raw cast on client input.** `::uuid`, `::int`, `::date` raise on
  bad text and turn a 422 into a 500. Use `v_b->>'dueOn' ?> NULL::date` or
  `common.try_cast(v_b->>'dueOn', NULL::date)`; both give the default on bad
  input (uuid, boolean, integer, bigint, numeric, interval, timestamp,
  timestamptz, date, inet). The one safe raw cast is a typed path parameter
  (`{id:uuid}`), which the gateway checked.
- **Tell absent from malformed.** `?>` answers NULL for both.
  `api.malformed_params(v_b, '{"dueOn": "date", "ownerId": "uuid"}')` names
  every present field that does not read as its type, ready for
  `invalid_params =>`; NULL when all read.
- **Validate an enum as text before casting** to the enum type.
- **No `EXCEPTION` block in a body.** It costs a subtransaction per request,
  maps a missing reference to the wrong status and hides bugs. An error you
  did not foresee reaches the gateway, which answers a sanitized 500 (or the
  coded status in [problem codes](references/problem-codes.md)) and logs the
  SQLSTATE; the client never sees `SQLERRM`.
- **Never put `SQLERRM` or a database message in an answer.**
- **Handlers call kernels; kernels write.** A handler never runs `INSERT`,
  `UPDATE` or `DELETE` on a table. A kernel returns the whole row
  (`RETURNS core.task`), never just the id, so the handler can answer from
  one snapshot.
- **Read each request value once** into a local and hand rows on, never the
  id to look up again; a check with constant arguments in a query is
  `(SELECT fn(...))`.
- **Read identity through the typed readers**: `api.current_principal_id()`,
  `api.current_organization_id()`, `api.has_permission('task.write')`. A
  deploy test fails on a handler that reads an `auth.*` setting or a session
  header by name.
- **Access is declared, not checked in the body.** The body checks only
  object-level rules (someone else's record), with a coded 403.

## Status codes

| Answer | When |
|---|---|
| 200 | a read, an update, an action that returns the row |
| 201 | a create; declare `'successStatus', 201` |
| 204 | a delete with nothing to say; declare `'successStatus', 204` |
| 404 | the resource in the URL does not exist, or row-level security hides it |
| 409 + `code` | the state forbids it: a closed task, a row still referenced, an existing link, a row that changed since it was read |
| 422 + `invalid-params` | a body field is missing, malformed or breaks a rule; a value already taken (reason `duplicate`); a referenced id names nothing (reason `not_found`) |
| 403 + `code` | an object-level rule only; the permission check is the gateway's |

- **400 is the gateway's**, never the body's: a malformed typed path
  parameter, a `querySchema` violation, a body that is not JSON or breaks the
  top level of `inputSchema`.
- **404 for the thing acted on, 422 for a thing pointed to.** `DELETE
  /tasks/{id}` with an unknown id is 404; `POST /tasks` with an unknown
  `projectId` is 422.
- **404, never 403, for a row another organization owns**: 403 would confirm
  that it exists.
- **A value the user typed that clashes is 422**, a field they can fix, not
  409. 409 is for a state the rule forbids.
- **A delete of a row others still reference** probes `*_problem` and
  answers 409 `<schema>.<entity>.in_use` (or a sharper code). Left to the foreign key,
  the gateway's floor answers a generic `pgmi.resource_in_use`: a missing
  probe.
- **Every 409 and 422 carries `code =>` or `invalid_params =>`**; a deploy
  test names any that does not. A code is a literal at the call, a stable
  dotted word (`core.task.closed`, [problem codes](references/problem-codes.md));
  one `CASE` arm per code a problem function returns. `pgmi.` codes are the framework's. Human text belongs to the
  client's translations, keyed by code and reason.

## Methods

- **GET reads**: it calls no kernel that writes.
- **A state transition is a POST to a verb**: `POST /tasks/{id:uuid}/close`,
  not a PUT of a status field.
- **Work that must survive a later failure** (an e-mail, a call to another
  system) is a row in an outbox table that a job drains after commit, never
  a call from the handler.

## Updates and races

- **PATCH** changes the fields the body names, to `null` if it says so: test
  presence with `v_b ? 'note'`, never `IS NULL`, and never
  `COALESCE(p_note, note)` in the kernel, which cannot clear a field.
- **PUT** replaces every editable field; a missing optional field clears it.
  Pick one per resource.
- **A probe is not a lock.** Two requests can both pass it. The kernel guards
  the write in its own `WHERE` (`AND status = 'open'`, `AND updated_at =
  p_expected`) and returns NULL when it lost; the handler answers 409.
- **A route that can hit `40001` or `40P01` runs twice.** The client gateway
  retries the whole transaction; keep side effects inside it (an outbox
  row), and never retry inside PL/pgSQL.

[Concurrency](references/concurrency.md) has the kernel and handler for each.

## Testing a handler

Call it through `api.rest_invoke(method, url, headers, body)` in a test under
`api/__test__/`, with headers from
`pg_temp.test_session_headers(current_setting('test.alice_subject'), current_setting('test.acme_org_id')::uuid)`.
Assert with `IS DISTINCT FROM`: `<>` against a missing key is NULL and never
fails. Cover the success, each refusal the body can return, malformed input,
and that no problem `detail` matches `PL/pgSQL|SQLSTATE|CONTEXT`.

## Review checklist

Severity when reviewing: a wrong answer a client acts on (a 500 from client
input, a 403 that confirms a hidden row, a GET that writes, internals in a
body) is critical; a contract defect (200 for a create, an unbounded list,
an uncoded 409) is major; naming and shape drift between sibling endpoints
is minor.

- [ ] every input read once into a typed local, with `?>` or `try_cast`
- [ ] present-but-malformed fields refused with `api.malformed_params`
- [ ] probes through the entity's `*_problem` / `*_problems` functions, which the kernel also reads
- [ ] no `EXCEPTION`, no `SQLERRM`, no raw cast on client input
- [ ] writes go through a kernel that returns the row; no DML on a table
- [ ] status chosen by the table above; every 409/422 coded with a literal
- [ ] a delete of a referenced row answers a coded 409 from the probe
- [ ] PATCH tests presence; a racing write is guarded in the kernel's `WHERE`
- [ ] every list paged (`api.page_query_schema`, `api.page_response`)
- [ ] tests through `api.rest_invoke` for success, each refusal and malformed input
