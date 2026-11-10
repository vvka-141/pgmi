# Use window functions instead of self-joins and correlated subqueries

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
Running totals, rank in a group, the gap since the previous row, the latest
row per group. Textbook SQL self-joins or runs a correlated subquery per row:
quadratic work, ties handled however the join falls. A window does it in one pass.

## Use when
- A per-row figure depends on other rows of its group: running sum, rank,
  previous value, share of the group total; or the top row(s) per group.

## Don't use when
- One row per group with only aggregates: a plain `GROUP BY`.
- Few groups, many rows each, and an index on (group, order): a `LATERAL`
  subquery with `ORDER BY ... LIMIT 1` per group reads far fewer rows.

## Requires
- PostgreSQL 15+ (any supported release has these window functions).
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE sale (
    sale_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account text NOT NULL, sold_on date NOT NULL, amount numeric(12,2) NOT NULL
);

SELECT sale_id, account, sold_on, amount,
       sum(amount) OVER (w ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS running_total,
       sold_on - lag(sold_on) OVER w AS days_since_previous,
       rank() OVER (PARTITION BY account ORDER BY amount DESC) AS amount_rank
FROM sale
WINDOW w AS (PARTITION BY account ORDER BY sold_on, sale_id);

SELECT DISTINCT ON (account) account, sale_id, sold_on, amount
FROM sale
ORDER BY account, sold_on DESC, sale_id DESC;
```

The second query is the latest sale per account. For the top N per
group, filter `row_number() OVER (...) <= N` in an outer query.

## Proof
```sql runnable target=__test__/test_analytics_window_functions.sql
CREATE TEMP TABLE sale (sale_id int PRIMARY KEY, account text NOT NULL, sold_on date NOT NULL, amount numeric(12,2) NOT NULL);
INSERT INTO sale VALUES
    (1, 'a', '2026-01-01', 10), (2, 'a', '2026-01-02', 20), (3, 'a', '2026-01-02', 5),
    (4, 'a', '2026-01-05', 7), (5, 'b', '2026-01-01', 3), (6, 'b', '2026-01-03', 4);

DO $$
DECLARE
    v_diff int;
    v_peer numeric;
BEGIN
    WITH actual AS (
        SELECT sale_id,
               sum(amount) OVER (w ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS running_total,
               sold_on - lag(sold_on) OVER w AS days_since_previous,
               rank() OVER (PARTITION BY account ORDER BY amount DESC) AS amount_rank
        FROM sale
        WINDOW w AS (PARTITION BY account ORDER BY sold_on, sale_id)
    ), expected (sale_id, running_total, days_since_previous, amount_rank) AS (
        VALUES (1, 10::numeric, NULL::int, 2::bigint), (2, 30, 1, 1), (3, 35, 0, 4),
               (4, 42, 3, 3), (5, 3, NULL, 2), (6, 7, 2, 1)
    )
    SELECT count(*) INTO v_diff FROM (
        (SELECT * FROM expected EXCEPT SELECT * FROM actual) UNION ALL
        (SELECT * FROM actual EXCEPT SELECT * FROM expected)
    ) d;
    IF v_diff IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'window results differ from the expected rows in % row(s)', v_diff;
    END IF;
    SELECT running INTO STRICT v_peer FROM (SELECT sale_id, sum(amount) OVER
        (PARTITION BY account ORDER BY sold_on) AS running FROM sale) r WHERE sale_id = 2;
    IF v_peer IS DISTINCT FROM 35::numeric THEN
        RAISE EXCEPTION 'default frame gave % for sale 2, expected 35 (peer 3 included)', v_peer;
    END IF;
    WITH by_distinct_on AS (SELECT DISTINCT ON (account) sale_id FROM sale
        ORDER BY account, sold_on DESC, sale_id DESC
    ), by_row_number AS (
        SELECT sale_id FROM (SELECT sale_id, row_number() OVER
            (PARTITION BY account ORDER BY sold_on DESC, sale_id DESC) AS rn FROM sale) r
        WHERE rn = 1), expected (sale_id) AS (VALUES (4), (6))
    SELECT count(*) INTO v_diff FROM (
        (SELECT * FROM expected EXCEPT SELECT * FROM by_distinct_on) UNION ALL
        (SELECT * FROM by_distinct_on EXCEPT SELECT * FROM expected) UNION ALL
        (SELECT * FROM expected EXCEPT SELECT * FROM by_row_number) UNION ALL
        (SELECT * FROM by_row_number EXCEPT SELECT * FROM expected)
    ) d;
    IF v_diff IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'latest-per-account differs from sales 4 and 6 in % row(s)', v_diff;
    END IF;
    BEGIN
        EXECUTE 'SELECT sale_id FROM sale WHERE row_number() OVER () = 1';
        RAISE EXCEPTION 'a window function in WHERE was accepted';
    EXCEPTION WHEN windowing_error THEN NULL;
    END;
END $$;
```

## Pitfalls
- With `ORDER BY`, the default frame is `RANGE BETWEEN UNBOUNDED PRECEDING
  AND CURRENT ROW`, which ends at the current row's last peer: rows tied on
  the sort key all get the same running total. Write `ROWS BETWEEN ...` and
  add a unique tiebreaker to the `ORDER BY`; `DISTINCT ON` needs that full
  order too, or it keeps an arbitrary row per group.
- `last_value` and `nth_value` see only that default frame, so
  `last_value(x) OVER (ORDER BY t)` returns the current row's last peer, not
  the group's last row. Use `first_value` with the opposite order instead.
- Window functions run after `WHERE`, so they cannot appear there (`42P20`).
  Filter on them in an outer query.

## Source
- PostgreSQL documentation: Window Function Calls,
  https://www.postgresql.org/docs/current/sql-expressions.html#SYNTAX-WINDOW-FUNCTIONS;
  Window Functions, https://www.postgresql.org/docs/current/functions-window.html;
  SELECT, DISTINCT Clause, https://www.postgresql.org/docs/current/sql-select.html#SQL-DISTINCT.
- pgmi adaptation: the sale schema with a tie, the test pinning each
  figure, the peer pitfall, and `DISTINCT ON` agreeing with `row_number`.
