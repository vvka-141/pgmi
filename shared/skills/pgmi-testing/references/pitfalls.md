## Contents

- Deferred constraints are never checked
- Sequences keep advancing
- The clock stands still
- Row-level security looks open
- Effects outside the transaction
- The test sees the deploy, not the database

## Deferred constraints are never checked

A `DEFERRABLE INITIALLY DEFERRED` constraint is checked at `COMMIT`. A test
never commits, so a violation it should catch is rolled back unseen. Switch
to immediate checking inside the test: `SET CONSTRAINTS ... IMMEDIATE` checks
the pending changes at that moment, and the rollback after the test restores
the deferred mode.

```sql runnable target=__test__/test_pgmi_testing_4.sql
DO $$
DECLARE
    v_rejected boolean := false;
BEGIN
    CREATE TEMP TABLE t4_parent (id int PRIMARY KEY);
    CREATE TEMP TABLE t4_child (
        parent_id int REFERENCES t4_parent DEFERRABLE INITIALLY DEFERRED
    );

    INSERT INTO t4_child VALUES (42);

    BEGIN
        SET CONSTRAINTS ALL IMMEDIATE;
    EXCEPTION WHEN foreign_key_violation THEN
        v_rejected := true;
    END;

    IF NOT v_rejected THEN
        RAISE EXCEPTION 'orphan child row was accepted';
    END IF;
END $$;
```

Constraint triggers created `DEFERRABLE INITIALLY DEFERRED` behave the same.

## Sequences keep advancing

`nextval()` is not undone by a rollback. Never assert a specific generated id,
and never compute one as "the last id plus one". Capture it with
`RETURNING ... INTO` and compare against that.

## The clock stands still

`now()`, `current_timestamp` and `transaction_timestamp()` return the start of
the transaction, and every test runs inside the deploy's one transaction. Rows
inserted by different tests, or seconds apart, get the same default
timestamp. Use `clock_timestamp()` for elapsed time, and pass the time in as a
parameter when the code under test depends on it.

## Row-level security looks open

Superusers, roles with `BYPASSRLS` and, unless the table has
`FORCE ROW LEVEL SECURITY`, the table owner all bypass policies. Tests run as
the deploying role, so a policy test that just selects sees every row and
passes. Switch roles inside the test; the rollback switches back.

```sql
DO $$
BEGIN
    SET LOCAL ROLE app_reader;
    PERFORM set_config('app.user_id', 'alice', true);
    IF EXISTS (SELECT FROM note WHERE owner IS DISTINCT FROM 'alice') THEN
        RAISE EXCEPTION 'policy leaks rows of other users';
    END IF;
END $$;
```

Check which roles and settings this project's policies use before writing one.

## Effects outside the transaction

The rollback covers this database's transaction only. Work sent elsewhere,
through `dblink`, a foreign table or a file written by the server, stays done.
Keep it out of tests.

## The test sees the deploy, not the database

Tests run before the deploy commits, against the schema it just applied, in a
single session. They prove what this deploy produces; they cannot show
concurrency effects, lock waits or what another session sees. Data that
exists only on some environments is not a test fixture: put what a test needs
into its `_setup.sql`.
