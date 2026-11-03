# Build an index on a live table with CREATE INDEX CONCURRENTLY

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A plain `CREATE INDEX` holds a `SHARE` lock on the table for the whole build:
reads continue, every insert, update and delete waits. On a large table that
is an outage. `CONCURRENTLY` takes `SHARE UPDATE EXCLUSIVE` instead and lets
writes continue, but it cannot run inside a transaction block or a function.
In a pgmi project every plan file runs through `EXECUTE` inside `deploy.sql`,
so the statement fails there with `25001`, even after a `COMMIT`.

## Use when
- The table takes writes during the deploy and the build outlasts them.

## Don't use when
- The table is new in this deploy or small: a plain `CREATE INDEX` in a plan
  file is atomic with the rest of the change and builds in milliseconds.
- The index must exist before later statements of the same atomic head
  (a unique index the next migration's upsert relies on): build it in the
  head, or split the change across two deploys.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
The statement goes at top level in `deploy.sql`, after its first top-level
`COMMIT`, where each statement autocommits as in psql. Follow the project's
own policy if `deploy.sql` has one: the advanced template's tail fails the
deploy on any invalid index, so put builds before that check and add no reap
block. Otherwise drop the wreckage of a failed build, then create if absent:

```sql
COMMIT;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_index
               WHERE indexrelid = to_regclass('public.event_occurred_at_idx')
                 AND NOT indisvalid) THEN
        DROP INDEX public.event_occurred_at_idx;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS event_occurred_at_idx
    ON public.event (occurred_at);
```

## Proof
```sql runnable kind=tail
CREATE SCHEMA IF NOT EXISTS indexing_proof;
CREATE TABLE IF NOT EXISTS indexing_proof.event (
    event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO indexing_proof.event (occurred_at) SELECT now() FROM generate_series(1, 100);

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_index
               WHERE indexrelid = to_regclass('indexing_proof.event_occurred_at_idx')
                 AND NOT indisvalid) THEN
        DROP INDEX indexing_proof.event_occurred_at_idx;
    END IF;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS event_occurred_at_idx
    ON indexing_proof.event (occurred_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS event_occurred_at_idx
    ON indexing_proof.event (occurred_at);

DO $$
DECLARE
    v_valid boolean;
BEGIN
    SELECT indisvalid INTO v_valid FROM pg_index
    WHERE indexrelid = to_regclass('indexing_proof.event_occurred_at_idx');
    IF v_valid IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'concurrent index missing or invalid: indisvalid = %', v_valid;
    END IF;

    BEGIN
        EXECUTE 'CREATE INDEX CONCURRENTLY event_id_idx ON indexing_proof.event (event_id)';
        RAISE EXCEPTION 'CREATE INDEX CONCURRENTLY ran inside a function';
    EXCEPTION WHEN active_sql_transaction THEN
        NULL;
    END;
END $$;

DROP SCHEMA indexing_proof CASCADE;
```

## Pitfalls
- A failed or cancelled build (a duplicate for a unique index, a deadlock,
  pgmi's `--timeout`) leaves an index that `pg_index.indisvalid` reports
  false: ignored by queries, still maintained on every write, and skipped by
  `IF NOT EXISTS`, which matches on the name alone. Find them with
  `SELECT indexrelid::regclass FROM pg_index WHERE NOT indisvalid`.
- Reap by name, never every invalid index: a build running in another
  session is invalid until it finishes.
- The build waits for every transaction that has written to the table, then
  for old snapshots; one long transaction that wrote the table or holds an
  old snapshot stalls it. Watch
  `pg_stat_progress_create_index` and size `--timeout` for the table.
- `DROP INDEX` in the reap block takes `ACCESS EXCLUSIVE` briefly. To avoid
  even that, run `DROP INDEX CONCURRENTLY` by hand, or rebuild in place with
  `REINDEX INDEX CONCURRENTLY`.
- A tail statement autocommits and stays applied when a later one fails:
  write each to run again safely.

## Source
- PostgreSQL documentation: CREATE INDEX, Building Indexes Concurrently,
  https://www.postgresql.org/docs/current/sql-createindex.html; Explicit
  Locking, https://www.postgresql.org/docs/current/explicit-locking.html;
  Progress Reporting,
  https://www.postgresql.org/docs/current/progress-reporting.html.
- pgmi adaptation: placement after deploy.sql's first COMMIT, the reap-then-
  create idiom for re-runs, deferral to the project's own invalid-index
  check, the tail test that proves the build, its re-run and its refusal
  inside a function.
