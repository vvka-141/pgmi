# Index a computed value with an expression index

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
Lookups compare a derived value, not the stored one: emails matched without
regard to case, a document found by one key inside a jsonb column. An index
on the column itself is useless for `WHERE lower(email) = $1`, so the query
scans the table. The common workaround, a second "normalized" column kept in
sync by application code or a trigger, drifts.

## Use when
- Queries filter on a function of a column (`lower(email)`,
  `payload ->> 'order_ref'`) and the stored value must stay as written.
- A rule holds on the derived value: emails unique regardless of case (a
  `UNIQUE` expression index enforces it).

## Don't use when
- The expression is not immutable (depends on the time zone, the current
  time, another table): PostgreSQL refuses it. Store the derived value in a
  generated column and index that.
- The query searches inside jsonb by containment (`payload @> '{...}'`): use
  a GIN index on the column instead.

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE account (
    account_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email      text NOT NULL,
    profile    jsonb NOT NULL DEFAULT '{}'
);

CREATE UNIQUE INDEX account_email_lower_key ON account (lower(email));
CREATE INDEX account_profile_ref_idx ON account ((profile ->> 'external_ref'));

SELECT account_id FROM account WHERE lower(email) = lower($1);
SELECT account_id FROM account WHERE profile ->> 'external_ref' = $1;
```

The query must repeat the indexed expression exactly. `WHERE email ILIKE $1`
or `WHERE profile -> 'external_ref' = to_jsonb($1)` cannot use these indexes.

## Proof
```sql runnable target=__test__/test_indexing_expression_index.sql
CREATE TEMP TABLE account (
    account_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX account_email_lower_key ON account (lower(email));

DO $$
DECLARE
    v_plan text;
    v_constraint text;
BEGIN
    INSERT INTO account (email) VALUES ('Ada@Example.com');
    BEGIN
        INSERT INTO account (email) VALUES ('ada@example.COM');
        RAISE EXCEPTION 'an email differing only in case was accepted';
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'account_email_lower_key' THEN
            RAISE EXCEPTION 'wrong constraint fired: %', v_constraint;
        END IF;
    END;

    PERFORM set_config('enable_seqscan', 'off', true);
    EXECUTE 'EXPLAIN (FORMAT JSON) SELECT account_id FROM account WHERE lower(email) = ''ada@example.com''' INTO v_plan;
    IF v_plan NOT LIKE '%"account_email_lower_key"%' THEN
        RAISE EXCEPTION 'the matching expression did not use the index: %', v_plan;
    END IF;
    EXECUTE 'EXPLAIN (FORMAT JSON) SELECT account_id FROM account WHERE email = ''ada@example.com''' INTO v_plan;
    IF v_plan LIKE '%"account_email_lower_key"%' THEN
        RAISE EXCEPTION 'a bare column comparison used the expression index: %', v_plan;
    END IF;

    BEGIN
        CREATE INDEX account_created_day_idx ON account ((created_at::date));
        RAISE EXCEPTION 'a non-immutable index expression was accepted';
    EXCEPTION WHEN invalid_object_definition THEN
        NULL;
    END;
END $$;
```

## Pitfalls
- `timestamptz::date` depends on the `TimeZone` setting, so it is refused
  (`42P17`). Index `(created_at AT TIME ZONE 'UTC')::date` and query with the
  same expression.
- Marking a wrapper function `IMMUTABLE` when it is not (it reads a table or
  a setting) silences the error and corrupts the index when the outside value
  changes. Indexes are not rebuilt when a function's body is replaced.
- The planner has no statistics for the expression until the table is
  analyzed after the index exists; estimates on it are guesses until then.
- Each expression index is computed on every insert and every update of its
  columns.

## Source
- PostgreSQL documentation: Indexes on Expressions,
  https://www.postgresql.org/docs/current/indexes-expressional.html;
  CREATE INDEX, https://www.postgresql.org/docs/current/sql-createindex.html;
  Function Volatility Categories,
  https://www.postgresql.org/docs/current/xfunc-volatility.html.
- pgmi adaptation: the account schema, the test that proves case-insensitive
  uniqueness, which query shapes use the index, and the immutability refusal.
