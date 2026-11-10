# Store a validity period as one range column

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A price, contract or assignment is valid from one time to another. Two columns
`valid_from` and `valid_to` invite four mistakes: end before start, a closed
end that double-counts the boundary instant, `NULL` meaning "still valid" in
some queries and "unknown" in others, and overlap checks written by hand that
get one of the four cases wrong. Nothing stops two periods from overlapping.

## Use when
- A row is valid over an interval of instants (`tstzrange`) or days
  (`daterange`), and queries ask "valid at t" or "overlaps p".
- Periods for one key must not overlap: pair the range with an exclusion
  constraint (see exclusion-constraint.md in the postgres-integrity skill).

## Don't use when
- The row records a single moment: use `timestamptz` (see timestamptz.md in
  this skill).
- Only the start matters and the end is the next row's start: store the start
  and derive the end with `lead()`.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE price (
    price_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    product_id bigint NOT NULL,
    amount numeric(12,2) NOT NULL,
    valid tstzrange NOT NULL CHECK (NOT isempty(valid) AND lower(valid) IS NOT NULL)
);
CREATE INDEX price_valid ON price USING gist (valid);

-- The price in force at an instant.
SELECT amount FROM price WHERE product_id = 7 AND valid @> now();

-- Close the open period and start the next one at the same instant.
UPDATE price SET valid = tstzrange(lower(valid), now())
WHERE product_id = 7 AND upper_inf(valid);
INSERT INTO price (product_id, amount, valid) VALUES (7, 12.50, tstzrange(now(), NULL));
```

The two-argument constructor builds `[)` bounds: the start is included, the
end is not, so consecutive periods share a boundary without overlapping. A
`NULL` bound is unbounded on that side; `upper_inf` tests for it. `@>` asks
"contains", `&&` "overlaps", `-|-` "adjacent". `daterange` is discrete and
stored in `[)` form whatever bounds are given. `range_agg` merges ranges into
a multirange.

## Proof
```sql runnable target=__test__/test_time_range_types.sql
DO $$
DECLARE
    v_jan daterange := daterange('2026-01-01', '2026-02-01');
    v_feb daterange := daterange('2026-02-01', '2026-03-01');
    v_open tstzrange := tstzrange('2026-01-01 00:00+00', NULL);
BEGIN
    IF v_jan && v_feb OR NOT v_jan -|- v_feb THEN
        RAISE EXCEPTION '[) periods that meet must be adjacent, not overlapping';
    END IF;
    IF NOT v_jan @> date '2026-01-31' OR v_jan @> date '2026-02-01' THEN
        RAISE EXCEPTION 'containment includes the start and excludes the end';
    END IF;
    IF daterange('2026-01-01', '2026-01-31', '[]') IS DISTINCT FROM v_jan THEN
        RAISE EXCEPTION 'daterange was not canonicalized to [): %',
            daterange('2026-01-01', '2026-01-31', '[]');
    END IF;
    IF NOT upper_inf(v_open) OR upper(v_open) IS NOT NULL
       OR NOT v_open @> timestamptz '2999-01-01 00:00+00' THEN
        RAISE EXCEPTION 'a NULL upper bound must be unbounded: %', v_open;
    END IF;

    BEGIN
        PERFORM daterange('2026-02-01', '2026-01-01');
        RAISE EXCEPTION 'a range ending before it starts was accepted';
    EXCEPTION WHEN data_exception THEN
        IF SQLSTATE IS DISTINCT FROM '22000' THEN
            RAISE EXCEPTION 'unexpected SQLSTATE %: %', SQLSTATE, SQLERRM;
        END IF;
    END;
END $$;
```

## Pitfalls
- `upper_inf` is false for an `'infinity'` bound. Use `NULL` for "still
  valid", never `'infinity'`, so one test covers every open period.
- `lower` and `upper` of an empty range are `NULL`, and an empty range
  overlaps nothing. `daterange(d, d)` is empty; the `CHECK` in the pattern
  rejects it.
- Only a GiST (or SP-GiST) index serves `@>` and `&&`; a B-tree on the range
  serves equality only. To filter by key and range in one index, use
  `btree_gist`, as the exclusion constraint does.
- Building the range in the query (`tstzrange(valid_from, valid_to) @> t`)
  over two legacy columns cannot use an index on either column. Migrate to
  one range column.

## Source
- PostgreSQL documentation: Range Types,
  https://www.postgresql.org/docs/current/rangetypes.html; Range/Multirange
  Functions and Operators,
  https://www.postgresql.org/docs/current/functions-range.html.
- pgmi adaptation: the price schema, closing an open period, the test that
  proves half-open bounds, canonical form and the rejected inverted range.
