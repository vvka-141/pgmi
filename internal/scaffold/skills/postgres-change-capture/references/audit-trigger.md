# Record every row change in an audit table with a trigger

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
"Who changed this row, when, and from what?" An `INSERT INTO history` next to
every write in application code misses any write that bypasses that code (a
script, a bulk fix, another service). A trigger records every change the
database accepts, in the same transaction, so the audit row commits or rolls
back with the change.

## Use when
- Every change must be traceable to an actor and a time, whoever writes.

## Don't use when
- An external system consumes the changes: use a publication and logical
  decoding (logical-decoding.md in this skill) instead of polling a table.
- Queries need the table as it was at a past time: model validity periods.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE row_audit (
    audit_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    table_name text NOT NULL, operation text NOT NULL, actor text,
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    old_row jsonb, new_row jsonb
);

CREATE OR REPLACE FUNCTION row_audit_capture() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO row_audit (table_name, operation, actor, old_row, new_row)
    VALUES (TG_TABLE_NAME, TG_OP, current_setting('app.actor', true),
            to_jsonb(OLD), to_jsonb(NEW));
    RETURN NULL;
END $$;

CREATE TRIGGER price_audit AFTER INSERT OR UPDATE OR DELETE ON price
    FOR EACH ROW EXECUTE FUNCTION row_audit_capture();
```

`OLD` is NULL on INSERT and `NEW` on DELETE, so one function serves every
table and operation. The application sets the actor per transaction with
`SET LOCAL app.actor = 'alice'`. For bulk writes, a statement-level trigger
(`AFTER UPDATE ... REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
FOR EACH STATEMENT`) writes one `INSERT ... SELECT` over the transition
tables, joined on the key, instead of one insert per row.

## Proof
```sql runnable target=__test__/test_change_capture_audit_trigger.sql
CREATE TEMP TABLE price (sku text PRIMARY KEY, amount numeric NOT NULL);
CREATE TEMP TABLE row_audit (
    audit_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    table_name text NOT NULL, operation text NOT NULL, actor text,
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    old_row jsonb, new_row jsonb
);
CREATE FUNCTION pg_temp.row_audit_capture() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO row_audit (table_name, operation, actor, old_row, new_row)
    VALUES (TG_TABLE_NAME, TG_OP, current_setting('app.actor', true),
            to_jsonb(OLD), to_jsonb(NEW));
    RETURN NULL;
END $$;
CREATE TRIGGER price_audit AFTER INSERT OR UPDATE OR DELETE ON price
    FOR EACH ROW EXECUTE FUNCTION pg_temp.row_audit_capture();

DO $$
DECLARE v_count int; v_audit row_audit;
BEGIN
    PERFORM set_config('app.actor', 'alice', true);
    INSERT INTO price VALUES ('a', 10), ('b', 20);
    UPDATE price SET amount = 40 WHERE sku = 'b';
    DELETE FROM price WHERE sku = 'a';

    SELECT * INTO STRICT v_audit FROM row_audit WHERE operation = 'UPDATE';
    IF v_audit.actor IS DISTINCT FROM 'alice'
       OR v_audit.table_name IS DISTINCT FROM 'price'
       OR (v_audit.old_row->>'amount')::numeric IS DISTINCT FROM 20
       OR (v_audit.new_row->>'amount')::numeric IS DISTINCT FROM 40 THEN
        RAISE EXCEPTION 'update audit row is wrong: %', to_jsonb(v_audit);
    END IF;
    SELECT * INTO STRICT v_audit FROM row_audit WHERE operation = 'DELETE';
    IF v_audit.new_row IS NOT NULL OR v_audit.old_row->>'sku' IS DISTINCT FROM 'a' THEN
        RAISE EXCEPTION 'delete audit row is wrong: %', to_jsonb(v_audit);
    END IF;

    TRUNCATE price;
    SELECT count(*) INTO v_count FROM row_audit;
    IF v_count IS DISTINCT FROM 4 THEN
        RAISE EXCEPTION 'expected 4 audit rows and none for TRUNCATE, got %', v_count;
    END IF;
END $$;
```

## Pitfalls
- `TRUNCATE` fires no row or `DELETE` triggers (the Proof's last check). Add
  an `AFTER TRUNCATE ... FOR EACH STATEMENT` trigger, or revoke `TRUNCATE`.
- The trigger runs with the writer's privileges, so every writer needs
  `INSERT` on the audit table and could forge rows. Make the function
  `SECURITY DEFINER` with `SET search_path` pinned, owned by the one role
  that may write the audit table.
- `now()` is the transaction start, the same for every change in one
  transaction; `clock_timestamp()` orders changes within it.
- The actor is NULL when the application forgot `SET LOCAL app.actor`. Make
  `actor` `NOT NULL` if a missing actor must fail the write.
- Volume: one row and two jsonb copies per changed row. Partition the audit
  table by time and drop old partitions instead of deleting rows.

## Source
- PostgreSQL documentation: CREATE TRIGGER,
  https://www.postgresql.org/docs/current/sql-createtrigger.html; Trigger
  Functions, https://www.postgresql.org/docs/current/plpgsql-trigger.html;
  TRUNCATE, https://www.postgresql.org/docs/current/sql-truncate.html.
- pgmi adaptation: the generic jsonb audit table, the actor setting, the test
  that proves the capture and the TRUNCATE gap.
