# Compute subtotals and a grand total in one pass with ROLLUP

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A report wants detail rows, a subtotal per region and a grand total. The
obvious SQL is one `GROUP BY` per level glued with `UNION ALL`, which scans
the data once per level and repeats every expression. Worse, a subtotal row
shows NULL in the columns it rolled up, so a reader cannot tell "all
products" from a product that really is NULL.

## Use when
- One result needs several grouping levels: detail, subtotals, total.
- Labels such as "All regions" must appear on exactly the subtotal rows.

## Don't use when
- Only one level is needed: a plain `GROUP BY`.
- The levels need different filters or joins: separate queries are clearer.

## Requires
- PostgreSQL 15+ (any supported release has grouping sets).
- Extension: none.
- Server settings: none.

## Pattern
`ROLLUP (a, b)` is `GROUPING SETS ((a, b), (a), ())`; `CUBE (a, b)` adds
`(b)`, every subset. `GROUPING(a, b)` is a bit mask, rightmost argument in
the lowest bit, with a bit set when that column is rolled up in the row.

```sql
CREATE TABLE sale (
    sale_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    region text NOT NULL,
    product text,
    units int NOT NULL
);

SELECT CASE WHEN GROUPING(region) = 1 THEN 'All regions' ELSE region END AS region,
       CASE WHEN GROUPING(product) = 1 THEN 'All products'
            ELSE coalesce(product, '(no product)') END AS product,
       GROUPING(region, product) AS level,
       sum(units) AS units
FROM sale
GROUP BY ROLLUP (region, product)
ORDER BY GROUPING(region, product), region, product;
```

## Proof
```sql runnable target=__test__/test_analytics_grouping_sets.sql
CREATE TEMP TABLE sale (region text NOT NULL, product text, units int NOT NULL);
INSERT INTO sale VALUES
    ('north', 'apple', 10), ('north', 'pear', 5), ('north', NULL, 2), ('south', 'apple', 7);

DO $$
DECLARE
    v_diff int;
    v_rows int;
BEGIN
    WITH report AS (
        SELECT CASE WHEN GROUPING(region) = 1 THEN 'All regions' ELSE region END AS region,
               CASE WHEN GROUPING(product) = 1 THEN 'All products'
                    ELSE coalesce(product, '(no product)') END AS product,
               GROUPING(region, product) AS level,
               sum(units) AS units
        FROM sale
        GROUP BY ROLLUP (region, product)
    ), expected (region, product, level, units) AS (
        VALUES ('north', 'apple', 0, 10::bigint), ('north', 'pear', 0, 5),
               ('north', '(no product)', 0, 2), ('south', 'apple', 0, 7),
               ('north', 'All products', 1, 17), ('south', 'All products', 1, 7),
               ('All regions', 'All products', 3, 24)
    )
    SELECT count(*) INTO v_diff FROM (
        (SELECT * FROM expected EXCEPT SELECT * FROM report)
        UNION ALL
        (SELECT * FROM report EXCEPT SELECT * FROM expected)
    ) d;
    IF v_diff IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'ROLLUP report differs from the expected rows in % row(s)', v_diff;
    END IF;

    SELECT count(*) INTO v_rows FROM (SELECT 1 FROM sale GROUP BY CUBE (region, product)) c;
    IF v_rows IS DISTINCT FROM 10 THEN
        RAISE EXCEPTION 'CUBE produced % rows, expected 4 detail + 2 region + 3 product + 1 total', v_rows;
    END IF;

    BEGIN
        EXECUTE 'SELECT region, GROUPING(units) FROM sale GROUP BY ROLLUP (region)';
        RAISE EXCEPTION 'GROUPING over a column outside GROUP BY was accepted';
    EXCEPTION WHEN grouping_error THEN
        NULL;
    END;
END $$;
```

## Pitfalls
- `coalesce(product, 'All products')` labels the real NULL product as a
  subtotal. Decide with `GROUPING()`, never with `IS NULL`.
- `GROUPING()` arguments must match `GROUP BY` expressions exactly; anything
  else fails with `42803`.
- Without `ORDER BY` the subtotal rows come back in no promised order. Sort
  by `GROUPING(...)` to put them where the report wants them.
- `CUBE` over n columns makes 2^n grouping sets; list the sets you need with
  `GROUPING SETS` instead of cubing wide keys.
- Queries with grouping sets do not use parallel aggregation.

## Source
- PostgreSQL documentation: GROUPING SETS, CUBE, and ROLLUP,
  https://www.postgresql.org/docs/current/queries-table-expressions.html#QUERIES-GROUPING-SETS;
  Aggregate Functions, Grouping Operations,
  https://www.postgresql.org/docs/current/functions-aggregate.html.
- pgmi adaptation: the sale schema with a real NULL, the labelling rule, the
  test that pins every level and rejects a non-grouping `GROUPING()` argument.
