---
title: "Basic template"
description: "The basic template: classical PostgreSQL design in two layers, a run-once ledger, a test gate before COMMIT, and functions your application calls by name."
weight: 130
---

# The basic template

The basic template is conventional database development, with the
application outside PostgreSQL. Tables hold the data, views are the read
surface, and by convention functions are the writers. Your
application connects as usual and calls those functions by name. There is no
API layer, gateway or MCP inside the database. Choose it when you want a
well-structured database and keep HTTP, if any, in your application.

Every project first hits the same boring problem: remember which scripts
already ran, and do not run them again. That infrastructure takes work to get
right, so the basic template ships it. It is scaffolding, not a framework:
pgmi itself stays an execution fabric, and after `pgmi init` the generated
code is yours to change, replace or delete.

## What you get out of the box

| Feature | What it does |
|---|---|
| Run-once ledger | `deployment.run_once` records each run-once file that ran (id, path, checksum); later deploys skip it |
| Drift refusal | A run-once file that already ran and was edited, moved, deleted or made idempotent fails the deploy before anything runs, naming the file |
| Test gate | `CALL pgmi_test()` runs every test inside a savepoint before `COMMIT`; one failure fails the deploy with nothing committed |
| Required `env` | `--param env=dev`, `test` or `prod` is required; a missing or other value refuses the deploy |
| No `PUBLIC` execute | `ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC`: grant each role what it needs |
| Dev-only seed | Only `env=dev` creates a `Demo` project, through the writer function like any caller |

![Before anything runs, both templates refuse a deploy if a run-once file that already ran has changed; then re-run files always run and run-once files are skipped once they have run — basic records only run-once files in deployment.run_once, advanced logs every file in internal.deployment_script_execution_log.](../diagrams/d16-run-once-ledger.drawio.svg)

Comments, whitespace outside literals and keyword case are not edits: the checksum ignores
them. To change a table that already ran, add a new run-once file after it.

## Two layers, called by name

![A basic-template project in two layers: deploy.sql runs the run-once tables in physical/ and re-runs the views and functions in semantic/, and your application writes by calling functions such as fn_create_project('Launch') and reads project_view with plain SQL, getting a row of NULLs when a write is refused.](../diagrams/d17-basic-two-layers.drawio.svg)

`physical/` holds tables, keys, constraints and indexes. Each file runs once
per database. `semantic/` holds views and functions, re-run on every deploy
with `CREATE OR REPLACE`. Each rule is stated once: a constraint in the table,
or a refusal function such as `fn_task_problems` that the writer asks. A
writer that refuses returns NULL, and the caller asks the same refusal
function for the reason.

```sql
SELECT * FROM fn_create_project('Launch');
SELECT * FROM fn_add_task((SELECT project_id FROM project WHERE name = 'Launch'), 'Write the plan');
SELECT name, open_task_count, state FROM project_view;
```

## How to start

```bash
pgmi init myapp --template basic
pgmi deploy myapp -d mydb --param env=dev
```

`pgmi init` prints the deploy command with your database name. Add
`--overwrite --force` to drop and recreate a local database; never use it on a
database whose data you keep.

## Where next

- [Template README](https://github.com/vvka-141/pgmi/blob/main/shared/templates/basic/README.md): the run-once rule, project structure, tests and parameters
- [Choosing a template](../templates.md): minimal, basic and advanced compared
- [Testing](../TESTING.md): `__test__/` directories, fixtures and `pgmi_test()`
- [Metadata](../METADATA.md): the `<pgmi-meta>` header that marks a file run-once
