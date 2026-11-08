---
name: pgmi-mcp
description: Designs and writes MCP tools, resources and prompts in PostgreSQL for this advanced-template project — task-level tools instead of table mirrors, one call and one snapshot per task, detail levels and paging, schemas and descriptions written for a model, answers as a briefing (hints, next actions, omitted), refusals with rule and remedy that never raise, untrusted stored text kept apart from guidance, write tools that take an array all or nothing, identity through approved connections, and testing with api.mcp_call_tool. Use when adding or reviewing an MCP handler, designing an API surface agents will call, or when a tool call returns an error or isError.
---

# MCP tools an agent can use well

An MCP tool is a handler whose caller is a language model. A model pays for
every call in latency and every byte in tokens, cannot see a screen, reads
every word of the answer as a possible instruction, and acts for a person who
stays accountable. So a good tool is **one task, one call, one snapshot**,
answered as data the model can act on, with the next step named and stored
text kept apart from the server's own words.

Neighbouring skills: pgmi-layers, then pgmi-api-layer (the kernels, views and
problem functions a tool calls), pgmi-handlers (the REST side of the same operations), pgmi-api
(protocols, identity). The wire rules of the MCP endpoint are
`gateways/PROTOCOL.md`; every gateway in `gateways/` serves `/mcp`.

## Registering

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', '<uuid>',
        'type', 'tool',                  -- tool | resource | prompt
        'effect', 'read',                -- tools only: read; write with 'reversible'; proposal with 'proposes'
        'name', 'review_project_tasks',  -- a task, verb first
        'title', 'Review a project''s tasks',
        'description', 'What, when to use it, Read-only or Writes, detail levels, free-text fields',
        'permissions', jsonb_build_array('task.read'),   -- or 'access', ...
        'tags', jsonb_build_array('tasks', 'read'),
        'inputSchema', jsonb_build_object('type', 'object', 'additionalProperties', false, ...),
        'outputSchema', jsonb_build_object('type', 'object', ...)),
    $body$ ... $body$);
```

- **`effect` is required on a tool**, refused on a resource or prompt.
  Annotations are derived from it (`readOnlyHint`, `idempotentHint`,
  `destructiveHint`); hand-written `annotations` are refused.
- **A write a person must confirm is a proposal tool**: `'effect',
  'proposal', 'proposes', '<write tool>'`, no `reversible`. The target is a
  registered tool with effect `write`, registered first. The body only calls
  `membership.fn_propose('<write tool>', (request).arguments)` and answers
  `{"proposalId": ...}`; nothing waits. Declare its own permission
  (`task.propose`): any role granting it may propose, without the target's.
  A person acting as themselves with `proposals.decide` confirms at
  `POST /proposals/{id}/confirm`, which runs the target as that person under
  the target's permission, or rejects at `.../reject`. An agent never
  decides, and a proposal is decided once.
- `$ref` in a tool schema is local (`#/$defs/<name>`). `readOnly` is refused
  on MCP handlers: every exchange is logged in `api.mcp_exchange`.
- A resource declares `uriTemplate` and `mimeType`; a prompt `arguments`.
  Pick by who drives it: the model calls tools, the host reads resources, the
  user picks prompts.

[A worked tool](references/tool-example.md) deploys as written.

## Designing the tool

- **Name a task, not a table.** `review_project_tasks`, not `get_task` plus
  `list_tasks` plus `get_project`. A table mirror moves the joins, the rules
  and the order of steps into the model, which gets them wrong and spends a
  round trip on each.
- **One call answers the task in one snapshot.** Figures that must
  reconcile (a total and its rows) come from one statement. Where a REST page
  already builds the same view, the page and the tool share one `LANGUAGE sql
  STABLE` function returning `jsonb`.
- **Filters answer the question.** Give the tool the filters its task needs
  (state, due date, "assigned to me"), so one call returns the rows that
  matter, not a list to page through.
- **Let the caller choose the cost.** `detail` (`summary` default, `full`),
  `limit` with a hard ceiling and `offset`; answer `total`, `hasMore`, and
  `omitted` for what was left out.
- **Write for a reader who has never seen the domain.** The description, in
  a fixed order: what it does; "Use it ..." and which tool fits better
  otherwise; "Read-only" or "Writes ..."; what each detail level holds; which
  answer fields are free text. Every property described, with its unit,
  format and default; every enum value explained.
- **`outputSchema` on every tool that answers data**, and every success
  passes it as `structured_content`, with its serialization as the one text
  item for clients that read only text.

## The answer is a briefing

The same keys in every tool: the rows or an `outcome`, `hints` (`{code,
severity, message}`), `nextActions` (`{tool, arguments}` filled from this
answer, only calls the caller may make), `asOf`, and `total`/`hasMore`/
`omitted` for lists. Facts earn a place only when they change the next
decision. [Briefing](references/briefing.md) has the shape, refusals with
rule and remedy, "did you mean" recovery, and write tools.

## Bodies refuse, never raise

1. Read every argument with `?>`, never `::`.
2. Compute every problem the schema check leaves at once as `(field,
   reason)` rows: `invalid`, `out_of_range`, `required`. Before the body
   runs, the framework refuses a wrong `type`, a value outside `enum`, a
   missing `required` key, an undeclared one with `additionalProperties:
   false`, `const`, the bounds (`minimum`, `maximum`, the exclusive ones) and
   the sizes (`minLength`, `maxLength`, `minItems`, `maxItems`,
   `uniqueItems`) and the schema `false`, at every depth (`properties`,
   `items`, `#/$defs`). `format` and `pattern` are hints it does not check:
   those are the body's. Every other keyword (`oneOf`, `if`, `multipleOf`,
   `prefixItems`, an object `additionalProperties`, ...) is refused at
   registration, so what `tools/list` publishes is what is enforced. Use
   the entity's `core.fn_<entity>_problems` where one exists.
3. Refuse with `api.mcp_tool_result(content, request_id, true)`, one text
   item `{code, rule, remedy, invalidParams}`: a result with `isError`, which
   the model reads. A JSON-RPC error is for protocol failures only.
4. Never `RAISE`, never `EXCEPTION`, never `SQLERRM` in an answer. An error
   the body did not foresee becomes a sanitized `isError` result that tells
   the model nothing it can fix.
5. Never call a `pg_temp` function from a body: it ends with the deploy
   session, so the tool deploys green and fails on every call after it.
6. An error answer changes nothing. On an `isError` result, a JSON-RPC error,
   or a result the framework rejects, every write the call made rolls back;
   the exchange log still records the answer. A refusal after a write is safe.

`lib/__test__/test_mcp_tool_contract.sql` calls every registered tool with
wrongly typed arguments and fails the deploy when a body raises.

## Untrusted text apart from guidance

Anything a person or another system stored (a title, a comment, an imported
description) may carry text written to steer the model.

- Descriptions, hint messages, next actions, `forPerson` and server
  instructions are built from the server's own literals and stable codes,
  never by putting a stored value or an argument into a sentence.
- Stored text travels as values in `structuredContent` (and the JSON of the
  text item), and the description names those fields as free text.
- The database still decides: a model talked into a call gets only what its
  person may do (declared access, row-level security, kernel refusals).
  Destructive tools are separate tools, so a client can ask the person first.

## Identity

An MCP caller is the agent of a connection a person approved on the web
(`POST /connections`, naming the organization and an `agent.*` role), acting
for that person with the role's permissions intersected with theirs.
The client signs in with OAuth (the gateway verifies the token and passes
its issuer, subject and client id; the first call answers `-31001` with a
pairing code the person approves at `POST /connections/approve`) or sends a
handle as `Authorization: Bearer cn_...` or `X-Connection`; either way the
database resolves the person (the effective principal, what row-level
security sees), the connection's agent (the actual principal) and the
organization fixed at approval. Nothing on MCP signs anyone up, and a
connection holds no platform permission. A paused or switched-off agent has
no identity and answers `-31001` with the reason; an unknown, revoked or
expired handle is HTTP 401.
In a body read identity through `api.current_principal_id()` and
`api.current_organization_id()`; `api.current_actual_principal_id()` is the
agent and `api.current_actor_kind()` is `agent`. Every call is logged with
both principals and the actor kind.

## Errors

| Situation | Answer |
|---|---|
| bad values, a refused action, nothing found | `isError` result with `{code, rule, remedy, invalidParams}`, from the body |
| any `inputSchema` violation, at any depth | `isError` result, from the framework, before the body runs |
| unknown or hidden tool; a prompt's missing argument | `-32602`, from the framework |
| no permission or no organization | `-31003` with `data.code` `pgmi.forbidden`, `pgmi.organization_required`, from the framework |
| a client not yet paired, paused, switched off, or of no web-signed-in person | `-31001` with `data.code` `membership.connection.pending` (with `pairingCode`, `expiresAt`), `.paused`, `.disabled`, `.not_admitted`, or `pgmi.client_not_admitted` when the server admits no OAuth client, at HTTP 200, before any handler |
| an unknown, revoked or expired connection handle | HTTP 401 with `WWW-Authenticate`, from the gateway (the database answers `-31001` `pgmi.connection_unknown`) |
| a transaction weaker than the handler's isolation floor | `-32603` with `data.code` `pgmi.transaction_isolation_too_weak` |
| retries ran out; no database connection | `-31000` with `data.code` `pgmi.transaction_retryable`, `pgmi.unavailable`, from the gateway |
| headers that disagree with the body, an unknown version | `-32020` / `-32022` from `api.mcp_request_policy`, HTTP 400, nothing dispatched or logged |
| no credential while OAuth is on; a bad token | HTTP 401 with `WWW-Authenticate`, from the gateway |
| a resource that does not exist | `api.mcp_resource_not_found` (`-32602`); an empty `contents` becomes it |
| a success that breaks the tool's `outputSchema` | sanitized `isError` result, from the framework; the log keeps why |
| a resource or prompt that cannot be served | `api.mcp_resource_error` / `api.mcp_prompt_error` (`-32603`) |

pgmi's own codes (`-31000`, `-31001`, `-31003`) sit outside the JSON-RPC
reserved range `-32768`..`-32000`; branch on `data.code`, not the number.

[Protocol and builders](references/protocol.md) has method routing, the two
protocol eras, envelopes and every builder.

## Testing

Call `api.mcp_call_tool(name, arguments, context, request_id)` with
`pg_temp.test_mcp_context(login, organization)` as the context and assert on
`(...).envelope`: the echoed `id`, `result.structuredContent`, the text item,
`result.isError` with each refusal, and the anonymous call (`NULL` context).

## Before calling the tool done

- [ ] named for a task, verb first; it replaces calls rather than adding one
- [ ] one call, one snapshot; a page's read function reused, not copied
- [ ] `detail`, a capped `limit`, `total`, `hasMore`, `omitted`
- [ ] description in the fixed order; every property and enum value described
- [ ] `outputSchema` declared; `structured_content` and its text on every success
- [ ] every refusal at once, coded, with rule and remedy; no raise, no `SQLERRM`
- [ ] guidance from literals; stored text only as values
- [ ] next actions filled and limited to what the caller may call
- [ ] `effect` right; writes through kernels, all or nothing
- [ ] tested signed in, anonymous, and with each refusal
