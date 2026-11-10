# Transaction design in deploy.sql

## Contents

- One transaction, gated by the tests
- Phases after the first COMMIT
- Concurrent indexes
- Lock and statement limits
- Branching on parameters
- What a failure leaves behind

The execution contract itself (what counts as the first `COMMIT`, how the
tail runs, what pgmi does with an unterminated `BEGIN`) is in `pgmi ai`. Read
it there before relying on an edge case. This file is about the design
choices that follow from it.

## One transaction, gated by the tests

The default shape. Both templates start from it; only the advanced template
also sets `lock_timeout`, so add it to a basic project's `deploy.sql` before
altering a live table:

```sql
BEGIN;
SET LOCAL lock_timeout = '5s';

-- the file loop(s): see this project's deploy.sql for which files and order

SAVEPOINT _tests;
CALL pgmi_test();
ROLLBACK TO SAVEPOINT _tests;

-- preview gate, if the project has one

COMMIT;
```

- `CALL pgmi_test()` expands to `SAVEPOINT` statements, which PostgreSQL
  refuses outside an explicit transaction block. Keep it after a top-level
  `BEGIN`.
- A test failure aborts the transaction, so nothing the deploy did lands.
  That is the gate: put anything that must not ship untested before the
  `COMMIT`.
- One transaction holds every lock it took until the `COMMIT`. On a busy
  database, keep the head short and fast; move long data work to its own
  phase.

## Phases after the first COMMIT

After the first top-level `COMMIT`, each top-level statement autocommits.
Statements there are not grouped implicitly. A phase that must be atomic
says so with its own `BEGIN ... COMMIT`:

```sql
COMMIT;                           -- end of the gated head

BEGIN;
UPDATE account SET email_normalized = lower(email)
WHERE email_normalized IS NULL;
COMMIT;
```

A failure in a later phase stops the deploy but keeps every phase that
already committed. Write each one so a rerun converges (the `WHERE ... IS
NULL` above), then fix and redeploy.

Do not reach for procedures to get per-file commits in the head. `COMMIT`
raises `invalid transaction termination` in a `DO` block or a `CALL`ed
procedure whenever a transaction block surrounds it, and in the head one
always does.

Moving work after the first `COMMIT` takes it out from under the test gate.
Do it only for work that cannot run inside the transaction or is too large
for one.

## Concurrent indexes

`CREATE INDEX CONCURRENTLY` cannot run inside a transaction block, and it is
refused from any function, so it never goes through `EXECUTE` in a file
loop, even after a `COMMIT`. Write it in `deploy.sql` at top level, after the
first `COMMIT`, one statement each. `VACUUM` and `CREATE DATABASE` follow the
same rule.

A failed concurrent build leaves an `INVALID` index, which `IF NOT EXISTS`
then skips forever. Check whether this project's `deploy.sql` already has a
policy for invalid indexes and follow it. Without one, reap only the named
wreckage, then create if absent:

```sql
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_index
        WHERE indexrelid = to_regclass('idx_user_email') AND NOT indisvalid
    ) THEN
        DROP INDEX idx_user_email;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_email ON users(email);
```

Name the index rather than sweeping every invalid one: a concurrent build in
another session is also `INVALID` until it finishes.

`--timeout` covers the whole deploy, these statements included. A build that
outlives it is cancelled and leaves the index `INVALID`; raise `--timeout`
for large tables.

## Lock and statement limits

Set limits in SQL, where they apply to the statements they are meant for:

- `SET LOCAL lock_timeout` right after `BEGIN`: a DDL statement that cannot
  get its lock fails fast instead of queueing every application query behind
  it. `SET LOCAL` ends with the transaction.
- `SET statement_timeout` around a known long statement, `RESET` after it.

`--timeout` is a safety net for the whole run, not a substitute for these.

Concurrent pgmi deploys to the same database are already excluded by pgmi.
That does not exclude other tools or people changing the schema.

## Branching on parameters

Every conditional behaviour (environment, preview, skipping a phase) is an
`IF` on a parameter inside `deploy.sql`:

```sql
DO $$
DECLARE
    v_env text := current_setting('pgmi.env', true);
BEGIN
    IF v_env IS NULL OR v_env NOT IN ('dev', 'test', 'prod') THEN
        RAISE EXCEPTION 'Parameter env must be dev, test or prod, got %', coalesce(quote_literal(v_env), 'nothing')
            USING HINT = 'Pass --param env=dev, test or prod; there is no default.';
    END IF;
    IF v_env = 'dev' THEN
        -- development-only seed data
    END IF;
END $$;
```

If this project declares its parameters (a defaults file, a helper function,
another setting prefix), use that instead of reading `pgmi.<key>` directly.

## What a failure leaves behind

| Failure in | What stays applied |
|---|---|
| The head (before the first `COMMIT`) | Nothing |
| An explicit `BEGIN ... COMMIT` after it | Everything committed before that block |
| An autocommitted statement after it | Everything before that statement |

The deploy stops at the first failure either way. Do not wrap a file in an
exception handler to keep going: the deploy would report success over a
broken file. If a handler is needed for another reason, end it with a bare
`RAISE;`, which keeps the original error, its SQLSTATE and its position, so
pgmi can still name the failing file and line.
