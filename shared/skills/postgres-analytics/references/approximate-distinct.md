# Count distinct values at scale with HyperLogLog rollups

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
"Unique visitors per day, per week, per month" over a large event table.
Exact `count(DISTINCT user_id)` deduplicates every value it reads, never runs
as a parallel aggregate, and does not roll up: daily distinct counts do not
add to the weekly one, since a visitor seen on two days counts twice, so each
period rescans the raw events. A HyperLogLog sketch holds a period's set in
about 1.3 kB, unions with other sketches, and estimates within a few percent.

## Use when
- Rollup tables must answer distinct counts for any range of periods or
  segments, over data too large to rescan, and a small error is acceptable.

## Don't use when
- The number must be exact (billing, quotas, audits), or the exact query is
  fast enough: write `SELECT count(*) FROM (SELECT DISTINCT ...)`, which the
  planner can hash and parallelize, unlike `count(DISTINCT ...)`.

## Requires
- PostgreSQL 15+.
- Extension `hll` (postgresql-hll, third-party, from Citus): not part of core
  PostgreSQL and not in the stock `postgres` Docker image. Check that your
  provider and major version offer it.
- Server settings: none.

## Pattern
```sql
CREATE EXTENSION IF NOT EXISTS hll;
CREATE TABLE daily_visitor (day date PRIMARY KEY, visitors hll NOT NULL);

INSERT INTO daily_visitor (day, visitors)
SELECT (viewed_at AT TIME ZONE 'UTC')::date, hll_add_agg(hll_hash_bigint(user_id))
FROM page_view WHERE viewed_at >= current_date - 1 GROUP BY 1
ON CONFLICT (day) DO UPDATE
SET visitors = hll_union(daily_visitor.visitors, EXCLUDED.visitors);

SELECT hll_cardinality(hll_union_agg(visitors)) AS visitors
FROM daily_visitor WHERE day BETWEEN '2026-03-02' AND '2026-03-08';
```

## Proof
The exact baseline, which needs no extension: distinct counts do not add up.

```sql runnable target=__test__/test_analytics_approximate_distinct.sql
CREATE TEMP TABLE page_view (user_id bigint NOT NULL, day int NOT NULL);
INSERT INTO page_view VALUES (1, 1), (1, 1), (2, 1), (3, 1), (2, 2), (3, 2), (4, 2), (1, 3), (5, 3);
DO $$
DECLARE
    v_diff int;
    v_week bigint;
    v_week_subquery bigint;
    v_sum_of_days numeric;
BEGIN
    WITH expected (day, visitors) AS (
        VALUES (1, 3::bigint), (2, 3), (3, 2)
    ), actual AS (
        SELECT day, count(DISTINCT user_id) AS visitors FROM page_view GROUP BY day
    )
    SELECT count(*) INTO v_diff FROM (
        (SELECT * FROM expected EXCEPT SELECT * FROM actual) UNION ALL
        (SELECT * FROM actual EXCEPT SELECT * FROM expected)
    ) d;
    IF v_diff IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'daily distinct counts differ from the expected rows in % row(s)', v_diff;
    END IF;
    SELECT count(DISTINCT user_id) INTO STRICT v_week FROM page_view;
    SELECT count(*) INTO STRICT v_week_subquery FROM (SELECT DISTINCT user_id FROM page_view) u;
    SELECT sum(n) INTO STRICT v_sum_of_days
    FROM (SELECT count(DISTINCT user_id) AS n FROM page_view GROUP BY day) d;
    IF v_week IS DISTINCT FROM 5::bigint OR v_week_subquery IS DISTINCT FROM 5::bigint THEN
        RAISE EXCEPTION 'weekly distinct visitors: % and %, expected 5', v_week, v_week_subquery;
    END IF;
    IF v_sum_of_days IS DISTINCT FROM 8::numeric THEN
        RAISE EXCEPTION 'sum of daily distinct counts is %, expected 8 (not the weekly 5)', v_sum_of_days;
    END IF;
END $$;
```

The sketch rollup (skipped, with the reason logged, on a server without `hll`):

```sql runnable target=__test__/test_analytics_approximate_distinct_hll.sql requires=hll
CREATE EXTENSION IF NOT EXISTS hll;
CREATE TEMP TABLE page_view (user_id bigint NOT NULL, day int NOT NULL);
INSERT INTO page_view VALUES (1, 1), (1, 1), (2, 1), (3, 1), (2, 2), (3, 2), (4, 2), (1, 3), (5, 3);
CREATE TEMP TABLE daily_visitor (day int PRIMARY KEY, visitors hll NOT NULL);
DO $$
DECLARE v_week double precision;
BEGIN
    FOR i IN 1..2 LOOP
        INSERT INTO daily_visitor (day, visitors)
        SELECT day, hll_add_agg(hll_hash_bigint(user_id)) FROM page_view GROUP BY day
        ON CONFLICT (day) DO UPDATE
        SET visitors = hll_union(daily_visitor.visitors, EXCLUDED.visitors);
    END LOOP;
    SELECT hll_cardinality(hll_union_agg(visitors)) INTO STRICT v_week FROM daily_visitor;
    IF v_week IS NULL OR abs(v_week - 5) >= 0.5 THEN
        RAISE EXCEPTION 'weekly sketch estimates % visitors, expected 5 after a re-run rollup', v_week;
    END IF;
END $$;
```

## Pitfalls
- Hash every input with the `hll_hash_*` function for its type, with the
  same seed everywhere: sketches hashed differently union into garbage.
- Small sets are stored exactly; past that the estimate carries a relative
  error of about 1.04/sqrt(2^log2m), roughly 2% at the default `log2m = 11`.
  Never compare an estimate for equality in production code.
- Re-running a sketch rollup is idempotent; re-running a rollup of exact
  counts adds them twice.
- A sketch cannot be filtered after the fact: roll up per segment you will
  query (day, day and country) and union at read time.

## Source
- PostgreSQL documentation: Aggregate Functions,
  https://www.postgresql.org/docs/current/functions-aggregate.html; Parallel
  Aggregation, https://www.postgresql.org/docs/current/parallel-plans.html#PARALLEL-AGGREGATION.
- pgmi adaptation: the visitor rollup, the baseline test that shows distinct
  counts do not add, and the sketch test that survives a re-run.
