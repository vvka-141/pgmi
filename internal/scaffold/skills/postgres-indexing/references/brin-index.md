# Index a large append-only table by time with a BRIN index

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
An events, readings or audit table grows by inserts in time order and is
queried by time range ("last week of readings"). A B-tree on the timestamp
works but grows with every row, often to a large fraction of the table, and
every insert pays to maintain it. The rows are already stored roughly in
time order, so most of that index restates what the table's physical layout
says for free.

## Use when
- The table is large (many gigabytes) and mostly appended; rows are rarely
  updated or deleted.
- The indexed column follows physical order: `pg_stats.correlation` close to
  1 (or -1) after `ANALYZE`.
- Queries select ranges (`recorded_at >= $1 AND recorded_at < $2`) that cover
  many rows, not single rows.

## Don't use when
- Correlation is low (random keys, frequently updated rows, bulk loads in
  arbitrary order): every block range spans every value and the index skips
  nothing. Use a B-tree.
- Queries fetch one row or a few by exact value: a B-tree answers directly;
  BRIN always reads whole block ranges.
- The rule is uniqueness: BRIN cannot back a unique index.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE reading (
    sensor_id   bigint NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    value       double precision NOT NULL
);

CREATE INDEX reading_recorded_at_brin
    ON reading USING brin (recorded_at)
    WITH (pages_per_range = 32, autosummarize = on);

SELECT attname, correlation FROM pg_stats
WHERE schemaname = 'public' AND tablename = 'reading';
```

`pages_per_range` (default 128): fewer pages per range means a larger index
that skips more precisely. Lower it when a typical query covers few ranges.

## Proof
```sql runnable target=__test__/test_indexing_brin_index.sql
CREATE TEMP TABLE reading (
    recorded_at timestamptz NOT NULL,
    sensor_id bigint NOT NULL
);
INSERT INTO reading
SELECT timestamptz '2026-01-01 00:00Z' + i * interval '1 minute', (i * 7919) % 10000
FROM generate_series(1, 10000) AS i;
ANALYZE reading;
CREATE INDEX reading_recorded_at_brin ON reading USING brin (recorded_at)
    WITH (pages_per_range = 32);

DO $$
DECLARE
    v_time real;
    v_sensor real;
    v_am name;
    v_options text[];
BEGIN
    SELECT max(correlation) FILTER (WHERE attname = 'recorded_at'),
           max(correlation) FILTER (WHERE attname = 'sensor_id')
    INTO v_time, v_sensor
    FROM pg_stats
    WHERE schemaname::regnamespace = pg_my_temp_schema() AND tablename = 'reading';
    IF v_time IS NULL OR v_time < 0.99 THEN
        RAISE EXCEPTION 'recorded_at correlation is %, expected close to 1', v_time;
    END IF;
    IF v_sensor IS NULL OR abs(v_sensor) > 0.1 THEN
        RAISE EXCEPTION 'sensor_id correlation is %, expected close to 0', v_sensor;
    END IF;

    SELECT am.amname, c.reloptions INTO STRICT v_am, v_options
    FROM pg_class c JOIN pg_am am ON am.oid = c.relam
    WHERE c.oid = 'reading_recorded_at_brin'::regclass;
    IF (v_am, v_options) IS DISTINCT FROM ('brin'::name, '{pages_per_range=32}'::text[]) THEN
        RAISE EXCEPTION 'index is % with %, expected brin with pages_per_range=32', v_am, v_options;
    END IF;

    BEGIN
        CREATE UNIQUE INDEX reading_sensor_brin ON reading USING brin (sensor_id);
        RAISE EXCEPTION 'a unique BRIN index was accepted';
    EXCEPTION WHEN feature_not_supported THEN
        NULL;
    END;
END $$;
```

## Pitfalls
- New block ranges are not summarized until vacuum, `autosummarize`, or
  `brin_summarize_new_values()` runs; an unsummarized range is always read,
  so queries on the newest data scan the tail of the table.
- Updates, deletes followed by reuse of free space, and backfills of old
  timestamps break the physical order. Re-check correlation after such work;
  for a few out-of-order rows, the `timestamptz_minmax_multi_ops` operator
  class tolerates outliers better than the default.
- `EXPLAIN` shows a BRIN index as a `Bitmap Index Scan` with
  `Rows Removed by Index Recheck`: the index is lossy, so the recheck is
  expected; a huge recheck count means the ranges are too wide.
- Prove the gain on production-sized data with `EXPLAIN (ANALYZE, BUFFERS)`;
  on a small table a sequential scan wins and the planner ignores the index.

## Source
- PostgreSQL documentation: BRIN Indexes,
  https://www.postgresql.org/docs/current/brin.html; CREATE INDEX, Storage
  Parameters, https://www.postgresql.org/docs/current/sql-createindex.html;
  pg_stats, https://www.postgresql.org/docs/current/view-pg-stats.html.
- pgmi adaptation: the reading schema, the correlation check that decides
  between BRIN and B-tree, the test that proves the storage parameter and the
  refusal of a unique BRIN index.
