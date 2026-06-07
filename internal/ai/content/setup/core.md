## When this applies

This guidance applies only inside a **pgmi project** — a directory that contains
`deploy.sql` and/or `pgmi.yaml`. If neither file is present, this is not a pgmi
project and none of the rules below apply. In a monorepo, the pgmi project may be
a subdirectory; the `deploy.sql`/`pgmi.yaml` pair marks its root.

## What pgmi is

pgmi is a PostgreSQL-native deployment driver. It connects to a database, loads
your project's SQL files and CLI parameters into `pg_temp` views, then runs your
`deploy.sql`. From there, **your SQL drives everything** — execution order,
transaction boundaries, idempotency, retries. pgmi does not orchestrate the
deployment; `deploy.sql` does.

The practical consequence: do not look for a flag to change deployment behavior.
There isn't one, and adding one is against the design. Change `deploy.sql`.

## The core model

When pgmi runs, it exposes the project to SQL through session-scoped views in
`pg_temp`:

- `pgmi_source_view` — every project file (path, content, `is_sql_file`),
  excluding `deploy.sql` and `__test__/` files.
- `pgmi_parameter_view` — CLI `--param key=value` pairs. Also readable as
  `current_setting('pgmi.key', true)`.
- `pgmi_plan_view` — files in execution order, derived from `<pgmi-meta>` sort
  keys when present.
- `pgmi_test_source_view`, `pgmi_test_directory_view` — test files and their
  directory hierarchy.
- `pgmi_source_metadata_view` — parsed `<pgmi-meta>` blocks.

Query the `*_view` names — they are the stable contract. The underscore-prefixed
`_pgmi_*` tables are internal; do not read them.

`deploy.sql` reads these views and executes file content directly:

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

## Basic vs advanced

pgmi ships two templates, and they sit at different points on the same model:

- **Basic** — `deploy.sql` runs the files in `migrations/` in lexicographic
  order, reading `pgmi_source_view`. No metadata, no idempotency tracking. Use it
  for learning and small projects.
- **Advanced** — files carry `<pgmi-meta>` blocks with sort keys for explicit
  multi-phase ordering; `deploy.sql` reads `pgmi_plan_view`. Adds idempotency
  tracking, a role hierarchy, JWT/API-key auth, and REST/RPC/MCP routing.

Know which one you are in before editing. The presence of `<pgmi-meta>` blocks and
a `lib/api/` tree means advanced.

**The advanced template is an editable reference system, not sacred generated
code.** It is yours to read, change, and delete from. Do not treat its files as a
framework you must not touch.

## Transactional testing

Tests live in `__test__/` or `__tests__/` directories. A `_setup.sql` file in a
test directory is the fixture for that directory.

`deploy.sql` runs them with the preprocessor macro:

```sql
CALL pgmi_test();              -- run all tests
CALL pgmi_test('.*/api/.*');   -- filter by POSIX regex
```

Each test runs inside a savepoint, so **a test's own writes roll back
automatically** and tests do not pollute the database. Fixture data from
`_setup.sql` is established once per directory and stays visible to every test in
that directory (it rolls back at the directory's teardown, not between tests).

A **failing test raises an error**, and because tests run as part of the
deployment transaction, that error **aborts the deployment** — exactly what you
want in CI.

## Safety

- `--overwrite` drops and recreates the target database; `--force` skips the
  confirmation prompt. These are for **local or disposable databases**. Using them
  against a database you care about destroys it. Require explicit intent; never
  add them reflexively to a command.
- **Never put secrets on the command line.** `--param key=secret` leaks to the
  process list (`ps`), shell history, and CI logs. Passing secrets *as parameters*
  is fine and expected — just use `--params-file` (or a CI/CD-generated seeding
  file), not argv. Caveat: values are still readable via `SHOW ALL`/`pg_settings`
  within the deploy session, and a password used in `ALTER ROLE ... PASSWORD` can
  reach the server log under `log_statement=ddl`/`all` — set `log_statement`
  accordingly. The connection password belongs in the connection string or
  environment (`PGPASSWORD`, `.pgpass`).

## Advanced-only

The following apply **only** in an advanced-template project (gated behind its
`<pgmi-meta>` markers and `lib/api/` tree) — ignore them in a basic project:

- REST/RPC/MCP request routing through the `api.handler` registry.
- The four-schema layout and `owner → admin → api → customer` role hierarchy.
- Row-level security on membership tables.
- Schema/handler design conventions for the HTTP and MCP surfaces.

For any of these, read the depth skills below before editing.

## Going deeper

This file is the core model, kept deliberately short. For full detail on a
specific area, run (if the `pgmi` binary is installed):

```bash
pgmi ai skill pgmi-sql              # SQL/PL/pgSQL conventions, deploy.sql patterns
pgmi ai skill pgmi-philosophy       # why pgmi refuses orchestration flags
pgmi ai skill pgmi-metadata-system  # <pgmi-meta> blocks, sortKeys, execution ordering
pgmi ai skill pgmi-test-architecture # __test__/ dirs, fixture naming, isolation
pgmi ai skill pgmi-testing-review   # writing and debugging tests
pgmi ai skill postgresql-patterns   # EXECUTE, format(), composite types, dynamic SQL
pgmi ai skill pgmi-templates        # template internals, basic and advanced
pgmi ai skill pgmi-api-architecture # advanced: REST/RPC/MCP design
pgmi ai skill pgmi-mcp              # advanced: MCP handler implementation
pgmi ai skills                      # full list
```

If `pgmi` is not on PATH, install it with:

```bash
go install github.com/vvka-141/pgmi/cmd/pgmi@latest
```

(or download a release binary). The core model above stands on its own without
the binary; the skills add depth.
