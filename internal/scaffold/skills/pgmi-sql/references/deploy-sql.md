# deploy.sql patterns

## Contents

- Selecting files to execute
- Phases and conditions
- Parameters
- Errors: let them through
- Calling what the deploy just created

Column names and the test macro come from `pgmi ai contract`; which statements
share a transaction comes from `pgmi ai`. Check both before relying on either.

## Selecting files to execute

`pg_temp.pgmi_plan_view` holds every loaded file that is not a test, SQL or
not: `README.md`, `pgmi.yaml`, JSON data, an editor's `001_users.sql~`. Join
`pg_temp.pgmi_source_view` and filter on `is_sql_file` before you `EXECUTE`.
Without that guard a stale backup of a migration runs as a migration, and the
deploy still succeeds.

```sql
DO $$
DECLARE
    v_file record;
BEGIN
    FOR v_file IN
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './migrations/%'
        ORDER BY p.execution_order
    LOOP
        RAISE DEBUG 'executing %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

This check is verified to deploy as written, as a test; it shows the plan
really does carry non-SQL files:

```sql runnable target=__test__/test_pgmi_sql_1.sql
DO $$
DECLARE
    v_non_sql int;
    v_executable int;
BEGIN
    SELECT count(*) FILTER (WHERE NOT s.is_sql_file),
           count(*) FILTER (WHERE s.is_sql_file)
    INTO v_non_sql, v_executable
    FROM pg_temp.pgmi_plan_view p
    JOIN pg_temp.pgmi_source_view s ON s.path = p.path;

    IF v_non_sql IS NOT DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'expected non-SQL files (README.md, pgmi.yaml) in pgmi_plan_view';
    END IF;
    IF v_executable IS NOT DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'expected SQL files in pgmi_plan_view';
    END IF;
END $$;
```

Further points:

- The root `deploy.sql` is never loaded. A `deploy.sql` in a subdirectory is
  loaded and is a SQL file, so a broad loop executes it unless you exclude
  its path.
- Test files never appear in `pgmi_source_view` or `pgmi_plan_view`, so a
  deployment loop cannot run one by accident.
- A file with several sort keys has one plan row per key and runs once per
  row, so it must be a re-run file; a run-once ledger keyed by file id
  records it at one key only. Use `DISTINCT` when you check files rather
  than steps.
- Always `ORDER BY`. The plan view gives `execution_order`. If you order
  `pgmi_source_view` by path yourself, write `ORDER BY path COLLATE "C"`:
  under a linguistic collation the order can change with the server's locale.
- Non-SQL files are data. Read them with `content::jsonb` or `content::xml`
  and `SELECT ... INTO STRICT` when the file must exist, so a missing file
  fails the deploy instead of yielding NULL.

## Phases and conditions

Filter by path prefix for phases, and by parameter for environment-only work.
Keep each phase a separate query so the order between phases is explicit:

```sql
DO $$
DECLARE
    v_file record;
    v_env text := COALESCE(current_setting('pgmi.env', true), 'development');
BEGIN
    FOR v_file IN
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file
          AND (p.path LIKE './schema/%'
               OR (p.path LIKE './seed/%' AND v_env IS DISTINCT FROM 'production'))
        ORDER BY (p.path LIKE './seed/%'), p.execution_order
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

A preview mode, phases that commit separately and other transaction
strategies are design decisions; see the pgmi-design skill.

## Parameters

- `--param key=value`, `--params-file` and `PGMI_PARAM_<KEY>` environment
  variables become session settings:
  `current_setting('pgmi.key', true)` returns the text value, or NULL when the
  parameter was not given. Cast it yourself.
- The one-argument form `current_setting('pgmi.key')` raises when the
  parameter is missing. Use it for a value the deploy cannot do without.
- Before reading a parameter directly, check deploy.sql: a project may
  declare its parameters, validate them, set defaults and expose them through
  its own helper or settings prefix. Use that, so a parameter has one source
  of defaults.
- Never `RAISE NOTICE` a parameter that may hold a password or token.

## Errors: let them through

pgmi names the failing file by matching the statement text PostgreSQL reports
against the loaded files, and adds the line for parse errors. That needs the
original error. Leave `EXECUTE` without a handler.

If you must catch, for a warning or cleanup, re-raise with a bare `RAISE;`.
It keeps the SQLSTATE, the message and the statement context.
`RAISE EXCEPTION '...'` drops the context, so pgmi can no longer name the
file, and turns the SQLSTATE into `P0001`, so a caller cannot tell a
retryable `40001` from a bug. Verified to deploy as written:

```sql runnable target=__test__/test_pgmi_sql_2.sql
DO $$
DECLARE
    v_kept text;
    v_flattened text;
BEGIN
    BEGIN
        BEGIN
            EXECUTE 'SELECT 1 / 0';
        EXCEPTION WHEN division_by_zero THEN
            RAISE WARNING 'while executing the file';
            RAISE;
        END;
    EXCEPTION WHEN OTHERS THEN
        v_kept := SQLSTATE;
    END;

    BEGIN
        BEGIN
            EXECUTE 'SELECT 1 / 0';
        EXCEPTION WHEN division_by_zero THEN
            RAISE EXCEPTION 'failed: %', SQLERRM;
        END;
    EXCEPTION WHEN OTHERS THEN
        v_flattened := SQLSTATE;
    END;

    IF v_kept IS DISTINCT FROM '22012' THEN
        RAISE EXCEPTION 'bare RAISE should keep SQLSTATE 22012, got %', v_kept;
    END IF;
    IF v_flattened IS DISTINCT FROM 'P0001' THEN
        RAISE EXCEPTION 'RAISE EXCEPTION should flatten to P0001, got %', v_flattened;
    END IF;
END $$;
```

To continue past a failure on purpose, name the one condition you expect
(`WHEN duplicate_object`, `WHEN undefined_table`). `WHEN OTHERS` that does not
re-raise commits a partial deploy that reports success.

A block with an `EXCEPTION` clause opens a subtransaction each time it is
entered. Keep such blocks out of per-row loops.

## Calling what the deploy just created

PL/pgSQL plans each statement the first time it runs, not when the block is
parsed. A `DO` block may therefore call a function that an earlier `EXECUTE`
in the same block created, without wrapping the call in dynamic SQL:

```sql runnable target=__test__/test_pgmi_sql_6.sql
DO $$
DECLARE
    v_result int;
BEGIN
    EXECUTE $sql$
        CREATE FUNCTION pg_temp.pgmi_sql_twice(p int) RETURNS int
        LANGUAGE sql IMMUTABLE AS $fn$ SELECT p * 2 $fn$
    $sql$;

    v_result := pg_temp.pgmi_sql_twice(21);

    IF v_result IS DISTINCT FROM 42 THEN
        RAISE EXCEPTION 'expected 42, got %', v_result;
    END IF;
END $$;
```
