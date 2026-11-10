# Upsert with INSERT ... ON CONFLICT; synchronise sets with MERGE

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
"Insert the row, or update it if it exists." The obvious code checks with a
`SELECT`, then inserts or updates. Two sessions both see no row and both
insert: one fails with a unique violation, or, without a unique key, the row
exists twice. `INSERT ... ON CONFLICT DO UPDATE` decides atomically against a
unique index. `MERGE` applies a whole source set with several actions, but it
does not carry that guarantee.

## Use when
- `ON CONFLICT`: writers insert or update single rows or batches by key,
  concurrently. This is the default.
- `MERGE`: one job reconciles a target with a source set (insert new, update
  changed, delete or flag the rest), and concurrent writers to the same keys
  are excluded or the job retries.

## Don't use when
- The key has no unique constraint or index: add one first. `ON CONFLICT`
  needs it; without it, concurrent `MERGE`s insert duplicates.
- Only inserts must be idempotent: `ON CONFLICT DO NOTHING`.

## Requires
- PostgreSQL 15+.
- `MERGE ... WHEN NOT MATCHED BY SOURCE` and `MERGE ... RETURNING` with
  `merge_action()`: PostgreSQL 17+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE stock_level (
    sku text PRIMARY KEY,
    quantity int NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO stock_level AS s (sku, quantity) VALUES ('A-1', 5)
ON CONFLICT (sku) DO UPDATE
SET quantity = EXCLUDED.quantity, updated_at = now()
WHERE s.quantity IS DISTINCT FROM EXCLUDED.quantity;
```

The `WHERE` skips no-op updates, so unchanged rows write no new version.
Reconciling the table against a feed:

```sql
MERGE INTO stock_level s
USING stock_feed f ON f.sku = s.sku
WHEN MATCHED AND f.quantity = 0 THEN DELETE
WHEN MATCHED AND s.quantity IS DISTINCT FROM f.quantity THEN
    UPDATE SET quantity = f.quantity, updated_at = now()
WHEN NOT MATCHED THEN INSERT (sku, quantity) VALUES (f.sku, f.quantity);
```

On 17+, `WHEN NOT MATCHED BY SOURCE THEN DELETE` also removes rows absent
from the feed; on 15 and 16 that takes a separate `DELETE ... WHERE NOT
EXISTS`.

## Proof
```sql runnable target=__test__/test_concurrency_upsert_vs_merge.sql
CREATE TEMP TABLE stock_level (sku text PRIMARY KEY, quantity int NOT NULL);
CREATE TEMP TABLE stock_feed (sku text PRIMARY KEY, quantity int NOT NULL);

DO $$
DECLARE v_rows int; v_left text[];
BEGIN
    INSERT INTO stock_level VALUES ('A', 1), ('B', 2), ('C', 3);

    INSERT INTO stock_level AS s VALUES ('A', 1), ('D', 4)
    ON CONFLICT (sku) DO UPDATE SET quantity = EXCLUDED.quantity
    WHERE s.quantity IS DISTINCT FROM EXCLUDED.quantity;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'upsert touched % rows, expected only the insert of D', v_rows;
    END IF;

    INSERT INTO stock_feed VALUES ('A', 10), ('B', 0), ('E', 5);
    MERGE INTO stock_level s USING stock_feed f ON f.sku = s.sku
    WHEN MATCHED AND f.quantity = 0 THEN DELETE
    WHEN MATCHED THEN UPDATE SET quantity = f.quantity
    WHEN NOT MATCHED THEN INSERT VALUES (f.sku, f.quantity);
    SELECT array_agg(sku || '=' || quantity ORDER BY sku) INTO v_left FROM stock_level;
    IF v_left IS DISTINCT FROM '{A=10,C=3,D=4,E=5}'::text[] THEN
        RAISE EXCEPTION 'after MERGE: %', v_left;
    END IF;

    IF current_setting('server_version_num')::int < 170000 THEN RETURN; END IF;
    EXECUTE $q$
        MERGE INTO stock_level s USING stock_feed f ON f.sku = s.sku
        WHEN NOT MATCHED BY SOURCE THEN DELETE
    $q$;
    SELECT array_agg(sku ORDER BY sku) INTO v_left FROM stock_level;
    IF v_left IS DISTINCT FROM '{A,E}'::text[] THEN
        RAISE EXCEPTION 'after NOT MATCHED BY SOURCE: %', v_left;
    END IF;
END $$;
```

The 17+ statement runs through `EXECUTE` so 15 and 16 never parse it. The
Proof shows each statement's effect; concurrency needs two sessions.

## Pitfalls
- `MERGE` with `WHEN NOT MATCHED THEN INSERT` raises `23505` when another
  session inserts the same key concurrently; it does not fall back to its
  `MATCHED` actions. Use `ON CONFLICT` for concurrent single-row upserts, or
  retry the `MERGE`.
- Both fail with `21000` when one statement would change the same row twice
  (a key repeated in the input). Deduplicate first (`DISTINCT ON (sku)`).

## Source
- PostgreSQL documentation: INSERT, ON CONFLICT,
  https://www.postgresql.org/docs/current/sql-insert.html; MERGE,
  https://www.postgresql.org/docs/current/sql-merge.html; Transaction
  Isolation, Read Committed,
  https://www.postgresql.org/docs/current/transaction-iso.html; Release 17,
  https://www.postgresql.org/docs/current/release-17.html.
- pgmi adaptation: the stock schema, the no-op-skipping upsert, and the test
  that proves both statements with a version guard for the 17+ clause.
