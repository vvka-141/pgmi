# Run a work queue in a table with FOR UPDATE SKIP LOCKED

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
Several workers take jobs from one table. Reading `WHERE status = 'pending'
LIMIT 10` and then updating those rows lets two workers claim the same rows.
`FOR UPDATE` stops the duplicates but queues every worker behind the first
one's locks. `FOR UPDATE SKIP LOCKED` passes over rows another worker holds
and takes the next free ones, in one statement that also marks them claimed.

## Use when
- Many workers consume jobs from a table, each job processed once.

## Don't use when
- Strict global order matters (each job after the previous one finished): a
  single consumer, or an advisory lock per ordering key (advisory-locks.md
  in this skill).

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE job (
    job_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'done', 'failed')),
    run_after timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz,
    attempts int NOT NULL DEFAULT 0
);
CREATE INDEX job_pending ON job (run_after, job_id) WHERE status = 'pending';
```

The claim, committed on its own before the work starts:

```sql
WITH next AS (
    SELECT job_id FROM job
    WHERE status = 'pending' AND run_after <= now()
    ORDER BY run_after, job_id
    LIMIT 10
    FOR UPDATE SKIP LOCKED
)
UPDATE job j
SET status = 'running', claimed_at = clock_timestamp(), attempts = j.attempts + 1
FROM next WHERE j.job_id = next.job_id
RETURNING j.job_id, j.payload;
```

The worker marks each job `done` or `failed` in its own short transaction. A
periodic reaper returns `running` jobs whose `claimed_at` is older than the
lease to `pending`: their worker died.

## Proof
```sql runnable target=__test__/test_concurrency_skip_locked_queue.sql
CREATE TEMP TABLE job (
    job_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payload jsonb NOT NULL, status text NOT NULL DEFAULT 'pending',
    run_after timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz, attempts int NOT NULL DEFAULT 0
);
CREATE INDEX job_pending ON job (run_after, job_id) WHERE status = 'pending';
CREATE FUNCTION pg_temp.claim(p_limit int) RETURNS SETOF bigint
LANGUAGE sql AS $$
    WITH next AS (
        SELECT job_id FROM job
        WHERE status = 'pending' AND run_after <= now()
        ORDER BY run_after, job_id LIMIT p_limit
        FOR UPDATE SKIP LOCKED
    )
    UPDATE job j SET status = 'running', claimed_at = clock_timestamp(),
                     attempts = j.attempts + 1
    FROM next WHERE j.job_id = next.job_id
    RETURNING j.job_id
$$;

DO $$
DECLARE v_first bigint[]; v_second bigint[];
BEGIN
    INSERT INTO job (payload, run_after)
    SELECT jsonb_build_object('n', n), now() + make_interval(mins => n - 5)
    FROM generate_series(1, 6) n;

    SELECT array_agg(c ORDER BY c) INTO v_first FROM pg_temp.claim(3) c;
    SELECT array_agg(c ORDER BY c) INTO v_second FROM pg_temp.claim(3) c;
    IF v_first IS DISTINCT FROM '{1,2,3}'::bigint[] OR v_second IS DISTINCT FROM '{4,5}'::bigint[] THEN
        RAISE EXCEPTION 'claims were % then %, expected {1,2,3} then {4,5}', v_first, v_second;
    END IF;
    IF EXISTS (SELECT FROM job WHERE job_id = 6 AND status IS DISTINCT FROM 'pending') THEN
        RAISE EXCEPTION 'a job scheduled for the future was claimed';
    END IF;
END $$;
```

This proves the claim takes the oldest due jobs, never re-claims a running
one, and leaves future jobs alone. It cannot show the skipping: a test runs
in one session, which never conflicts with its own row locks.

## Pitfalls
- Processing a job inside the claiming transaction holds its lock and an open
  transaction for the whole job, which holds back vacuum. Commit the claim
  first; recover crashed workers with the lease.
- Order is a preference, not a guarantee: a worker that commits late finishes
  an older job after a newer one.
- Without the partial index every claim scans the finished history. The
  index predicate must match the claim's `WHERE status = 'pending'`.
- Delete or archive `done` rows; a queue table that only grows slows down.
- Under `REPEATABLE READ` or `SERIALIZABLE`, a claim that meets a row changed
  since its snapshot fails with `40001`. Run claims in `READ COMMITTED`.

## Source
- PostgreSQL documentation: SELECT, The Locking Clause,
  https://www.postgresql.org/docs/current/sql-select.html; Explicit Locking,
  Row-Level Locks, https://www.postgresql.org/docs/current/explicit-locking.html;
  Partial Indexes, https://www.postgresql.org/docs/current/indexes-partial.html.
- pgmi adaptation: the job schema, the claim with its lease, and the test
  that proves claim order and exclusivity in one session.
