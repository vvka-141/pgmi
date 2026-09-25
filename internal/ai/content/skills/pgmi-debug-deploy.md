---
name: pgmi-debug-deploy
description: "Use when a pgmi deploy fails — map the exit code to what to inspect and how to fix it"
scope: core
user_invocable: true
---

## Purpose

A failed `pgmi deploy` tells you three things: an **exit code**, an **error line**,
and (usually) a **SQLSTATE**. This skill maps them to a diagnosis. It is a lookup
table, not a tutorial — find your row, do the check, fix, redeploy.

## Start here

```bash
pgmi deploy ./project -d mydb; echo "exit=$?"
pgmi deploy ./project -d mydb --json 2>/dev/null   # machine-readable, no NOTICE stream
```

In an agent loop use `--json 2>/dev/null`: stdout carries only the result, and
the NOTICE stream on stderr (tens to hundreds of lines) never reaches your
context. The `--json` failure envelope carries what you need:

```json
{ "status": "failed", "exitCode": 13, "sqlstate": "42601",
  "failedFile": "./migrations/002_data.sql",
  "script": "./migrations/002_data.sql", "line": 4, "column": 1, "sourceLine": "SELEC 1;" }
```

For an error inside a project file, `script`/`line`/`column` point into that
file. For an error in deploy.sql itself `script` is `deploy.sql`, and
`scriptExpanded: true` means `line` refers to the script **after** `pgmi_test()`
macro expansion, not to the file on disk — the numbers legitimately differ.

## Exit code → diagnosis

| Exit | Meaning | First thing to check |
|------|---------|----------------------|
| **10** | Invalid pgmi configuration | pgmi rejected the invocation **before connecting**: no database name, a `--param` without `=`, an unparseable `pgmi.yaml`, a bad `--compat`, a missing `--params-file`, or conflicting cloud-auth flags. Nothing was deployed and no database was touched. A *missing* parameter that your SQL requires is **exit 13**, not this. |
| **11** | Connection failed | Is the server up and reachable? `psql "$PGMI_CONNECTION_STRING" -c 'select 1'`. Check host/port/SSL and `PGPASSWORD`/`.pgpass`. pgmi never invents a connection. |
| **12** | User denied overwrite | You (or the approver) declined the DROP. Intentional. Re-run with `--force` only if you truly mean to destroy the database. |
| **13** | **SQL execution failed** | The overwhelmingly common one. Your SQL raised. See below. |
| **14** | `deploy.sql` not found | You pointed at the wrong directory. `deploy.sql` must sit at the **root** of the path you pass. |
| **15** | Concurrent deploy detected | Another pgmi run holds the advisory lock on this database. Wait, or find it: `SELECT * FROM pg_locks WHERE locktype = 'advisory'`. |
| **16** | Timed out | Exceeded `--timeout` (default 3m). Either the deploy is genuinely slow (raise it) or it is **blocked on a lock** — check `pg_stat_activity` for `wait_event_type = 'Lock'`. |
| **130** | Interrupted (Ctrl-C) | The open transaction rolled back. If the interrupt came in the **atomic head**, nothing was committed; in the tail, earlier autocommitted units stay applied (see `executionMode` / `unitsCommitted` below). |

## Exit 13: SQL execution failed

pgmi reports the line of the statement PostgreSQL rejected:

```
pgmi: error: execution failed: ERROR: syntax error at or near "SELEC" (SQLSTATE 42601)
LOCATION: deploy.sql line 16, column 1 (of the expanded script: pgmi_test() macros shift line numbers)
LINE 16: SELEC 1;
         ^
```

Then read the SQLSTATE:

| SQLSTATE | Meaning | Usual cause in a deploy |
|----------|---------|-------------------------|
| `42601` | Syntax error | A typo, or a dollar-quote that closed early. See below. |
| `42P01` | Undefined table | Execution **order**. The file that creates it runs after the file that uses it — check `sortKeys` / path order in `pgmi_plan_view`. |
| `42883` | Undefined function | Same as above, or a missing extension. |
| `42703` | Undefined column | Typo, or a migration that was expected to have run but did not. |
| `23505` | Unique violation | A seed script is not idempotent. Use `ON CONFLICT DO NOTHING`. |
| `25001` … `cannot run inside a transaction block` | `CREATE DATABASE`, `CREATE INDEX CONCURRENTLY` or `VACUUM` ran in the **atomic head** | Move it below your first top-level `COMMIT`, where each statement autocommits. |
| `25001` … `cannot be executed from a function` | Same statements, but reached through `EXECUTE` — a plan loop inside a `DO` block | Same SQLSTATE, different fix: no transaction state helps. Write the statement at top level in the tail, not through the loop. |
| `25P01` … `SAVEPOINT can only be used in transaction blocks` | Your `CALL pgmi_test()` is not inside an **explicit** transaction | The macro expands to `SAVEPOINT`, and PostgreSQL refuses it in the implicit block of a multi-statement query. `SAVEPOINT` is nowhere in your file — pgmi put it there. Wrap the call: `BEGIN;` … `CALL pgmi_test();` … `COMMIT;` |
| `40001` / `40P01` | Serialization failure / deadlock | Transient. Retry the whole transaction from a fresh snapshot — never with a savepoint. |
| `42704` … `unrecognized configuration parameter "pgmi.x"` | A required parameter was not passed | `current_setting('pgmi.x')` without the `, true` second argument raises this when `--param x=…` is missing. Pass the parameter, or use `current_setting('pgmi.x', true)` with a default. |
| `P0002` … `pgmi: no tests were discovered` / `no tests matched the requested pattern` | `CALL pgmi_test()` found nothing to run | The pattern matched no file under `__test__/`, or the project has no tests. Patterns match project-relative paths such as `./__test__/test_users.sql`. |
| `P0001` | `RAISE EXCEPTION` | **Your own code, or a failing test.** The message is yours. Read it. A `missing required parameter` or `unknown parameter` lands here, not on exit 10 — the template raised it after pgmi connected. Add the `--param` it names. |

### The failure is a test

If the message came from `__test__/`, the deploy did its job: the tests ran, one
failed, and the whole deployment refused to commit. Nothing was applied.
`failedFile` names the test file (e.g. `./__test__/test_users.sql`), just as it
names a migration file for migration failures. Fix the test or the code, then
redeploy.

### "Which file failed?"

`failedFile` populates automatically. For migrations pgmi matches the text
PostgreSQL was executing against the loaded files, and `script`/`line`/`column`
point into the file for parse and analysis errors. For tests,
`pgmi_test_generate()` attributes each step. If `failedFile` is missing, a
handler in deploy.sql re-raised with `RAISE EXCEPTION`: drop it, or re-raise
with a bare `RAISE;` (see the `pgmi-sql` skill).

### Dollar-quote closed early

A string containing `$$` inside a `$$ … $$` body terminates it. Symptom: a syntax
error at a line that looks perfectly valid, often near a regex containing `$`.
Fix: use a named tag — `$fn$ … $fn$`.

## Did anything apply?

A failed deploy with `--json` now tells you whether the database is clean or
half-migrated via `executionMode` and `unitsCommitted`:

```json
{ "executionMode": "atomic", "executionUnits": 3, "unitsCommitted": 0 }
```

| `executionMode` | `unitsCommitted` | Database state |
|-----------------|------------------|----------------|
| `"atomic"` | `0` | **Clean.** The failure was in the atomic head — the transaction rolled back, nothing was applied. Safe to fix and redeploy. |
| `"psql"` | N > 0 | **Partially applied.** N earlier tail units already committed (autocommit). The database has changes from those units. Write tail statements idempotently so a redeploy converges. |

When `executionMode` is absent (pre-execution failure, e.g. exit 10/11/14), the
database was never touched.

The human summary also reports this on a tail failure:

```
✗ mydb: failed after 0.72s (unit 3 of 4 failed; 2 earlier unit(s) already committed)
```

## Nothing failed, but nothing happened

The deploy exits 0 and the database is unchanged:

* **The plan is empty.** `SELECT count(*) FROM pg_temp.pgmi_plan_view` inside
  `deploy.sql`. A `WHERE` clause that matches no files (a wrong `directory`
  prefix — it has a **trailing slash**: `'./migrations/'`) silently does nothing.
* **One-time scripts already ran.** With `idempotent="false"`, a script that has
  already executed is skipped by design.
* **Your `deploy.sql` never executed anything.** pgmi loads files and hands over;
  it does not run your SQL for you. Something must `EXECUTE` the plan.

## Reproducing what pgmi saw

The session is ordinary PostgreSQL. To inspect the plan without deploying, add a
temporary probe to `deploy.sql`. It lists every loaded file, not only SQL — if
you see `README.md` or a `001.sql~` backup here, that is expected, and it is
exactly why an execute loop must filter on `is_sql_file`:

```sql
DO $$
DECLARE r record;
BEGIN
    FOR r IN SELECT execution_order, path FROM pg_temp.pgmi_plan_view ORDER BY execution_order
    LOOP
        RAISE NOTICE '% %', r.execution_order, r.path;
    END LOOP;
END $$;
```

`--verbose` raises `client_min_messages` to `debug`, so `RAISE DEBUG` in your own
SQL becomes visible.

## See Also

- `pgmi-sql` — writing `deploy.sql`, dollar-quoting, per-file error attribution
- `pgmi-testing-review` — when the failure is a test
- `pgmi-metadata-system` — when the failure is execution order
