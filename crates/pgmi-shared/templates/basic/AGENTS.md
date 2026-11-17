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

The skills are this project's opinions, not pgmi rules. Its owner may delete or
rewrite any of them; follow the ones present.

## Where things are

- `deploy.sql` — the deployment: refuses a changed run-once file, a `physical/` file without the run-once header and an SQL file outside `physical/`, `semantic/` and `__test__/`, runs the files in path order, then the tests.
- `physical/` — tables, keys, constraints, indexes; run-once files, frozen once a kept database ran them.
- `semantic/` — views and the functions that are the only writers; re-run on every deploy.
- File order is path order, no sort keys: a new table is `physical/003_<entity>.sql`; a `semantic/` file that uses another file's view or function must sort after it (`task_report.sql` after `task.sql`).
- `__test__/` directories — tests next to the layer they test; `_setup.sql` is a directory's fixture.
- `.pgmiignore` — paths pgmi does not load; parameter files (`*.params`, `*.env`) hold secrets and stay out of the session and out of git.
- `project.json`, `pgmi.yaml` — project metadata and connection defaults. `env` is required on every deploy and deliberately not in `pgmi.yaml`: `dev`, `test` or `prod`, and only `dev` seeds the `Demo` project. Locally `--param env=dev`; in CI `PGMI_PARAM_ENV`.

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
