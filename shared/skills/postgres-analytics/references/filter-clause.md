# Count and sum several conditions in one pass with FILTER

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A report needs several conditional figures per group: orders, paid orders,
refunds, paid revenue. The textbook answer is `SUM(CASE WHEN ... THEN 1 ELSE
0 END)` per column, or one query per condition joined back together. The
`CASE` form hides the condition inside arithmetic and is easy to get subtly
wrong (`ELSE NULL` versus `ELSE 0`); separate queries scan the table once per
figure, and a `WHERE` on the condition silently drops the groups where it
never holds.

## Use when
- One row per group carries several counts or sums over different subsets.
- A group must stay in the report with 0 when no row matches a condition.

## Don't use when
- Every figure uses the same condition: put it in `WHERE`, which also lets an
  index cut the rows read.
- The function is a non-aggregate window function (`row_number`, `lag`):
  `FILTER` is refused there; filter the rows in a subquery instead.

## Requires
- PostgreSQL 15+ (any supported release has `FILTER`).
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE customer_order (
    order_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    region text NOT NULL,
    status text,
    amount numeric(12,2) NOT NULL
);

SELECT region,
       count(*)                                          AS orders,
       count(*) FILTER (WHERE status = 'paid')           AS paid,
       count(*) FILTER (WHERE status = 'refunded')       AS refunded,
       coalesce(sum(amount) FILTER (WHERE status = 'paid'), 0) AS paid_revenue
FROM customer_order
GROUP BY region;
```

## Proof
```sql runnable target=__test__/test_analytics_filter_clause.sql
CREATE TEMP TABLE customer_order (region text NOT NULL, status text, amount numeric(12,2) NOT NULL);
INSERT INTO customer_order VALUES
    ('north', 'paid', 100), ('north', 'paid', 50), ('north', 'refunded', 30),
    ('south', 'paid', 70), ('south', 'pending', 20), ('south', NULL, 10),
    ('west', 'pending', 40);

CREATE TEMP VIEW order_report AS
SELECT region,
       count(*) AS orders,
       count(*) FILTER (WHERE status = 'paid') AS paid,
       count(*) FILTER (WHERE status = 'refunded') AS refunded,
       count(*) FILTER (WHERE status <> 'paid') AS not_paid,
       coalesce(sum(amount) FILTER (WHERE status = 'paid'), 0) AS paid_revenue,
       sum(amount) FILTER (WHERE status = 'paid') AS raw_paid_revenue
FROM customer_order
GROUP BY region;

DO $$
DECLARE
    v_diff int;
BEGIN
    WITH expected (region, orders, paid, refunded, not_paid, paid_revenue, raw_paid_revenue) AS (
        VALUES ('north', 3::bigint, 2::bigint, 1::bigint, 1::bigint, 150::numeric, 150::numeric),
               ('south', 3, 1, 0, 1, 70, 70),
               ('west',  1, 0, 0, 1, 0, NULL)
    )
    SELECT count(*) INTO v_diff FROM (
        (SELECT * FROM expected EXCEPT SELECT * FROM order_report)
        UNION ALL
        (SELECT * FROM order_report EXCEPT SELECT * FROM expected)
    ) d;
    IF v_diff IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'FILTER report differs from the expected rows in % row(s)', v_diff;
    END IF;

    SELECT count(*) INTO v_diff FROM (
        SELECT region FROM customer_order WHERE status = 'refunded' GROUP BY region
    ) w;
    IF v_diff IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'WHERE kept % group(s), expected it to drop all but north', v_diff;
    END IF;
END $$;
```

## Pitfalls
- `count(...) FILTER` of no rows is 0, but `sum`, `avg`, `min` and `max` of
  no rows are NULL. Wrap them in `coalesce` when the report means 0.
- A row whose condition is NULL is left out: `status <> 'paid'` does not
  count the row with a NULL status. Write `status IS DISTINCT FROM 'paid'`
  when it should.
- `FILTER` restricts the input of one aggregate only; the rows are still
  read. A filter that most figures share belongs in `WHERE`.
- `count(DISTINCT x) FILTER (WHERE ...)` works, but the `DISTINCT` keeps the
  query from parallel aggregation.

## Source
- PostgreSQL documentation: Aggregate Expressions,
  https://www.postgresql.org/docs/current/sql-expressions.html#SYNTAX-AGGREGATES;
  Aggregate Functions, https://www.postgresql.org/docs/current/functions-aggregate.html.
- pgmi adaptation: the order report, the test that pins every figure
  including the empty-group 0 and NULL, and shows `WHERE` dropping groups.
