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

Neighbouring skills: pgmi-layers (tables, kernels, views and where a rule
lives), pgmi-handlers (what goes inside a handler body), pgmi-api
(protocols, identity, access), pgmi-mcp (MCP tools),
pgmi-metadata (`<pgmi-meta>` blocks), pgmi-testing (test files and fixtures).

## Order and sort keys

- Application files sort at `005/...` or later; `001` to `004` are the
  framework. Keep one prefix per entity, structure before kernel before
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
- **Views go through `core.ensure_view`**, never `CREATE VIEW` directly.
- **Every handler declares exactly one of `access` or `permissions`.**
- **Every REST handler declares `outputSchema`**; the OpenAPI test fails the
  deploy otherwise. A success other than 200 also declares `successStatus`
  (201, 204): registration refuses a body that returns a literal 2xx other
  than the declared one.
- **Every 409 and 422 carries `code =>` or `invalid_params =>`.**

## Access

| Declaration | Who gets in | When |
|---|---|---|
| `'permissions', jsonb_build_array('project.write')` | a member whose roles grant one of them, in an organization session | the default for application data |
| `'access', 'authenticated'` | any signed-in person | data that is the caller's own, or checked in the body |
| `'access', 'public'` | anyone | health checks, public catalogues; never a kernel that writes |

The permissions and roles are `data/authorization.json`. A new permission
goes in its `permissions` list, and in the `grants` of every role that may
use it; a permission no role grants leaves the handler unreachable. That
includes the `agent.*` roles: an MCP agent holds its role's permissions
intersected with its person's, so a permission only `org.*` roles grant is
out of every agent's reach but `agent.delegate`'s. A handler
that declares `permissions` needs a row in the hand-written table of
`api/__test__/test_access_matrix.sql`; the deploy fails until it has one. The
worked example uses `authenticated` so that it deploys without that row;
switch it to `project.read` / `project.write` when you adopt it, and its
organization check goes away: the gateway answers 403
`pgmi.organization_required` itself.

## Handler metadata

[Metadata and routes](references/metadata.md) lists every key with its
default, the path template rules and what the gateway checks before the body
runs. The ones that bite:

- `path` is a template: `/projects/{id:uuid}`. No regex, no trailing `/`, no
  query part. Read parameters from `(request).path_params`.
- `inputSchema` is checked at its top level by the gateway: a missing
  required key or a wrong type answers 400 `pgmi.invalid_body` before the
  body runs. The body checks the rest and answers 422.
- `querySchema` declares the query parameters the body reads.
- `httpMethod` is a regular expression: anchor it, `^POST$`.

## Deploy and verify

```bash
pgmi metadata plan .
pgmi deploy . -d <database> --param env=dev --overwrite --force
```

A green deploy commits with exit 0; a failed test rolls everything back. The
pgmi-workflow skill has the full loop and pgmi-debug-deploy reads a failure.

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
