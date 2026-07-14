---
name: pgmi-debug-deploy
description: "Use when a pgmi deploy fails — map the exit code to what to inspect and how to fix it"
user_invocable: true
---

## Purpose

A failed `pgmi deploy` tells you three things: an **exit code**, an **error line**,
and (usually) a **SQLSTATE**. This skill maps them to a diagnosis. It is a lookup
table, not a tutorial — find your row, do the check, fix, redeploy.

## Start here

```bash
pgmi deploy ./project -d mydb; echo "exit=$?"
pgmi deploy ./project -d mydb --json      # same failure, machine-readable
```

The `--json` failure envelope carries what you need without re-reading the console:

```json
{ "status": "failed", "exitCode": 13, "sqlstate": "42601",
  "script": "deploy.sql", "line": 16, "column": 1, "sourceLine": "SELEC 1;",
  "scriptExpanded": true, "failedFile": "./migrations/002_data.sql" }
```

`scriptExpanded: true` means `line` refers to the script **after** `pgmi_test()`
macro expansion, not to the file on disk — the numbers legitimately differ.

## Exit code → diagnosis

| Exit | Meaning | First thing to check |
|------|---------|----------------------|
| **10** | Invalid configuration/parameters | A required `--param` is missing or malformed, or a flag combination is rejected. Read the message — it names the parameter. |
| **11** | Connection failed | Is the server up and reachable? `psql "$PGMI_CONNECTION_STRING" -c 'select 1'`. Check host/port/SSL and `PGPASSWORD`/`.pgpass`. pgmi never invents a connection. |
| **12** | User denied overwrite | You (or the approver) declined the DROP. Intentional. Re-run with `--force` only if you truly mean to destroy the database. |
| **13** | **SQL execution failed** | The overwhelmingly common one. Your SQL raised. See below. |
| **14** | `deploy.sql` not found | You pointed at the wrong directory. `deploy.sql` must sit at the **root** of the path you pass. |
| **15** | Concurrent deploy detected | Another pgmi run holds the advisory lock on this database. Wait, or find it: `SELECT * FROM pg_locks WHERE locktype = 'advisory'`. |
| **16** | Timed out | Exceeded `--timeout` (default 3m). Either the deploy is genuinely slow (raise it) or it is **blocked on a lock** — check `pg_stat_activity` for `wait_event_type = 'Lock'`. |
| **130** | Interrupted (Ctrl-C) | The transaction rolled back. Nothing was committed. |

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
| `25001` | Cannot run inside a transaction block | You used `CREATE DATABASE`, `CREATE INDEX CONCURRENTLY`, or `VACUUM` in `deploy.sql`, which runs inside a transaction. |
| `40001` / `40P01` | Serialization failure / deadlock | Transient. Retry the whole transaction from a fresh snapshot — never with a savepoint. |
| `P0001` | `RAISE EXCEPTION` | **Your own code, or a failing test.** The message is yours. Read it. |

### The failure is a test

If the message came from `__test__/`, the deploy did its job: the tests ran, one
failed, and the whole deployment refused to commit. Nothing was applied. Fix the
test or the code, then redeploy.

### "Which file failed?"

If your `deploy.sql` attributes failures per file (`RAISE EXCEPTION 'Failed in %: %'`),
`--json` surfaces it as `failedFile`. If it does not, add the wrapper — see the
`pgmi-sql` skill. Without it a failing migration only tells you *what* broke, not *where*.

### Dollar-quote closed early

A string containing `$$` inside a `$$ … $$` body terminates it. Symptom: a syntax
error at a line that looks perfectly valid, often near a regex containing `$`.
Fix: use a named tag — `$fn$ … $fn$`.

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
temporary probe to `deploy.sql`:

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
