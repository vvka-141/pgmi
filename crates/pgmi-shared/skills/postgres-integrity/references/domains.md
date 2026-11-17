# Name a reused value rule once with a domain

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
The same rule repeats across tables: an email has an `@`, an amount is
positive, a country code is two upper-case letters. Copied as a column
`CHECK` into every table, the copies drift, and a new column forgets one. A
domain is a type with the rule attached; every column, function argument
and PL/pgSQL variable of that type enforces it.

## Use when
- One value rule applies to columns in several tables or to function
  parameters.
- The rule depends only on the value itself.

## Don't use when
- The rule involves other columns of the row: a table `CHECK`.
- The value set changes at runtime (a list of currencies the business adds
  to): a lookup table and a foreign key, so adding a value is an `INSERT`,
  not DDL.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE DOMAIN email AS text
    CONSTRAINT email_shape CHECK (VALUE ~ '^[^@[:space:]]+@[^@[:space:]]+$');

CREATE DOMAIN positive_amount AS numeric(12, 2)
    CONSTRAINT positive_amount_check CHECK (VALUE > 0);

CREATE TABLE shipment (
    shipment_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    billing_email email NOT NULL,
    amount positive_amount NOT NULL
);
```

Keep the domain NULL-tolerant and put `NOT NULL` on each column.

## Proof
```sql runnable target=__test__/test_integrity_domains.sql
CREATE DOMAIN pg_temp.email AS text
    CONSTRAINT email_shape CHECK (VALUE ~ '^[^@[:space:]]+@[^@[:space:]]+$');
CREATE DOMAIN pg_temp.positive_amount AS numeric(12, 2)
    CONSTRAINT positive_amount_check CHECK (VALUE > 0);
CREATE TEMP TABLE shipment (
    billing_email pg_temp.email NOT NULL,
    amount pg_temp.positive_amount NOT NULL
);

DO $$
DECLARE
    v_constraint text;
BEGIN
    INSERT INTO shipment VALUES ('Billing@example.com', 10.00);

    BEGIN
        INSERT INTO shipment VALUES ('billing@example.com', 0);
        RAISE EXCEPTION 'a zero amount was accepted';
    EXCEPTION WHEN check_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'positive_amount_check' THEN
            RAISE EXCEPTION 'wrong constraint fired: %', v_constraint;
        END IF;
    END;

    BEGIN
        INSERT INTO shipment VALUES ('billing@example.com', NULL);
        RAISE EXCEPTION 'a NULL amount was accepted';
    EXCEPTION WHEN not_null_violation THEN
        NULL;
    END;

    ALTER DOMAIN pg_temp.email ADD CONSTRAINT email_lower
        CHECK (VALUE = lower(VALUE)) NOT VALID;
    BEGIN
        INSERT INTO shipment VALUES ('New@example.com', 1.00);
        RAISE EXCEPTION 'an upper-case email passed the NOT VALID constraint';
    EXCEPTION WHEN check_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'email_lower' THEN
            RAISE EXCEPTION 'wrong constraint fired: %', v_constraint;
        END IF;
    END;
    BEGIN
        ALTER DOMAIN pg_temp.email VALIDATE CONSTRAINT email_lower;
        RAISE EXCEPTION 'validation passed over an existing upper-case email';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END $$;
```

## Pitfalls
- A domain `CHECK` accepts NULL, and a domain `NOT NULL` is not reliable: an
  outer join or an empty scalar subquery yields a NULL of the domain type
  that no check sees. Declare `NOT NULL` on the column.
- `ALTER DOMAIN ... ADD CONSTRAINT` scans every column of that domain in
  every table and fails the deploy on the first violating row. Add it
  `NOT VALID` (new and updated values are checked at once), fix the old rows,
  then `ALTER DOMAIN ... VALIDATE CONSTRAINT`.
- Adding or validating a domain constraint fails outright while any array,
  composite or range column uses the domain. Keep domains out of container
  columns, or plan to drop and re-add those columns.
- `ALTER DOMAIN` cannot change the base type. Widening `numeric(12, 2)`
  means a new domain and `ALTER TABLE ... ALTER COLUMN ... TYPE` on every
  column, each a table rewrite.

## Source
- PostgreSQL documentation: CREATE DOMAIN,
  https://www.postgresql.org/docs/current/sql-createdomain.html; ALTER DOMAIN,
  https://www.postgresql.org/docs/current/sql-alterdomain.html.
- pgmi adaptation: the email and amount domains, the test that proves
  rejection, NULL handling and a `NOT VALID` domain constraint.
