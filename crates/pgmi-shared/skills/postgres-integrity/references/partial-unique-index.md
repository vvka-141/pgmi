# Enforce "unique among the live rows" with a partial unique index

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
A rule holds for only some rows: one active subscription per account, one
primary address per person, an email unique among accounts that are not
closed. A plain `UNIQUE` constraint blocks the history the rule allows, so the
obvious fallback is a check in application code or a trigger. Both race: two
sessions each see no conflict and both insert.

## Use when
- Uniqueness is conditional on a column value (`status = 'active'`,
  `closed_at IS NULL`, `is_primary`).
- "At most one row per X may be Y" must hold under concurrent writes.

## Don't use when
- The rule holds for every row: use a `UNIQUE` constraint, which foreign keys
  can reference and a partial index cannot.
- The rule is about overlapping ranges, not equal values: use an exclusion
  constraint (see exclusion-constraint.md in this skill).

## Requires
- PostgreSQL 15+ (any supported release has partial indexes).
- Extension: none.
- Server settings: none.

## Pattern
```sql
CREATE TABLE subscription (
    subscription_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id bigint NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'cancelled')),
    started_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX subscription_one_active_per_account
    ON subscription (account_id)
    WHERE status = 'active';
```

An upsert must repeat the predicate, or PostgreSQL finds no index to infer:

```sql
INSERT INTO subscription (account_id, status) VALUES (42, 'active')
ON CONFLICT (account_id) WHERE status = 'active' DO NOTHING;
```

## Proof
```sql runnable target=__test__/test_integrity_partial_unique_index.sql
CREATE TEMP TABLE subscription (
    subscription_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id bigint NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'cancelled'))
);
CREATE UNIQUE INDEX subscription_one_active_per_account
    ON subscription (account_id) WHERE status = 'active';

DO $$
DECLARE
    v_constraint text;
    v_rows int;
BEGIN
    INSERT INTO subscription (account_id, status)
    VALUES (1, 'cancelled'), (1, 'cancelled'), (1, 'active'), (2, 'active');

    BEGIN
        INSERT INTO subscription (account_id, status) VALUES (1, 'active');
        RAISE EXCEPTION 'a second active subscription for account 1 was accepted';
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
        IF v_constraint IS DISTINCT FROM 'subscription_one_active_per_account' THEN
            RAISE EXCEPTION 'wrong constraint fired: %', v_constraint;
        END IF;
    END;

    INSERT INTO subscription (account_id, status) VALUES (1, 'active')
    ON CONFLICT (account_id) WHERE status = 'active' DO NOTHING;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'upsert inserted % row(s), expected 0', v_rows;
    END IF;
END $$;
```

## Pitfalls
- NULLs never conflict in a unique index. If the key column can be NULL, add
  `NOT NULL`, or on PostgreSQL 15+ declare the index `NULLS NOT DISTINCT`.
- The predicate must be immutable: `WHERE ends_at > now()` is refused. Store
  a status, or use an exclusion constraint over a range.
- A query uses the index only when its `WHERE` implies the predicate, so a
  lookup by `account_id` alone does not use it. Add a plain index if that
  lookup matters.
- Adding the index to a table that already breaks the rule fails the deploy
  with `23505`. Clean the duplicates in the same change, before the index.

## Source
- PostgreSQL documentation: Partial Indexes,
  https://www.postgresql.org/docs/current/indexes-partial.html; INSERT, ON
  CONFLICT, https://www.postgresql.org/docs/current/sql-insert.html.
- pgmi adaptation: the subscription schema, the test that proves both the
  rejection and the upsert inference.
