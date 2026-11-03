# Agent guide: {{PROJECT_NAME}}

This is a pgmi project (basic template). pgmi loads every file in this
directory into PostgreSQL session tables and runs `deploy.sql`. pgmi itself
decides nothing: `deploy.sql` is this project's SQL and chooses what runs, in
what order, and inside which transaction.

## Before you edit

| Before you… | Read |
|---|---|
| deploy or test for the first time in a session | `.agents/skills/pgmi-workflow/SKILL.md` |
| plan a new feature or data model | `.agents/skills/pgmi-design/SKILL.md`, then `.agents/skills/pgmi-layers/SKILL.md` |
| add or change a table, index or constraint in `physical/` | `.agents/skills/pgmi-layers/SKILL.md` and `deploy.sql` (run-once files and the drift refusal) |
| add or change a view or function in `semantic/`, or decide where a business rule lives | `.agents/skills/pgmi-layers/SKILL.md` |
| write SQL or PL/pgSQL, or change `deploy.sql` | `.agents/skills/pgmi-sql/SKILL.md` |
| add a test or fixture in a `__test__/` directory | `.agents/skills/pgmi-testing/SKILL.md` |
| fix a failed deploy | `.agents/skills/pgmi-debug-deploy/SKILL.md` |
| control execution order with pgmi-meta blocks | `.agents/skills/pgmi-metadata/SKILL.md` |
| choose a transaction strategy, preview mode or redeploy behaviour | `.agents/skills/pgmi-design/SKILL.md` |
| enforce a business rule in the database | `.agents/skills/postgres-integrity/SKILL.md` |
| choose, add or review an index | `.agents/skills/postgres-indexing/SKILL.md` |
| record or react to data changes (audit, change feed, notifications) | `.agents/skills/postgres-change-capture/SKILL.md` |
| write a report, count or aggregate | `.agents/skills/postgres-analytics/SKILL.md` |
| coordinate concurrent writers, a work queue or an upsert | `.agents/skills/postgres-concurrency/SKILL.md` |
| store times, dates or validity periods | `.agents/skills/postgres-time/SKILL.md` |
| restrict which rows or functions a role can reach | `.agents/skills/postgres-security/SKILL.md` |

## Where things are

- `deploy.sql` — the deployment: refuses a changed run-once file and a `physical/` file without the run-once header, runs the files in path order, then the tests.
- `physical/` — tables, keys, constraints, indexes; run-once files, frozen once a kept database ran them.
- `semantic/` — views and the functions that are the only writers; re-run on every deploy.
- `__test__/` directories — tests next to the layer they test; `_setup.sql` is a directory's fixture.
- `project.json`, `pgmi.yaml` — project metadata and connection defaults. `pgmi.yaml` sets `env: dev`; `env` is required and is `dev`, `test` or `prod`, and only `dev` seeds the `Demo` project. Deploy a shared database with `--param env=prod`.

## Facts that come from the binary

Run `pgmi ai contract` for the session API (views, columns, the test macro),
`pgmi ai` for the execution contract and `pgmi help deploy` for exit codes.
They match the installed pgmi version; do not copy them into this project.

Learn pgmi from this file, the skills and `deploy.sql`, not by searching the
web or other projects.

## Workspace root

Instruction files load only for the folder an agent works in. If this project
sits inside a larger workspace, add one line to the workspace root `AGENTS.md`
(create it if missing): "The PostgreSQL project in `<path>` has its own
`AGENTS.md`; read it before working there."
