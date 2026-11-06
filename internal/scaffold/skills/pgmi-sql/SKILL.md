---
name: pgmi-sql
description: Writes and reviews SQL and PL/pgSQL for a pgmi project — deploy.sql logic that selects and EXECUTEs project files from the session views, functions and procedures, schema changes (run-once by default, re-runnable when deploy.sql re-runs structure), dynamic SQL with format(), error handling that keeps pgmi's failure attribution, deployment parameters read with current_setting('pgmi.key', true), and function privileges. Use when writing or reviewing the SQL in deploy.sql, a migration, schema or function file, or a DO block, or when a statement fails with a PostgreSQL error such as a syntax error, cannot change return type, function does not exist, or cannot be executed from a function.
---

# Writing SQL for a pgmi project

pgmi loads every project file into session temporary tables and runs
`deploy.sql`. Everything else is this project's SQL. This skill covers that
SQL: deploy.sql logic, functions, schema changes, errors and privileges.

Neighbouring skills: pgmi-workflow (the edit, deploy and test loop),
pgmi-design (transaction boundaries, phases, preview mode, redeploy
strategy), pgmi-metadata (`<pgmi-meta>` blocks and plan order), pgmi-testing
(tests and fixtures), pgmi-debug-deploy (exit codes and failed deploys).

## Look up, don't guess

| Need | Ask |
|---|---|
| Session views, their columns, the test macro | `pgmi ai contract` |
| Which statements share a transaction | `pgmi ai` |
| Exit codes | `pgmi help deploy` |
| The plan in execution order, without a database | `pgmi metadata plan .` |
| Whether every `<pgmi-meta>` block parses | `pgmi metadata validate .` |
| Paths discovery left out of the session, and why | `pgmi deploy ... --verbose` |

## First, read this project's deploy.sql

Before you add or move a file, find four things in `deploy.sql` and the
README:

1. **What runs.** Which paths or directories it executes, and in what order:
   by path, or by `<pgmi-meta>` sort keys through `pg_temp.pgmi_plan_view`.
2. **How often.** Whether a file re-runs on every deploy or runs once and is
   then skipped. This decides how you write schema changes.
3. **Parameters.** Whether it reads `current_setting('pgmi.key', true)`
   directly, or declares, validates and defaults parameters itself.
4. **Tests.** Where it calls the test macro.

Templates answer all four differently, so never carry an answer over from
another project. A file that `deploy.sql` does not select never runs, and
nothing warns you.

## Decision guide

| You are about to | Default | Detail |
|---|---|---|
| Loop over project files in deploy.sql | Join `pgmi_source_view`, filter `is_sql_file`, order explicitly | [deploy.sql patterns](references/deploy-sql.md) |
| Wrap `EXECUTE` in an exception handler | Don't. If you must, re-raise with a bare `RAISE;` | [deploy.sql patterns](references/deploy-sql.md) |
| Read a deployment parameter | The project's own helper if deploy.sql has one, else `COALESCE(current_setting('pgmi.key', true), default)` | [deploy.sql patterns](references/deploy-sql.md) |
| Give a file a place in the plan | Where `deploy.sql` orders by path (the basic template): the directory and file name, e.g. `physical/003_<entity>.sql`. Where it orders by sort keys: a `<pgmi-meta>` block in a `/* */` comment, never `--`; see the pgmi-metadata skill | `pgmi metadata plan .` |
| Change a table that already exists | A new run-once file sorted after the one that created it (pgmi-layers); guarded re-run DDL only where deploy.sql re-runs structure | [schema changes](references/schema-changes.md) |
| Build an index concurrently, `VACUUM` | A top-level statement in deploy.sql, never inside `EXECUTE` or a `DO` block | [schema changes](references/schema-changes.md) |
| Write a function | SQL before PL/pgSQL, sets before loops, the right volatility | [functions](references/functions.md) |
| Change a function's arguments or return type | `DROP FUNCTION` the old signature in the same change | [functions](references/functions.md) |
| Build SQL from names or values | `format()` with `%I` and `%L`, or `EXECUTE ... USING` | [functions](references/functions.md) |
| Create a function other roles call | Keep `EXECUTE` away from `PUBLIC`; grant each caller. A project may fix the search_path members and each role's grant list in its tests; follow those | [functions](references/functions.md) |

## Rules that hold in every pgmi project

- **Let errors reach pgmi.** pgmi names the failing file, and the line when
  PostgreSQL reports one, from the error as PostgreSQL raised it. A handler
  that continues past `WHEN OTHERS` commits a partial deploy that reports
  success; one that re-raises with `RAISE EXCEPTION '...'` hides the file and
  turns every SQLSTATE into `P0001`.
- **Think in sets.** One `INSERT ... SELECT`, `UPDATE ... FROM` or
  `MERGE` beats a row-by-row loop. Loops in deploy.sql are for orchestrating
  `EXECUTE` over files, not for moving data.
- **Every project file must survive its next deploy.** Whether that means
  re-running cleanly or never changing after it ran depends on deploy.sql
  (point 2 above). Redeploy onto the existing database to prove it.
- **A project file is server-side SQL.** It runs through `EXECUTE`, so psql
  meta-commands (`\i`, `\set`, `\connect`) are syntax errors, and `COMMIT` or
  `ROLLBACK` inside it fails. Transaction boundaries belong in deploy.sql's
  top level; `pgmi ai` explains which statements share one.
- **Tag nested dollar quotes.** A function body inside a `DO` block, or SQL
  built inside a function, needs distinct tags (`$fn$`, `$sql$`); the first
  bare `$$` closes the outer block.
- **Follow the project's names.** Where it has no convention: singular
  snake_case table names (a table also names its row type, so `RETURNS
  account` reads right), and quote reserved words such as `"user"` everywhere.
- **Never print a parameter that may be a secret.** Parameters are session
  settings; a `RAISE NOTICE` of one lands in deploy output and CI logs.

## When a statement fails

1. Read the SQLSTATE and the file pgmi names. The pgmi-debug-deploy skill maps
   each SQLSTATE and exit code to its fix and says whether anything committed.
   Two are about how the SQL is written:
2. `42P13` cannot change return type: see [functions](references/functions.md).
3. `25001` cannot be executed from a function: the statement must move to the
   top level of deploy.sql, see [schema changes](references/schema-changes.md).
4. A file that never ran: deploy.sql does not select it, its extension is not
   a SQL extension, or discovery left it out (`--verbose` lists those).
