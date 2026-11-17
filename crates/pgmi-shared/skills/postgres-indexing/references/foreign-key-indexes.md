# Index the referencing side of every foreign key that is deleted or joined through

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A foreign key needs a unique index on the referenced columns, so the parent
side is always indexed. PostgreSQL does not create one on the referencing
columns. Deleting a parent row, or updating its key, makes the foreign key
trigger look for matching child rows; without an index on the child's
columns that is a full scan of the child table per parent row, holding row
locks while it runs. `ON DELETE CASCADE` on a large child turns one delete
into minutes. Joins from parent to child pay the same scan.

## Use when
- Parent rows are deleted or their key is updated (cleanup jobs, cascades,
  tenant removal).
- Queries fetch the children of one parent (`WHERE shipment.customer_id = $1`).
- Reviewing a schema: run the audit query below and justify each row it
  returns.

## Don't use when
- The parent is never deleted, its key never changes and nobody filters the
  child by that column (a lookup table of a few rows referenced by a huge
  log). The index would only cost writes.
- An existing index already starts with the foreign key columns: it serves
  the lookup. A second one is waste.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE customer (customer_id bigint PRIMARY KEY);

CREATE TABLE shipment (
    shipment_id  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id bigint NOT NULL REFERENCES customer ON DELETE CASCADE,
    issued_on   date NOT NULL
);

CREATE INDEX shipment_customer_id_idx ON shipment (customer_id);
```

Audit: foreign keys whose columns are not the leading key columns of a valid,
non-partial index on the referencing table. `indkey` lists key columns first,
then `INCLUDE` columns; `indnkeyatts` counts only the key columns, so an
`INCLUDE` column never counts as indexing a foreign key.

```sql
SELECT c.conrelid::regclass AS referencing_table, c.conname
FROM pg_constraint c
WHERE c.contype = 'f'
  AND NOT EXISTS (
      SELECT FROM pg_index i
      WHERE i.indrelid = c.conrelid
        AND i.indisvalid
        AND i.indpred IS NULL
        AND i.indnkeyatts >= cardinality(c.conkey)
        AND (string_to_array(i.indkey::text, ' ')::int2[])[1:cardinality(c.conkey)]
            @> c.conkey
        AND (string_to_array(i.indkey::text, ' ')::int2[])[1:cardinality(c.conkey)]
            <@ c.conkey);
```

## Proof
```sql runnable target=__test__/test_indexing_foreign_key_indexes.sql
CREATE TEMP TABLE customer (customer_id bigint PRIMARY KEY);
CREATE TEMP TABLE shipment (
    shipment_id bigint PRIMARY KEY,
    customer_id bigint NOT NULL REFERENCES customer ON DELETE CASCADE
);
CREATE INDEX shipment_customer_id_idx ON shipment (customer_id);
CREATE TEMP TABLE support_ticket (
    ticket_id bigint PRIMARY KEY,
    customer_id bigint REFERENCES customer
);
CREATE TEMP TABLE account (
    tenant_id  int,
    account_id int,
    PRIMARY KEY (tenant_id, account_id)
);
CREATE TEMP TABLE ledger_entry (
    entry_id   bigint PRIMARY KEY,
    tenant_id  int NOT NULL,
    account_id int NOT NULL,
    FOREIGN KEY (tenant_id, account_id) REFERENCES account
);
CREATE INDEX ledger_entry_tenant_id_idx ON ledger_entry (tenant_id) INCLUDE (account_id);

DO $$
DECLARE
    v_unindexed text;
BEGIN
    SELECT string_agg(c.conrelid::regclass::text, ', ' ORDER BY c.conname)
    INTO v_unindexed
    FROM pg_constraint c
    WHERE c.contype = 'f'
      AND c.conrelid IN ('shipment'::regclass, 'support_ticket'::regclass, 'ledger_entry'::regclass)
      AND NOT EXISTS (
          SELECT FROM pg_index i
          WHERE i.indrelid = c.conrelid
            AND i.indisvalid
            AND i.indpred IS NULL
            AND i.indnkeyatts >= cardinality(c.conkey)
            AND (string_to_array(i.indkey::text, ' ')::int2[])[1:cardinality(c.conkey)]
                @> c.conkey
            AND (string_to_array(i.indkey::text, ' ')::int2[])[1:cardinality(c.conkey)]
                <@ c.conkey);

    IF v_unindexed IS DISTINCT FROM 'ledger_entry, support_ticket' THEN
        RAISE EXCEPTION 'unindexed foreign keys: %, expected ledger_entry and support_ticket',
            coalesce(v_unindexed, 'none');
    END IF;
END $$;
```

## Pitfalls
- An index on `(issued_on, customer_id)` does not cover the foreign key: the
  lookup needs the key columns first. The audit query reports it.
- A partial index (`WHERE status = 'open'`) does not serve the trigger's
  lookup, which has no such predicate.
- On a partitioned child, create the index on the partitioned table so every
  partition gets one, including partitions attached later.
- Adding the index to a large, live table blocks writes for the build: use
  `CREATE INDEX CONCURRENTLY` (see create-index-concurrently.md in this
  skill).

## Source
- PostgreSQL documentation: Constraints, Foreign Keys,
  https://www.postgresql.org/docs/current/ddl-constraints.html; pg_index,
  https://www.postgresql.org/docs/current/catalog-pg-index.html.
- pgmi adaptation: the customer and shipment schema, the catalog audit query,
  the test that proves it reports only the unindexed foreign key.
