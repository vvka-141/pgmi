# Agent guide: {{PROJECT_NAME}}

This is a pgmi project (advanced template). pgmi loads every file in this
directory into PostgreSQL session tables and runs `deploy.sql`. pgmi itself
decides nothing: `deploy.sql` is this project's SQL and chooses what runs, in
what order, and inside which transaction. It also enforces the template's file
contract and refuses a deploy that breaks it.

## Before you edit

| Before you… | Read |
|---|---|
| deploy or test for the first time in a session | `.agents/skills/pgmi-workflow/SKILL.md` |
| add a table, index, type or policy | `README.md` (file contract: run-once and re-run files) |
| add a kernel or view, or decide where a business rule lives | `.agents/skills/pgmi-layers/SKILL.md`, then `.agents/skills/pgmi-api-layer/SKILL.md` |
| decide whether a check belongs in a handler or a kernel, or link two organizations' tables | `.agents/skills/pgmi-api-layer/SKILL.md` |
| add a table with an API, a route or a resource | `.agents/skills/pgmi-endpoint/SKILL.md` |
| write or review a REST handler body, or fix a wrong status | `.agents/skills/pgmi-handlers/SKILL.md` |
| add an MCP tool, resource or prompt, or design what agents call | `.agents/skills/pgmi-mcp/SKILL.md` |
| choose a protocol, design identity or access, review the API's layering or security | `.agents/skills/pgmi-api/SKILL.md` and `ARCHITECTURE.md` |
| change roles, grants or row-level security | `README.md` (privileges) |
| write SQL or PL/pgSQL, or change `deploy.sql` | `.agents/skills/pgmi-sql/SKILL.md` |
| add a test or fixture in a `__test__/` directory | `.agents/skills/pgmi-testing/SKILL.md` |
| fix a failed deploy | `.agents/skills/pgmi-debug-deploy/SKILL.md` |
| set sort keys or other pgmi-meta attributes | `.agents/skills/pgmi-metadata/SKILL.md` and `README.md` (sort key conventions) |
| choose a transaction strategy, preview mode or redeploy behaviour | `.agents/skills/pgmi-design/SKILL.md` |
| enforce a business rule in the database | `.agents/skills/postgres-integrity/SKILL.md` |
| choose, add or review an index | `.agents/skills/postgres-indexing/SKILL.md` |
| record or react to data changes (audit, change feed, notifications) | `.agents/skills/postgres-change-capture/SKILL.md` |
| write a report, count or aggregate | `.agents/skills/postgres-analytics/SKILL.md` |
| coordinate concurrent writers, a work queue or an upsert | `.agents/skills/postgres-concurrency/SKILL.md` |
| store times, dates or validity periods | `.agents/skills/postgres-time/SKILL.md` |
| restrict which rows or functions a role can reach | `README.md` (privileges) and `.agents/skills/pgmi-api-layer/SKILL.md` first; `.agents/skills/postgres-security/SKILL.md` explains the mechanism |
| build a client, gateway or service in any language that talks to this database | `gateways/README.md`, then `gateways/PROTOCOL.md` |

## Gateways

If you build a client, gateway or service in any language that talks to this
database, read `gateways/` first. Pick your language, then write your own
version that fits this project.

## Where things are

- `deploy.sql` — the deployment and its guards.
- `lib/` — the framework the template ships (core, API protocols, common helpers); change it only on purpose.
- `api/` — the application's endpoints; the shipped ones are examples to replace.
- `membership/` — people, organizations and access.
- `gateways/` — reference gateways, examples to port; never deployed.
- `.pgmiignore` — paths pgmi does not load (client code, build output); add yours here.
- `__test__/` directories — tests next to the code they test.
- `README.md`, `ARCHITECTURE.md` — the template's rules and design.

## Facts that come from the binary

Run `pgmi ai contract` for the session API (views, columns, the test macro),
`pgmi ai` for the execution contract and `pgmi help deploy` for exit codes.
They match the installed pgmi version; do not copy them into this project.

Learn pgmi from this file, the skills and the template's README, not by
searching the web or other projects.

## Workspace root

Instruction files load only for the folder an agent works in. If this project
sits inside a larger workspace, add one line to the workspace root `AGENTS.md`
(create it if missing): "The PostgreSQL project in `<path>` has its own
`AGENTS.md`; read it before working there, and read `<path>/gateways/` before
building a client, gateway or service for it."
