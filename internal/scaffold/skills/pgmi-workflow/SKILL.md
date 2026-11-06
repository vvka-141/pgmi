---
name: pgmi-workflow
description: How to change, deploy and test a pgmi project — the edit, deploy, read-the-failure loop, where tests and fixtures go, and where to look up the session API instead of guessing. Use before the first deploy in a session, or when unsure where a new file belongs.
---

# Working in a pgmi project

pgmi runs nothing on its own. It loads every project file into session
temporary tables and runs `deploy.sql`, which is this project's own SQL. If a
file does not run, `deploy.sql` did not ask for it. Read `deploy.sql` first;
it is the source of truth for what happens.

## Look up, don't guess

These change with the installed pgmi binary. Ask it; never copy them from
memory or from another project:

| Need | Command |
|---|---|
| Session views, their columns, the test macro and its functions | `pgmi ai contract` |
| Execution contract (what runs in one transaction), CLI basics | `pgmi ai` |
| Exit codes | `pgmi help deploy` |
| Flags of a command | `pgmi <command> --help` |

## The loop

1. **Edit** the SQL file. Place it where `deploy.sql` and the template README
   say files of that kind live. Never leave a backup copy with a SQL
   extension in the project: it is loaded and may run, before the original.
   Every template's `.pgmiignore` keeps out `x (copy).sql`,
   `x (1).sql`, `x - Copy.sql`, `x copy.sql`, `x copy 2.sql` and `x.sql~`;
   any other name runs.
2. **Deploy** to a disposable local database:
   `pgmi deploy . -d <db> --overwrite --force` plus the parameters the
   template README lists. `--overwrite` drops the database first, so use it
   only on databases you can lose.
3. **Read the failure.** The error names the file, and the line when
   PostgreSQL reports one. Fix the cause; do not wrap the statement in an
   exception handler to make the deploy pass.
4. **Repeat** until the deploy and its tests are green, then deploy once more
   without `--overwrite` to prove the project redeploys onto existing data.

Never put a password on the command line. Use a `PGMI_PARAM_<NAME>`
environment variable (read as parameter `<name>`), `--params-file`, or the
connection string's environment variables.

## Tests

- Tests live in `__test__/` directories next to the code they test. pgmi
  does not run them on its own: `deploy.sql` calls `CALL pgmi_test()`, and the
  deploy fails if a test fails.
- `_setup.sql` in a test directory is a fixture. It runs before the tests in
  that directory and its subdirectories.
- Each test runs inside a savepoint and is rolled back, so tests never leave
  data behind and may run on every environment.
- Fail a test with `RAISE EXCEPTION` under a complete condition. Compare with
  `IS DISTINCT FROM`, because `<>` against a missing value is NULL and never
  fails.

A test in that shape, verified to deploy as written as
`__test__/test_workflow_example.sql`:

```sql runnable target=__test__/test_workflow_example.sql
DO $$
DECLARE
    v_found text;
BEGIN
    SELECT name INTO v_found
    FROM (VALUES ('alpha'), ('beta')) AS item(name)
    WHERE name = 'beta';

    IF v_found IS DISTINCT FROM 'beta' THEN
        RAISE EXCEPTION 'expected beta, got %', v_found;
    END IF;
END $$;
```

Mark a block `sql runnable target=<path>` only when it deploys as written.
Plain `sql` blocks are illustration.

## Before you hand back

- The deploy is green twice: once fresh, once onto the deployed database.
- Every behaviour you added has a test that fails when the behaviour is
  removed.
- No secret, password or token is in a committed file.
