## Contents

- What the macro expands to
- Fixture scoping, proved
- Separate test trees
- Filtering by pattern
- Callbacks
- Inspecting the plan

## What the macro expands to

`CALL pgmi_test()` is not a procedure. pgmi replaces it in `deploy.sql`, before
the script reaches PostgreSQL, with plain SQL generated from the test plan. For
a directory with a fixture and two tests it has this shape (callback calls left
out; savepoint names are pgmi's own):

```sql
SAVEPOINT dir;                 -- the directory
-- _setup.sql runs here
SAVEPOINT tests;               -- the state every test starts from
-- test_a.sql runs here
ROLLBACK TO SAVEPOINT tests;
-- test_b.sql runs here
ROLLBACK TO SAVEPOINT tests;
-- subdirectories run here, each inside its own pair of savepoints
ROLLBACK TO SAVEPOINT dir;     -- teardown: the fixture is gone
RELEASE SAVEPOINT dir;
```

Consequences:

- The expansion is top-level `SAVEPOINT` statements, which PostgreSQL accepts
  only inside an explicit transaction block. The macro is also only recognised
  at the top level of `deploy.sql`: inside a `DO` body or a string it is left
  alone, and inside a plan file it is never expanded.
- A test's changes, including DDL, `SET LOCAL` and `set_config(..., true)`,
  are undone before the next test. A fixture's changes last until its
  directory's teardown.
- The scaffolded `deploy.sql` files wrap the call in their own
  `SAVEPOINT _tests; ... ROLLBACK TO SAVEPOINT _tests;`, so no test or fixture
  survives into the `COMMIT`.
- No test is caught by pgmi. The first error aborts the transaction, and the
  rest of the deploy, including its `COMMIT`, does not happen.

## Fixture scoping, proved

These four files are verified to deploy as written. The fixture creates a
temporary table; the first test damages it; the second sees it intact; the
nested test sees the parent's fixture.

```sql runnable target=__test__/pgmi_testing_demo/_setup.sql
CREATE TEMP TABLE demo_account (id int PRIMARY KEY, balance numeric NOT NULL);
INSERT INTO demo_account VALUES (1, 100), (2, 50);
```

```sql runnable target=__test__/pgmi_testing_demo/test_a_damages_fixture.sql
DELETE FROM demo_account WHERE id = 1;
UPDATE demo_account SET balance = 0 WHERE id = 2;
CREATE TEMP TABLE demo_leftover (x int);
```

```sql runnable target=__test__/pgmi_testing_demo/test_b_sees_fixture_intact.sql
DO $$
DECLARE
    v_total numeric;
BEGIN
    SELECT sum(balance) INTO STRICT v_total FROM demo_account;
    IF v_total IS DISTINCT FROM 150 THEN
        RAISE EXCEPTION 'fixture not restored: expected total 150, got %', v_total;
    END IF;
    IF to_regclass('pg_temp.demo_leftover') IS NOT NULL THEN
        RAISE EXCEPTION 'a table created by an earlier test survived its rollback';
    END IF;
END $$;
```

```sql runnable target=__test__/pgmi_testing_demo/nested/test_c_inherits_fixture.sql
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM demo_account WHERE id = 1 AND balance = 100) THEN
        RAISE EXCEPTION 'nested directory does not see the parent fixture';
    END IF;
END $$;
```

## Separate test trees

Only the `__test__/` path segment links directories. `./__test__/sub/` is a
child of `./__test__/`; `./orders/__test__/` is not a child of either, so
`./__test__/_setup.sql` never runs for it. Each tree root runs in path order.
Give a tree that needs shared data its own `_setup.sql`.

## Filtering by pattern

- The first argument is a POSIX regular expression (PostgreSQL `~`) matched
  against each test file's path, for example `./orders/__test__/test_refund.sql`.
  `NULL` or `''` means every test.
- Fixtures are not matched: the fixtures of every directory on the way to a
  matched test run regardless, and a directory with no matched test in it or
  below it is skipped, fixture included.
- An invalid regex and a pattern that matches no test both fail the deploy,
  and so does `CALL pgmi_test()` in a project that has no tests.
- The pattern sits in a SQL string literal but is read as written: one
  backslash, no doubled quotes.

## Callbacks

`CALL pgmi_test('<pattern>', '<function>')` sends test lifecycle events to a
function you name instead of the default, which prints `[pgmi] Test: <path>`
and similar NOTICE lines. Use one to change what the run prints, for example
to quiet noisy fixtures or to emit TAP. The event type and the list of events
come from `pgmi ai contract`.

- The name must be a plain identifier, optionally schema-qualified; quoted
  names are rejected.
- The function must exist when the expanded SQL reaches it, so create it in
  `deploy.sql` before the call, usually in `pg_temp`.
- Events fired around a test run inside that test's savepoint, so rows a
  callback writes are rolled back with the test. Report with `RAISE`, not
  `INSERT`.
- Check whether this project's `deploy.sql` already passes a callback before
  adding one.

## Inspecting the plan

To see what a pattern selects without running it, query the plan function
from `deploy.sql` before the call and print it with `RAISE NOTICE`. Its name
and columns come from `pgmi ai contract`. Remove the probe afterwards.
