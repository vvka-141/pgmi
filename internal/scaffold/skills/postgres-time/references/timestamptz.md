# Store every point in time as `timestamptz`

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
An event time (created, paid, shipped) lands in a `timestamp` column because
the name reads naturally. `timestamp` stores a wall-clock reading with no
zone: the same value means different instants for clients in different zones,
and one hour each autumn is ambiguous. Comparisons, ordering and arithmetic
across zones and daylight-saving changes then silently go wrong.

## Use when
- The column records when something happened or will happen: `timestamptz`.
- The column is a calendar day with no time (due date, birthday): `date`.

## Don't use when
- The value is a wall-clock reading in a zone that may change its rules later
  (a meeting at 09:00 Berlin time next year): store `timestamp` plus the zone
  name, and convert with `AT TIME ZONE` when reading.
- The value is a period with a start and an end: use a range (see
  range-types.md in this skill).

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE sale (
    sale_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    paid_at timestamptz NOT NULL DEFAULT now(),
    due_on date NOT NULL
);

-- Wall clock in a named zone, to timestamptz and back.
SELECT timestamp '2026-01-15 13:00' AT TIME ZONE 'Europe/Berlin';
SELECT paid_at AT TIME ZONE 'Europe/Berlin' FROM sale;

-- The calendar day of an instant, in the zone the business runs in.
SELECT (paid_at AT TIME ZONE 'Europe/Berlin')::date AS paid_on, count(*)
FROM sale GROUP BY 1;
```

`timestamptz` stores a UTC instant; it keeps no zone. Output renders it in
the session `TimeZone`. `timestamptz AT TIME ZONE z` gives the wall clock in
`z` as `timestamp`; `timestamp AT TIME ZONE z` reads a wall clock in `z` and
gives the instant.

`now()` is the start of the transaction. Before deploy.sql's first
top-level `COMMIT`, where the templates run their tests, `now()` is one
value; each statement after it is its own transaction (`pgmi ai`).
`clock_timestamp()` is the real time and advances.

## Proof
```sql runnable target=__test__/test_time_timestamptz.sql
DO $$
DECLARE
    v_instant timestamptz := '2026-01-15 12:00:00+00';
    v_utc text;
    v_berlin text;
BEGIN
    PERFORM set_config('TimeZone', 'UTC', true);
    v_utc := to_char(v_instant, 'YYYY-MM-DD HH24:MI OF');
    PERFORM set_config('TimeZone', 'Europe/Berlin', true);
    v_berlin := to_char(v_instant, 'YYYY-MM-DD HH24:MI OF');

    IF v_utc IS DISTINCT FROM '2026-01-15 12:00 +00'
       OR v_berlin IS DISTINCT FROM '2026-01-15 13:00 +01' THEN
        RAISE EXCEPTION 'rendering: UTC %, Berlin %', v_utc, v_berlin;
    END IF;
    IF '2026-01-15 13:00:00+01'::timestamptz IS DISTINCT FROM v_instant THEN
        RAISE EXCEPTION 'the same instant in two zones does not compare equal';
    END IF;

    IF (v_instant AT TIME ZONE 'America/New_York') IS DISTINCT FROM timestamp '2026-01-15 07:00'
       OR (timestamp '2026-01-15 07:00' AT TIME ZONE 'America/New_York') IS DISTINCT FROM v_instant THEN
        RAISE EXCEPTION 'AT TIME ZONE did not round-trip';
    END IF;

    IF now() IS DISTINCT FROM transaction_timestamp() OR clock_timestamp() <= now() THEN
        RAISE EXCEPTION 'now() % is not the transaction start before clock %', now(), clock_timestamp();
    END IF;
END $$;
```

## Pitfalls
- `paid_at::date` and `date_trunc('day', paid_at)` use the session
  `TimeZone`, so a daily report changes with the client. Convert with
  `AT TIME ZONE` first, as in the pattern.
- A literal without an offset (`'2026-01-15 12:00'`) cast to `timestamptz` is
  read in the session `TimeZone`. Send offsets from the application.
- `+ interval '1 day'` on a `timestamptz` keeps the wall clock in the session
  zone across a daylight-saving change; `+ interval '24 hours'` does not.
- A row stamped `DEFAULT now()` in a deploy or test gets the transaction start
  time, the same for every row. Use `clock_timestamp()` where the order of
  rows within one transaction matters.

## Source
- PostgreSQL documentation: Date/Time Types,
  https://www.postgresql.org/docs/current/datatype-datetime.html; Date/Time
  Functions and Operators, AT TIME ZONE and Current Date/Time,
  https://www.postgresql.org/docs/current/functions-datetime.html.
- pgmi adaptation: the sale schema, the test that renders one instant in
  two zones, and what `now()` means inside a pgmi deploy.
