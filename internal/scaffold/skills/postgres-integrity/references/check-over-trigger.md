# Declare row rules as constraints, not validation triggers

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
Rules such as "a discount never exceeds the line amount" or "a line's
currency matches its order's" end up in a `BEFORE INSERT OR UPDATE` trigger
that raises. The trigger is invisible in the table definition, and when it
reads other rows it races: under `READ COMMITTED` two sessions each miss the
other's change. A declared constraint holds for every writer.

## Use when
- The rule is about one row's own columns: `CHECK`, plus `NOT NULL`.
- The rule says a value must match a row in another table, including "the
  same as my parent's": a foreign key, composite if needed.

## Don't use when
- The rule spans rows: uniqueness among some rows (partial-unique-index.md),
  no overlapping ranges (exclusion-constraint.md).
- The rule aggregates rows ("the order total stays within the credit
  limit"). No constraint expresses it: use a trigger on the child that first
  locks the parent row with `SELECT ... FOR UPDATE`, so writers queue.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE sales_order (
    order_id bigint PRIMARY KEY,
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    UNIQUE (order_id, currency)
);

CREATE TABLE order_line (
    order_id bigint NOT NULL,
    line_no int NOT NULL,
    currency text NOT NULL,
    quantity int NOT NULL CHECK (quantity > 0),
    unit_price numeric(12, 2) NOT NULL CHECK (unit_price >= 0),
    discount numeric(12, 2) NOT NULL DEFAULT 0,
    PRIMARY KEY (order_id, line_no),
    CONSTRAINT order_line_discount_check
        CHECK (discount BETWEEN 0 AND quantity * unit_price),
    CONSTRAINT order_line_order_currency_fk FOREIGN KEY (order_id, currency)
        REFERENCES sales_order (order_id, currency) ON UPDATE CASCADE
);
```

`UNIQUE (order_id, currency)` exists only as the composite foreign key's
target: it turns a cross-table lookup into a declared rule.

## Proof
```sql runnable target=__test__/test_integrity_check_over_trigger.sql
CREATE TEMP TABLE sales_order (order_id bigint PRIMARY KEY,
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'), UNIQUE (order_id, currency));
CREATE TEMP TABLE order_line (
    order_id bigint NOT NULL,
    line_no int NOT NULL,
    currency text NOT NULL,
    quantity int NOT NULL CHECK (quantity > 0),
    unit_price numeric(12, 2) NOT NULL CHECK (unit_price >= 0),
    discount numeric(12, 2) NOT NULL DEFAULT 0,
    PRIMARY KEY (order_id, line_no),
    CONSTRAINT order_line_discount_check
        CHECK (discount BETWEEN 0 AND quantity * unit_price),
    CONSTRAINT order_line_order_currency_fk FOREIGN KEY (order_id, currency)
        REFERENCES sales_order (order_id, currency) ON UPDATE CASCADE
);
DO $$
DECLARE
    v_constraint text;
BEGIN
    INSERT INTO sales_order VALUES (1, 'EUR');
    INSERT INTO order_line VALUES (1, 1, 'EUR', 2, 10.00, 5.00);

    BEGIN
        INSERT INTO order_line VALUES (1, 2, 'EUR', 1, 10.00, 25.00);
        RAISE EXCEPTION 'a discount above the line amount was accepted';
    EXCEPTION WHEN check_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'order_line_discount_check' THEN
            RAISE EXCEPTION 'wrong constraint fired: %', v_constraint;
        END IF;
    END;

    BEGIN
        INSERT INTO order_line VALUES (1, 3, 'USD', 1, 10.00, 0);
        RAISE EXCEPTION 'a line in another currency than its order was accepted';
    EXCEPTION WHEN foreign_key_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'order_line_order_currency_fk' THEN
            RAISE EXCEPTION 'wrong constraint fired: %', v_constraint;
        END IF;
    END;
END $$;
```

## Pitfalls
- A `CHECK` passes when it evaluates to NULL. `CHECK (discount <= total)` on a
  nullable column accepts NULL; pair it with `NOT NULL` when the value is
  required.
- A `CHECK` must depend only on the row. Calling a function that reads other
  tables appears to work, but PostgreSQL never rechecks it when those tables
  change, and a dump can fail to restore. Use the trigger with a parent lock.
- Name every constraint you map to an error message: a multi-column `CHECK`
  gets a generated name numbered by declaration order (`order_line_check1`).
- Adding a `CHECK` or foreign key to a large table scans it under a lock that
  blocks writes. Add it `NOT VALID` (new rows are checked at once), then
  `ALTER TABLE ... VALIDATE CONSTRAINT`, which takes a lock that lets writes
  continue.

## Source
- PostgreSQL documentation: Check Constraints and Foreign Keys,
  https://www.postgresql.org/docs/current/ddl-constraints.html; ALTER TABLE,
  NOT VALID and VALIDATE CONSTRAINT,
  https://www.postgresql.org/docs/current/sql-altertable.html.
- pgmi adaptation: the order/line schema with a composite foreign key in
  place of a lookup trigger, the test that proves both rejections.
