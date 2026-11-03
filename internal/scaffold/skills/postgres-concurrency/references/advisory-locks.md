# Allow one runner at a time with a transaction-scoped advisory lock

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A nightly rollup, a rebuild of a summary table, or the jobs of one tenant must
never run twice at once. A `running` flag in a table sticks when the process
dies, and locking a real table blocks unrelated readers and writers. An
advisory lock names the work, not a row: the database enforces "one holder"
and releases it when the holder's transaction ends.

## Use when
- At most one session at a time may run a piece of work, and the work fits
  in one transaction.
- Work must be serialised per key (per tenant, per account) without a lock
  row for every key.

## Don't use when
- The rule is about rows: lock the rows (`SELECT ... FOR UPDATE`) or let a
  constraint decide.
- Many workers should share a pool of jobs: use a queue with
  `FOR UPDATE SKIP LOCKED` (skip-locked-queue.md in this skill).

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE OR REPLACE FUNCTION refresh_daily_sales() RETURNS boolean
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT pg_try_advisory_xact_lock(hashtextextended('refresh_daily_sales', 0)) THEN
        RETURN false;
    END IF;
    -- the work; the lock is released when this transaction commits or aborts
    RETURN true;
END $$;
```

`pg_try_advisory_xact_lock` returns `false` at once when another session
holds the key; `pg_advisory_xact_lock` waits instead. Choose keys so they
cannot collide: `hashtextextended(name, 0)` maps a name to the bigint key
space; the two-integer form `(class, id)` gives each kind of work its own
`class` and is a separate key space from the bigint form. Session-level
`pg_advisory_lock` holds past the transaction, survives `ROLLBACK`, and stacks:
each lock call needs its own unlock. Use it only for work that spans several
transactions, and release it in every exit path.

## Proof
```sql runnable target=__test__/test_concurrency_advisory_locks.sql
CREATE FUNCTION pg_temp.held(p_key bigint) RETURNS boolean
LANGUAGE sql AS $$
    SELECT EXISTS (
        SELECT FROM pg_locks
        WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND granted
          AND ((classid::bigint << 32) | objid::bigint) = p_key AND objsubid = 1)
$$;

DO $$
DECLARE
    v_xact bigint := hashtextextended('recipe:xact', 0);
    v_session bigint := hashtextextended('recipe:session', 0);
BEGIN
    BEGIN
        IF NOT pg_try_advisory_xact_lock(v_xact) THEN
            RAISE EXCEPTION 'could not take the transaction-scoped lock';
        END IF;
        PERFORM pg_advisory_lock(v_session);
        IF NOT (pg_temp.held(v_xact) AND pg_temp.held(v_session)) THEN
            RAISE EXCEPTION 'locks not visible in pg_locks';
        END IF;
        RAISE division_by_zero;
    EXCEPTION WHEN division_by_zero THEN
        NULL;
    END;

    IF pg_temp.held(v_xact) THEN
        RAISE EXCEPTION 'the transaction-scoped lock outlived its rollback';
    END IF;
    IF NOT pg_temp.held(v_session) THEN
        RAISE EXCEPTION 'expected the session lock to survive the rollback';
    END IF;
    IF NOT pg_advisory_unlock(v_session) OR pg_temp.held(v_session) THEN
        RAISE EXCEPTION 'the session lock was not released by pg_advisory_unlock';
    END IF;
END $$;
```

The inner block's rollback releases the transaction-scoped lock and leaves
the session lock held until it is unlocked explicitly. A single session
cannot show the contention itself, since a session never blocks on its own
lock; verify "only one runner" with two connections.

## Pitfalls
- A session lock left behind by an error path blocks every later run until
  the connection closes, and a pooled connection may never close. Prefer the
  `_xact_` functions.
- Never call `pg_advisory_unlock_all()` in `deploy.sql`. pgmi itself holds a
  session-level advisory lock on `hashtextextended('pgmi.deploy.' ||
  current_database(), 0)` for the whole deploy to refuse concurrent deploys
  (exit code 15); unlocking everything drops it.
- A transaction-scoped lock taken in the head of `deploy.sql` is released by
  its first `COMMIT`; it does not guard the tail.
- `hashtext` returns a 32-bit hash, so unrelated names collide more often;
  use `hashtextextended`. Keys are scoped to the current database.

## Source
- PostgreSQL documentation: Explicit Locking, Advisory Locks,
  https://www.postgresql.org/docs/current/explicit-locking.html; Advisory Lock
  Functions, https://www.postgresql.org/docs/current/functions-admin.html.
- pgmi adaptation: the try-lock function, the key choice, the deploy-lock
  interaction, and the test that proves which lock a rollback releases.
