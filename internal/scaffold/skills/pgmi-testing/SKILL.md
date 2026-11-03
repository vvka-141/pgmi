---
name: pgmi-testing
description: Writes, places and debugs tests in a pgmi project. Covers where test files and _setup.sql fixtures go in __test__/ directories, how CALL pgmi_test() runs them inside savepoints and rolls them back, what each test can see, running a subset by pattern, test callbacks, and assertions that cannot pass by accident. Use when adding or reviewing a test, when a test passes but should fail, when a test fails only next to other tests, or when filtering or reporting a test run.
---

# Testing a pgmi project

The edit, deploy, read-the-failure loop is in the pgmi-workflow skill. This
skill is about how tests run and how to write ones that fail when they should.
Names of the session views and functions come from `pgmi ai contract`; never
copy them from memory.

## The model to keep in mind

- **What is a test.** Every SQL file under a `__test__/` or `__tests__/`
  directory, whatever its name. `_setup.sql` (or `_setup.psql`) is that
  directory's fixture. Non-SQL files there, such as a README, are ignored.
  Test files never enter the deploy plan.
- **When it runs.** Only where `deploy.sql` says `CALL pgmi_test()`. Read this
  project's `deploy.sql` for where, with which pattern and callback. The
  scaffolded ones call it after the deploy's own work, inside the deploy's
  transaction, so a test sees the just-deployed, still-uncommitted schema, and
  one failing test fails the whole deploy with nothing committed.
- **What it sees.** Each directory gets a savepoint. Its fixture runs once,
  then each test runs and is rolled back to the state right after the fixture.
  Tests in one directory never see each other's changes. A subdirectory sees
  the fixtures of its parent directories, never their tests' changes.
  Separate `__test__/` folders (`./__test__/`, `./lib/__test__/`) are separate
  trees: a fixture never reaches across them.
- **Order.** Within a directory, by file name in byte order; a directory's
  tests run before its subdirectories. Since each test starts from the fixture
  state, order is for reading output, not for correctness.
- **How a file executes.** As dynamic SQL inside a `DO` block. Several
  statements per file are fine. Transaction control (`BEGIN`, `COMMIT`,
  `SAVEPOINT`, `ROLLBACK`), `VACUUM`, `CREATE INDEX CONCURRENTLY` and psql
  meta-commands are not. The first error ends the deploy.
- **Transaction.** `CALL pgmi_test()` needs an explicit top-level transaction
  around it; see `pgmi ai` for the execution contract.

Savepoint layout, nesting, filtering and callbacks:
[execution model](references/execution-model.md).

## Where a new test goes

| Situation | Put it |
|---|---|
| Testing code in directory `X/` | `X/__test__/test_<behaviour>.sql` |
| Several tests need the same rows or helpers | That directory's `_setup.sql`; create rows through the project's own functions where it has them, so fixtures obey the same rules |
| Data only one test needs | Inside that test |
| Some tests need a different fixture | A subdirectory with its own `_setup.sql`; it inherits the parent's |
| A helper function for tests | Create it in `pg_temp` in `_setup.sql` |
| Cleanup after tests | Nothing: rollback is the teardown |

There is no teardown file. `_teardown.sql` or `cleanup.sql` is just another
test, and `_teardown.sql` sorts before `test_*`, so it runs first. A shared
`helpers.sql` in `__test__/` also runs as a test.

`deploy.sql` may restrict where tests live; check it before adding a
directory. Tests run on every deploy in every environment, so a test must not
depend on rows that only a development seed inserts.

## Writing the assertion

- Fail with `RAISE EXCEPTION`, naming expected and actual. Never `ASSERT`:
  `plpgsql.check_asserts = off` turns it into a no-op.
- Pass with at most one `RAISE NOTICE` line, the test's summary. Anything
  else goes to `RAISE DEBUG` (shown with `--verbose`), so a green deploy
  stays readable.
- Make every failing condition total: `IS DISTINCT FROM`, not `<>` or `=`,
  and `SELECT ... INTO STRICT` or a `FOUND` check, not a bare `INTO`. An `IF`
  whose condition is NULL takes no branch and the test passes.
- Assert on the database state, re-queried, not on what the code under test
  returns about itself.
- Compare result sets in both directions (`EXCEPT` each way), so a missing
  row and an extra row both fail.
- Expected errors: catch the one condition you expect, check which
  constraint raised it, and fail when nothing was raised. Never
  `WHEN OTHERS`. Through an API, catch nothing: assert the returned status
  and error code.

Patterns, each verified to deploy as written: [assertions](references/assertions.md).

## What the rollback does not undo

Sequence values, effects outside the database, and the transaction clock.
Deferred constraints never get checked, and the deploy role usually bypasses
row-level security. Each one hides a real bug:
[pitfalls](references/pitfalls.md).

## Running a subset

`CALL pgmi_test('<POSIX regex>')` runs the tests whose path matches, together
with the fixtures of the directories above them. Paths look like
`./billing/__test__/test_refund.sql`, so `'^\./billing/'` selects one area. It
is a regex, not `LIKE`: one backslash escapes, and the pattern cannot contain
a single quote. A pattern that matches nothing fails
the deploy, as does a project with no tests, so add no guard of your own. Put
the full call back before handing the change back.

## Before you hand back

- Break the behaviour once and watch the deploy fail on your test. A test that
  has never failed proves nothing.
- With the scaffolded callbacks, the deploy output has a `[pgmi] Test:` line
  for the file, which proves it ran.
- No condition that raises can evaluate to NULL.
- At most one NOTICE line per test file.
- The test depends on no other test's changes, no sequence value and no
  development-only data.
