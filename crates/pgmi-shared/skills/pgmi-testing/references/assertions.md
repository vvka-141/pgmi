## Contents

- Conditions that go silent on NULL
- A total assertion
- Expecting an error
- Comparing result sets
- Assert on state, not on the report

## Conditions that go silent on NULL

An `IF` whose condition is NULL takes no branch. The NULL usually comes from
missing data, which is the state a test exists to catch:

- `SELECT ... INTO v` without `STRICT` leaves `v` NULL when no row matches.
- `->>` and `->` return NULL for an absent key.
- `array_length('{}'::int[], 1)` is NULL, not 0; `cardinality()` returns 0.
- `=`, `<>`, `<`, `LIKE` and `NOT` all pass a NULL operand through.

```sql
-- Each of these passes when the row, key or value is missing
IF v_rec.status <> 'active' THEN ...
IF NOT v_rec.is_valid THEN ...
IF v_doc ->> 'total' <> '42' THEN ...
IF array_length(v_ids, 1) < 1 THEN ...
IF v_body NOT LIKE '%deleted%' THEN ...
```

Write the failing condition so that a NULL also fails: `IS DISTINCT FROM`,
`IS NOT TRUE`, `cardinality()`, `coalesce(..., '')`. `EXISTS` and
`NOT EXISTS` are never NULL. A condition that only steers control flow may
treat NULL as false; this rule is about conditions that raise.

## A total assertion

`STRICT` turns "no row" and "more than one row" into errors, so a lookup that
silently matched nothing cannot pass. Each comparison then uses
`IS DISTINCT FROM`.

```sql runnable target=__test__/test_pgmi_testing_1.sql
DO $$
DECLARE
    v_rec record;
BEGIN
    CREATE TEMP TABLE t1_order (id int PRIMARY KEY, status text, total numeric);
    INSERT INTO t1_order VALUES (1, 'paid', 42.00), (2, NULL, 10);

    SELECT status, total INTO STRICT v_rec FROM t1_order WHERE id = 1;
    IF v_rec.status IS DISTINCT FROM 'paid' OR v_rec.total IS DISTINCT FROM 42 THEN
        RAISE EXCEPTION 'order 1: expected (paid, 42), got (%, %)', v_rec.status, v_rec.total;
    END IF;

    SELECT status INTO STRICT v_rec FROM t1_order WHERE id = 2;
    IF v_rec.status IS NOT NULL THEN
        RAISE EXCEPTION 'order 2: expected no status, got %', v_rec.status;
    END IF;
END $$;
```

Without `STRICT`, check `FOUND` right after the `SELECT ... INTO`.

## Expecting an error

Catch only the condition you expect, check that the intended constraint
raised it, and fail when the statement succeeded. A `RAISE` inside the
protected block is not caught by a handler for a different condition, so the
"was not rejected" failure escapes.

```sql runnable target=__test__/test_pgmi_testing_2.sql
DO $$
DECLARE
    v_constraint text;
BEGIN
    CREATE TEMP TABLE t2_member (
        email text NOT NULL,
        CONSTRAINT t2_member_email_key UNIQUE (email)
    );
    INSERT INTO t2_member VALUES ('a@example.com');

    BEGIN
        INSERT INTO t2_member VALUES ('a@example.com');
        RAISE EXCEPTION 'duplicate email was accepted';
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_constraint = CONSTRAINT_NAME;
    END;

    IF v_constraint IS DISTINCT FROM 't2_member_email_key' THEN
        RAISE EXCEPTION 'expected t2_member_email_key to reject the duplicate, got %', v_constraint;
    END IF;
END $$;
```

The handler rolls back everything the protected block did, so the test can
continue on the state from before the failed statement.

```sql
-- Passes for any error at all, a typo or a missing table included
BEGIN
    PERFORM do_the_thing();
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'rejected as expected';
END;
```

For an error your own code raises, give it an `ERRCODE` and catch that
SQLSTATE, or compare `SQLERRM` exactly; a `LIKE` on the message breaks
silently when the wording changes.

## Comparing result sets

A count proves the size, not the content. Compare both ways, so a missing row
and an unexpected row both fail, and print what differs.

```sql runnable target=__test__/test_pgmi_testing_3.sql
DO $$
DECLARE
    v_diff text;
BEGIN
    CREATE TEMP TABLE t3_item (sku text, qty int);
    INSERT INTO t3_item VALUES ('A', 2), ('B', 0), ('C', 5);

    CREATE TEMP VIEW t3_in_stock AS SELECT sku, qty FROM t3_item WHERE qty > 0;

    WITH expected(sku, qty) AS (VALUES ('A', 2), ('C', 5)),
         actual AS (SELECT sku, qty FROM t3_in_stock),
         diff AS (
             (SELECT 'missing' AS kind, * FROM expected EXCEPT SELECT 'missing', * FROM actual)
             UNION ALL
             (SELECT 'unexpected', * FROM actual EXCEPT SELECT 'unexpected', * FROM expected)
         )
    SELECT string_agg(format('%s %s/%s', kind, sku, qty), ', ' ORDER BY kind, sku)
    INTO v_diff FROM diff;

    IF v_diff IS NOT NULL THEN
        RAISE EXCEPTION 't3_in_stock differs from expected: %', v_diff;
    END IF;
END $$;
```

`EXCEPT` removes duplicates. When duplicates matter, use `EXCEPT ALL`.

## Assert on state, not on the report

A function that returns `{"deleted": 3}` while deleting nothing satisfies
every assertion made on its return value. After calling the code under test,
query the tables it should have changed.
