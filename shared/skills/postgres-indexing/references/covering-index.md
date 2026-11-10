# Answer a hot query from the index alone with INCLUDE columns

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A frequent query filters on one column and reads two or three others:
"status and total of this customer's shipments". An index on the filter
column finds the rows, then visits the table for every match to read the
other columns. Adding those columns to the key (`(customer_id, status,
total_cents)`) makes them part of the sort order and of any uniqueness the
index enforces, and needs a B-tree operator class for each.

## Use when
- One query shape dominates and reads a few narrow columns besides the
  filter.
- The table changes slowly enough that most pages are all-visible after
  vacuum, so an index-only scan can skip the heap.
- A unique key must stay unique on its own column while the index also
  carries payload (`UNIQUE (code) INCLUDE (label)`).

## Don't use when
- The table is write-heavy: the visibility map stays mostly unset, every
  match still visits the heap, and the payload only makes the index larger.
- The payload is wide (long text, jsonb): index tuples that exceed the size
  limit make inserts fail. Read those columns from the table.
- The query also sorts or filters on the extra columns: they belong in the
  key, not in `INCLUDE`.

## Requires
- PostgreSQL 15+.
- Extension: none. `INCLUDE` is supported by B-tree, GiST and SP-GiST.
- Server settings: none; autovacuum keeps the visibility map current.

## Pattern
```sql
CREATE TABLE shipment (
    shipment_id  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id bigint NOT NULL,
    status      text NOT NULL,
    total_cents bigint NOT NULL,
    notes       text
);

CREATE INDEX shipment_customer_id_covering_idx
    ON shipment (customer_id) INCLUDE (status, total_cents);

SELECT status, total_cents FROM shipment WHERE customer_id = $1;
```

Check the plan with `EXPLAIN (ANALYZE, BUFFERS)` on production-sized data:
`Index Only Scan` with low `Heap Fetches` is the win; high `Heap Fetches`
means the visibility map is stale or the table churns too much.

## Proof
```sql runnable target=__test__/test_indexing_covering_index.sql
CREATE TEMP TABLE shipment (
    shipment_id bigint PRIMARY KEY,
    customer_id bigint NOT NULL,
    status text NOT NULL,
    total_cents bigint NOT NULL
);
CREATE INDEX shipment_customer_id_covering_idx
    ON shipment (customer_id) INCLUDE (status, total_cents);
CREATE TEMP TABLE product (code text NOT NULL, label text NOT NULL);
CREATE UNIQUE INDEX product_code_key ON product (code) INCLUDE (label);

DO $$
DECLARE
    v_keys int;
    v_all int;
    v_constraint text;
BEGIN
    SELECT indnkeyatts, indnatts INTO STRICT v_keys, v_all
    FROM pg_index WHERE indexrelid = 'shipment_customer_id_covering_idx'::regclass;
    IF (v_keys, v_all) IS DISTINCT FROM (1, 3) THEN
        RAISE EXCEPTION 'expected 1 key and 3 stored columns, got % and %', v_keys, v_all;
    END IF;

    INSERT INTO product VALUES ('A1', 'first');
    BEGIN
        INSERT INTO product VALUES ('A1', 'different label');
        RAISE EXCEPTION 'a duplicate code was accepted: INCLUDE columns joined the key';
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'product_code_key' THEN
            RAISE EXCEPTION 'wrong constraint fired: %', v_constraint;
        END IF;
    END;
END $$;
```

## Pitfalls
- The planner chooses an index-only scan from statistics; a freshly loaded
  table that has not been vacuumed still visits the heap for every row.
- Expressions cannot be `INCLUDE` columns.
- B-tree deduplication is never used on an index with `INCLUDE` columns, so a
  low-cardinality key costs more space than the plain index did.
- Replace the plain index on the same key rather than keeping both; the old
  one still costs every write.

## Source
- PostgreSQL documentation: Index-Only Scans and Covering Indexes,
  https://www.postgresql.org/docs/current/indexes-index-only-scans.html;
  CREATE INDEX, https://www.postgresql.org/docs/current/sql-createindex.html.
- pgmi adaptation: the shipment and product schema, the catalog check of key
  versus stored columns, the test that proves INCLUDE columns stay out of the
  uniqueness rule.
