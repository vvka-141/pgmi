---
name: pgmi-debug-deploy
description: Diagnoses a failed pgmi deploy in a pgmi project. Maps the exit code (10 invalid configuration, 11 connection, 12 overwrite refused, 13 SQL failed, 14 no deploy.sql, 15 concurrent deploy, 16 timeout, 130 interrupted) and the SQLSTATE to what to inspect and how to fix it, reads the --json failure record, and tells whether anything was committed. Use when pgmi deploy exits non-zero, when a test fails the deploy, when an error names no file, or when a deploy succeeds but changes nothing.
---

# Debugging a failed deploy

A failed deploy gives an exit code, an error message and, for SQL errors, a
SQLSTATE. Find the row, check the cause, fix it, redeploy. The edit, deploy
and fix loop itself is in the pgmi-workflow skill.

## Run it so the failure is readable

```bash
pgmi deploy . -d mydb --json 2>/dev/null | jq 'del(.notices, .ignoredPaths)'; echo "exit=${PIPESTATUS[0]}"
```

`--json` writes one record to stdout. The NOTICE stream goes to stderr, so
`2>/dev/null` keeps hundreds of progress lines out of your context; the record
still carries the last notices in `notices`, which the `jq` drops until you
need them. The fields that matter on failure:

| Field | Meaning |
|---|---|
| `exitCode`, `error`, `sqlstate` | What failed |
| `failedFile` | The file that failed: the project or test file PostgreSQL was executing, or `deploy.sql` |
| `phase` | `test` when the failure is in a test or fixture, `deploy` otherwise |
| `failedFileCandidates` | Present when several SQL files hold that same text: a copy is loaded too, and any of them may be the one that ran |
| `script`, `line`, `column`, `sourceLine` | Where, when PostgreSQL gave a position |
| `where`, `detail`, `hint` | PostgreSQL's own context fields |
| `executionMode`, `unitsCommitted` | Whether anything was committed (below) |
| `notices` | The deploy's last NOTICE, WARNING and test lines, the ones just before the failure |

Where the location comes from:

- A parse or analysis error (syntax, missing table or column) always has a
  `line`, pointing into the file named by `script`, a typo inside a function
  body included.
- A runtime error (division by zero, a constraint, `RAISE EXCEPTION`) inside
  a project or test file has a line when the file is a DO block, as tests
  are, comment header or not; otherwise `failedFile` names the file without one. `where` names the
  file rather than quoting it.
- An error in `deploy.sql` has `script: "deploy.sql"` and the line of the file
  on disk, even where `CALL pgmi_test()` was expanded. A runtime error before
  the first top-level `COMMIT` has a line only when that part holds a single
  DO block; after it, always, with `column` 0.

## Exit codes

`pgmi help deploy` prints the authoritative table for the installed binary.

| Exit | Meaning | Check |
|---|---|---|
| 1 | Unclassified error | Read the message |
| 2 | Usage error | Wrong arguments or flags. `pgmi <command> --help` |
| 3 | Internal panic | A pgmi bug. Keep the output and report it |
| 10 | Invalid configuration | pgmi rejected the project or the invocation. Nothing was deployed. See [references/exit-10.md](references/exit-10.md) |
| 11 | Connection failed | Server up and reachable? `pg_isready -h <host> -p <port>`. Then host, port, SSL mode, and the password in `PGPASSWORD`, `.pgpass` or the connection string |
| 12 | Overwrite not approved | `--overwrite` without a terminal and without `--force`, or the prompt was declined. Add `--force` only if the database may be destroyed |
| 13 | SQL execution failed | Your SQL or a test raised. See below |
| 14 | `deploy.sql` not found | The path you passed must be the project root, with `deploy.sql` directly in it |
| 15 | Concurrent deploy | Another pgmi deploy holds the lock on this database. Wait for it, or find it (below) |
| 16 | `--timeout` exceeded | Slow, or blocked on a lock. `failedFile` names the file that was running. Look for `wait_event_type = 'Lock'` in `pg_stat_activity` before raising `--timeout` |
| 130 | Interrupted | Ctrl-C or SIGTERM (a CI cancel). Check `executionMode` to see what stayed applied |

A parameter your SQL requires but did not get is not exit 10: `deploy.sql`
raises it after connecting, so it is 13.

To find the deploy holding exit 15's lock, run this in the target database,
or for an `--overwrite` deploy in the maintenance database, where the
overwrite lock lives:

```sql
SELECT a.pid, a.application_name, a.client_addr, a.state, a.query_start
FROM pg_locks AS l JOIN pg_stat_activity AS a USING (pid)
WHERE l.locktype = 'advisory' AND l.granted AND l.database = (
    SELECT oid FROM pg_database WHERE datname = current_database());
```

## Exit 13: read the SQLSTATE

| SQLSTATE | Usual cause | Fix |
|---|---|---|
| `42601` syntax error | A typo, or a dollar quote closed early: a `$$` inside a `$$ ... $$` body ends it, often in a regex | Use a named tag: `$fn$ ... $fn$` |
| `42P01` undefined table, `42883` undefined function | Order: the file that uses it runs before the file that creates it. Or a missing extension | Check the order `deploy.sql` runs files in. If it follows `pgmi_plan_view`, `pgmi metadata plan .` shows it (see the pgmi-metadata skill) |
| `42703` undefined column | A typo, or a change that has not run | Check the file that should have added it ran |
| `23505` unique violation | A seed is not re-runnable | Seed through the writer, which returns NULL instead of inserting a duplicate (as the basic template seeds `Demo`); without writers, `ON CONFLICT DO NOTHING` or `DO UPDATE` |
| `25001` ... cannot run inside a transaction block | `CREATE INDEX CONCURRENTLY`, `VACUUM` or `CREATE DATABASE` before the first top-level `COMMIT`, written directly or run through `EXECUTE` | Move it below that `COMMIT` as a top-level statement, where each statement autocommits |
| `25001` ... cannot be executed from a function | The same statements after the first `COMMIT`, but run through `EXECUTE` in a loop or a `DO` block | Write it as a top-level statement |
| `25P01` SAVEPOINT can only be used in transaction blocks | `CALL pgmi_test()` outside an explicit transaction. The macro expands to `SAVEPOINT` | Put it between `BEGIN;` and `COMMIT;` |
| `42704` unrecognized configuration parameter "pgmi.x" | `current_setting('pgmi.x')` without the second argument, and `--param x=...` not passed | Pass it, or use `current_setting('pgmi.x', true)` with a default |
| `P0002` pgmi: no tests were discovered / matched | `CALL pgmi_test()` found nothing to run | Tests are SQL files under `__test__/`; a pattern matches paths like `./__test__/test_x.sql` |
| `22023` | The template's `deploy.sql` refused a missing or malformed parameter (`env`, a password) | Set `PGMI_PARAM_<NAME>`, or `--param` for non-secrets |
| `P0001` | `RAISE EXCEPTION`: your code or a failing test | The message is the project's. Read it |
| `40001`, `40P01` | Serialization failure, deadlock | Transient. Redeploy |

### The failure is a test

`phase` is `test` and `failedFile` names the test file. The deploy did its job: the test ran and
refused the commit. Fix the code or the test, then redeploy. Whether anything
else was applied depends on where `CALL pgmi_test()` sits; check
`executionMode`.

### No file named

First, the error may be in `deploy.sql` itself: `failedFile` and `script` are
`deploy.sql` (a parameter read with `current_setting('pgmi.x')` without
`, true`, a typo in a DO block). Otherwise `failedFile` is empty because an exception handler in
`deploy.sql` caught the
error and raised a new one, losing the text PostgreSQL was executing. Remove
the handler, or re-raise with a bare `RAISE;`.

## Did anything apply?

| `executionMode` | `unitsCommitted` | Database |
|---|---|---|
| `atomic` | 0 | Unchanged. The failure was before the first top-level `COMMIT` and everything rolled back |
| `psql` | N | The failure was after the first top-level `COMMIT`. N units committed: everything through that `COMMIT`, then each tail statement before the failing one. Redeploying runs them again, so they must be re-runnable |
| absent | | `deploy.sql` never started. Nothing ran |

Two fields change that answer. pgmi makes the database before `deploy.sql`
runs, so a rollback does not undo it:

- `recreated: true`: `--overwrite` dropped and recreated the database. Its
  old data is gone; it holds what the table says this deploy committed.
- `created: true`: the database did not exist and pgmi created it. It holds
  what the table says this deploy committed, and is still there after the
  failure.

## Succeeded, but nothing changed

- The loop in `deploy.sql` matched no files. `directory` values end in a
  slash: `'./migrations/'`, not `'./migrations'`.
- `deploy.sql` skipped files it had recorded as already run. Look for its
  tracking table.
- The file is not loaded. Paths under a directory starting with `.`, under
  `node_modules/` or `__pycache__/`, or matched by `.pgmiignore` never reach
  the session. `--verbose` without `--json` lists every ignored path and
  the rule that excluded it.

To see what the session holds, add a temporary probe to `deploy.sql`:

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

It lists every loaded file, `README.md` included; that is expected.
`--verbose` sets `client_min_messages` to `debug` once pgmi's session setup
is done, so `RAISE DEBUG` lines in your SQL show on stderr.
