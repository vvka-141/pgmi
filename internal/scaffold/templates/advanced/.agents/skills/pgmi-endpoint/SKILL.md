---
name: pgmi-endpoint
description: Adds a domain entity and its REST endpoints to this advanced-template project end to end — the run-once table file, the kernel functions, the handler registrations and the test, with their sort keys, access declaration and row-level security, plus the handler metadata keys (path templates, querySchema, inputSchema, outputSchema, successStatus, readOnly). Use when adding a table with an API, a new route or a resource, or when a handler registration fails the deploy.
---

# Adding an endpoint

A feature is four files. [The worked example](references/example.md) adds
`project` with create, list and read; every block in it deploys as written.

| Layer | File | `idempotent` | Holds |
|---|---|---|---|
| Structure | `core/<entity>-structure.sql` | `false` (run once) | table, indexes, row-level security |
| Kernel | `core/<entity>.sql` | `true` (every deploy) | write functions returning the row, the JSON formatter, views |
| Handlers | `api/<entity>-handlers.sql` | `true` | `api.create_or_replace_rest_handler(...)` calls |
| Test | `api/__test__/test_<entity>.sql` | no `<pgmi-meta>` | `api.rest_invoke` calls with asserted status and body |

`core/` is a top-level folder for the application's entities; create it.
`lib/core/` is the template's framework: put nothing of yours there. The
schema is `core` in both.

Neighbouring skills: pgmi-layers, then pgmi-api-layer (tables, kernels, views
and where a rule lives, and this template's composite tenant keys),
pgmi-handlers (what goes inside a handler body), pgmi-api
(protocols, identity, access), pgmi-mcp (MCP tools),
pgmi-metadata (`<pgmi-meta>` blocks), pgmi-testing (test files and fixtures).

## Order and sort keys

- Application files sort at `005/100/...` or later; `001` to `004` and
  `005/000/...` are the framework. Keep one prefix per entity, structure before kernel before
  handlers: `005/100/project/1`, `/2`, `/3`. Check with `pgmi metadata plan .`.
- The `<pgmi-meta>` block is a `/* */` comment. A file without a place in the
  plan stops the deploy before anything runs.
- Registering a handler compiles its body, so the kernel's types and
  functions must exist first.

## Rules the deploy enforces

- **Structure runs once, code every deploy.** A table, index, policy or
  `ALTER TABLE` in a re-run file, or a function in a run-once file, is
  refused by name. A run-once file that already ran is never edited: put the
  change in a new run-once file sorted after it.
- **Every table is forced to row-level security or exempt with a reason.**
  An organization's rows: `organization_id` plus
  `SELECT core.apply_org_rls('core.<table>')`. Shared rows:
  `SELECT pg_temp.exempt_from_rls('core.<table>', '<reason>')`.
- **Every foreign key has an index leading with its columns**, or a
  `pg_temp.exempt_from_fk_index(...)` with the reason.
- **Every view is `WITH (security_invoker = true)`**, so the reader's
  row-level security applies through it.
- **A foreign key to another organization-scoped table is composite**:
  `(organization_id, project_id) REFERENCES core.project (organization_id, object_id)`,
  and the target has `UNIQUE (organization_id, object_id)` (references/example.md, sections 1 and 5).
  A key on the id alone would accept another organization's row; the deploy
  refuses it. See pgmi-api-layer.
- **Every handler declares exactly one of `access` or `permissions`.**
- **Every REST handler declares `outputSchema`**; the OpenAPI test fails the
  deploy otherwise. A success other than 200 also declares `successStatus`
  (201, 204, or `[201, 200]` when both happen): registration refuses a body
  whose literal 2xx, in a response call or a `CASE` branch, is not declared.
- **Every refusal carries `code =>`**; a 409 or 422 may carry `invalid_params =>` instead.
- **A 201 declares `locationTemplate`** (`/projects/{id}`) when a GET reads
  what it created, or `noLocation` with the reason nothing can GET it;
  registration refuses a 201 with neither.

## Rules the deploy does not check

- **Views go through `core.ensure_view`**, not a bare `CREATE OR REPLACE
  VIEW`: it runs the statement only when the view changed, so a redeploy
  does not take an ACCESS EXCLUSIVE lock that queues every reader.

## Access

| Declaration | Who gets in | When |
|---|---|---|
| `'permissions', jsonb_build_array('project.write')` | a member whose roles grant one of them, in an organization session | the default for application data |
| `'access', 'organization'` | any signed-in member, in an organization session | the organization's data where every member may act; no matrix row |
| `'access', 'authenticated'` | any signed-in person | the caller's own data, outside any organization |
| `'access', 'public'` | anyone | health checks, public catalogues; never a kernel that writes |

The permissions and roles are `data/authorization.json`. A new permission
goes in its `permissions` list, and in the `grants` of every role that may
use it; a permission no role grants leaves the handler unreachable. That
includes the `agent.*` roles: an MCP agent holds its role's permissions
intersected with its person's, so a permission only `org.*` roles grant is
out of every agent's reach but `agent.delegate`'s. A handler
that declares `permissions` needs a row in the hand-written table of
`api/__test__/test_access_matrix.sql`; the deploy fails until it has one.
`organization` names no permission, so it needs no row: the worked example
uses it and deploys as written. Switch to `project.read` / `project.write`
when roles must tell members apart, and add the matrix row. Never check the
session in the body: the dispatcher (`api.rest_invoke`) answers 401 and 403
`pgmi.organization_required` before it runs.

## Handler metadata

[Metadata and routes](references/metadata.md) lists every key with its
default, the path template rules and what the dispatcher checks before the body
runs. The ones that bite:

- `path` is a template: `/projects/{id:uuid}`. No regex, no trailing `/`, no
  query part. Read parameters from `(request).path_params`.
- `inputSchema` is checked at every depth by the dispatcher (type, enum,
  const, required, bounds, sizes): a violation answers 400
  `pgmi.invalid_body` before the body runs. `format`, `pattern` and
  cross-field rules are the body's, and answer 422.
- `querySchema` declares the query parameters the body reads.
- `httpMethod` is a regular expression: anchor it, `^POST$`.
- `readOnly: true` makes the route unlogged: its READ ONLY transaction cannot
  write the exchange log. `autoLog: true` on it is refused.

## Deploy and verify

```bash
pgmi metadata plan .
pgmi deploy . -d <database> --param env=dev --overwrite --force
```

`--overwrite` drops the database first, so use it only on a database you can
lose. A green deploy commits with exit 0; a failed test rolls everything back.
When it is green, deploy once more without `--overwrite` to prove the project
redeploys onto existing data:

```bash
pgmi deploy . -d <database> --param env=dev
```

The pgmi-workflow skill has the full loop and pgmi-debug-deploy reads a
failure.

## Before calling it done

- [ ] structure file run-once, kernel and handlers re-run, test without metadata
- [ ] row-level security or an exemption on every new table
- [ ] kernels return the whole row; handlers never write a table directly
- [ ] kernels named `core.fn_<verb>_<entity>`; every rule, "not blank"
      included, lives in `core.fn_<entity>_problem(s)`, read by kernel and
      handler (pgmi-layers)
- [ ] access declared; a `permissions` handler has its access-matrix row
- [ ] `outputSchema` on every handler, `successStatus` on every non-200 success
- [ ] the test covers success, each refusal the body returns, 401 and a forged handle
