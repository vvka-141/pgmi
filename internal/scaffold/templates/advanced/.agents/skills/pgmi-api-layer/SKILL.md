---
name: pgmi-api-layer
description: Add-on to pgmi-layers for this advanced-template project — what the physical and semantic layers look like here (entity tables, composite tenant foreign keys, forced row-level security, catalogs, kernels as the writers, per-entity problem functions, views through core.ensure_view) and the third layer on top, handlers that only shape input and output. Use after pgmi-layers when adding a table, kernel, view or business rule in this project, or when deciding whether a check belongs to a handler or a kernel.
---

# The layers in this template, and the API on top

Read pgmi-layers first: physical and semantic, one rule in one place. This
skill adds what this template names and enforces, and the third layer.

| Layer | Here | Skill with the details |
|---|---|---|
| Physical | entity tables in run-once files with sortKeys `005/...` or later | pgmi-endpoint |
| Semantic | kernels (the writers), `core.fn_<entity>_problem(s)`, views via `core.ensure_view` | this one |
| API | REST, RPC and MCP handlers over one registry | pgmi-api, pgmi-handlers, pgmi-mcp |

## Physical here

- **Entities**: `object_id core.entity_id`, `organization_id`, row-level
  security forced through `core.apply_org_rls` (pgmi-endpoint).
- **A foreign key between two organizations' tables is composite**,
  `(organization_id, project_id) REFERENCES core.project (organization_id, object_id)`,
  and the target has `UNIQUE (organization_id, object_id)`. A foreign-key
  check runs past row-level security, so a key on the id alone accepts
  another organization's row. [Example](references/example.md) proves it.
- **Reference data** lives in `data/catalogs/<schema>.<table>.json`, synced by
  `SELECT pg_temp.sync_catalog('<schema>.<table>')` from a re-run file sorted
  after the table, never `INSERT`ed by hand.
- **Change tracking** is `core.apply_change_tracking('<table>')`, not a
  hand-written trigger.

## Semantic here

- **Kernels** are the writers: `<schema>.fn_<verb>_<entity>`
  (`core.fn_create_task`, `membership.fn_add_member`), returning the row
  type (`RETURNS core.task`). A kernel that must reach every organization is
  `SECURITY DEFINER`, handed to the kernel role, and checks its own reach
  (pgmi-api).
- **Refusal functions** are `<schema>.fn_<entity>_problem(p_object_id uuid,
  p_operation text)` and `<schema>.fn_<entity>_problems(...)`, in the
  entity's schema. A code is `<schema>.<entity>.<reason>`
  (`membership.member.not_found`); a field reason is one word (`required`).
  Neither returns an HTTP answer.
- **Views**: `WITH (security_invoker = true)`, created through
  `core.ensure_view(name, statement)`, named `core.vw_<table>`.
- **A pinned `search_path` names `common`** before `pg_temp`, or
  `lib/__test__/test_common_reach.sql` fails.

## API

A handler materializes its input, validates it, asks the problem functions,
calls one kernel and shapes the answer from the kernel's row. It runs no DML
on a table and reads views, not tables. The handler maps a problem code to a
status (404 for `<schema>.<entity>.not_found`, 409 for a state, 422 for field
problems); the rule stays in the problem function. One business operation is
one kernel and one problem function, whichever protocol calls it; a REST page
and an MCP tool for the same task share one `LANGUAGE sql STABLE` read
function returning `jsonb`.

- Adding a table with its routes, end to end: pgmi-endpoint.
- Handler bodies, casts, status codes: pgmi-handlers.
- Protocols, identity, access, roles: pgmi-api.
- Agent tools: pgmi-mcp.

## Before calling it done

- [ ] composite foreign keys between organizations' tables; RLS forced or exempt with a reason
- [ ] every write through a kernel; handlers run no DML
- [ ] kernel and handler read the same `core.fn_<entity>_problem(s)`
- [ ] views `security_invoker`, through `core.ensure_view`
