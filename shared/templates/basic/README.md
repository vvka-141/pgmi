# {{PROJECT_NAME}}

A PostgreSQL project deployed by pgmi, in two layers: tables that run once,
views and functions that re-run, and tests that gate the commit.

## Quick Start

Set your database password and deploy:

```bash
export PGPASSWORD="your-postgres-password"    # or use a connection string
pgmi deploy . -d {{PROJECT_NAME}} --overwrite --force --param env=dev
```

`--overwrite --force` drops and recreates the database — use only for local development.
For an existing database, deploy incrementally (no `--overwrite`):

```bash
pgmi deploy . -d {{PROJECT_NAME}} --param env=prod
```

Edit `pgmi.yaml` to set connection defaults (host, port, username) so you
don't need to repeat them on every command.

### On a managed host

Deploy as a login with `CREATEDB`, such as the server's administrator login,
and sign in the way the host expects: `--azure` (Azure Database for
PostgreSQL), `--aws` with `--sslrootcert` and the RDS certificate bundle
(Amazon RDS and Aurora), `--google --google-instance <project:region:instance>`
(Google Cloud SQL). On Databricks Lakebase, connect to the maintenance
database `databricks_postgres` ([Lakebase guide](https://vvka-141.github.io/pgmi/docs/lakebase/)).
Connect to the server directly, not through a transaction pooler: a deploy is
one session.

## What This Does

1. Refuses the deploy if a run-once file changed after it ran here (below)
2. Runs the SQL files in path order: `physical/` first, then `semantic/`;
   an SQL file anywhere else (outside `__test__/`) is refused before anything runs
3. On `env=dev`, creates a `Demo` project through the writer function
4. Runs the tests, rolls their changes back, and commits only if all pass

## Two layers

| Layer | Folder | Holds | Runs |
|---|---|---|---|
| Physical | `physical/` | tables, keys, constraints, indexes | once per database |
| Semantic | `semantic/` | views (the read surface) and functions (the only writers) | on every deploy |

Every rule is stated once. The database refuses invalid rows through
constraints (`task_open_title_key`: two open tasks of a project never share a
title). A writer such as `fn_add_task` asks a refusal function
(`fn_task_problems`) and returns NULL when it refuses: `SELECT *` shows one row
whose columns are all NULL, so a caller tests `(fn_add_task(...)).task_id IS NULL`,
not `FOUND`. A caller that wants the reason asks the same function. Reads go through `project_view`, which carries
each project's task counts and its state.

```sql
SELECT * FROM fn_create_project('Launch');
SELECT * FROM fn_add_task((SELECT project_id FROM project WHERE name = 'Launch'), 'Write the plan');
SELECT name, open_task_count, state FROM project_view;
```

## Run-once files

A file in `physical/` opens with a header that marks it run-once:

```sql
/*
<pgmi-meta id="a7ba0c95-d9fc-44f9-8537-de2631a38a8a" idempotent="false">
  <description>Projects</description>
</pgmi-meta>
*/
CREATE TABLE project (...);
```

`deploy.sql` records each run-once file it ran in `deployment.run_once` (its
id, path and checksum) and skips it afterwards. A file without the header is
re-run on every deploy, so it must be safe to repeat: `CREATE OR REPLACE`.

**The rule:** once a database that keeps its data ran a run-once file, that
file is frozen. Edit it, move it, flip it to `idempotent="true"` or delete
it, and the next deploy fails before anything runs, naming the file:

```
Run-once file(s) changed after they ran on this database:
  ./physical/002_task.sql: content edited
```

Comments, whitespace outside literals and keyword case are not edits. To change a table, add
a new run-once file after it, with a fresh id (`uuidgen`):

```sql
/*
<pgmi-meta id="<new uuid>" idempotent="false">
  <description>Tasks get a due date</description>
</pgmi-meta>
*/
ALTER TABLE task ADD COLUMN due_on date;
```

While a database may still be thrown away, edit the table's own file instead
and recreate it with `--overwrite --force`. Write tables in their final form:
plain `CREATE TABLE` with every column, no `IF NOT EXISTS`.

## Project Structure

```
{{PROJECT_NAME}}/
├── deploy.sql                  # The deployment: drift refusal, file loop, seed, test gate
├── project.json                # Project metadata (read by deploy.sql, not by pgmi)
├── pgmi.yaml                   # Connection and parameter defaults
├── physical/                   # Run-once: tables, keys, indexes
│   ├── 001_project.sql
│   ├── 002_task.sql
│   └── __test__/               # The constraints refuse what they should
├── semantic/                   # Re-run: views and writer functions
│   ├── project.sql
│   ├── task.sql
│   └── __test__/               # The writers refuse and accept what they should
└── __test__/
    └── test_function_privileges.sql
```

## Tests

Every SQL file under a `__test__/` directory is a test; `_setup.sql` is that
directory's fixture. `CALL pgmi_test()` in `deploy.sql` runs them inside the
deploy's transaction and rolls their changes back. One failing test fails the
deploy with exit code 13 and nothing committed.

## Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| env | none | Required: `dev`, `test` or `prod`. Only `dev` seeds the `Demo` project; a missing or other value refuses the deploy |

Pass it on every deploy: `--param env=dev` locally, `PGMI_PARAM_ENV` in CI.

## Working with an AI assistant

`AGENTS.md` tells an assistant which skill to read before each kind of change;
`CLAUDE.md` imports it for Claude Code. The skills in `.agents/skills/` cover
the two layers, deploying, testing, SQL and PostgreSQL recipes. They belong to
this project: commit them, and change them when your conventions change.
They are opinions, not pgmi rules: delete or rewrite any skill that does not
fit how you build.

For the session API of the installed pgmi, run `pgmi ai contract`.

## Learn More

- [pgmi Documentation](https://github.com/vvka-141/pgmi)
- [The basic template](https://vvka-141.github.io/pgmi/docs/basic/): what it ships and why
- Advanced template: `pgmi init myapp --template advanced`
