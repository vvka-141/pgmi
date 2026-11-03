# Functions

## Contents

- SQL or PL/pgSQL
- Volatility and STRICT
- Changing a function that already exists
- Dynamic SQL
- Who may execute a function
- SECURITY DEFINER
- Testing a pure function where it is defined

## SQL or PL/pgSQL

Write `LANGUAGE sql` when the body is one query, or a few with no control
flow. Reach for PL/pgSQL for variables, branching, exception handling and
dynamic SQL. A PL/pgSQL `FOR ... LOOP` that changes rows one at a time is
almost always one set-based statement:

```sql
-- Instead of looping over account and updating each row:
UPDATE account a
SET last_login_at = s.last_seen
FROM (SELECT account_id, max(seen_at) AS last_seen FROM session GROUP BY account_id) s
WHERE s.account_id = a.id
  AND a.last_login_at IS DISTINCT FROM s.last_seen;
```

## Volatility and STRICT

- `IMMUTABLE`: the result depends only on the arguments. Never on a table,
  `now()`, `current_setting()` or the session time zone. A wrongly
  `IMMUTABLE` function gives stale results through indexes and constant
  folding, with no error.
- `STABLE`: reads the database or settings, and changes nothing.
- `VOLATILE` (the default): anything else, including every function that
  writes.
- `STRICT` returns NULL without running the body when any argument is NULL.
  A `STRICT` validation function that should raise on bad input therefore
  passes NULL silently. Use it only when NULL in means NULL out.

## Changing a function that already exists

`CREATE OR REPLACE FUNCTION` replaces the body and keeps the owner and the
grants. It cannot change:

- **The return type**, including `OUT` parameters and `RETURNS TABLE`
  columns. It fails with `42P13 cannot change return type of existing
  function`.
- **The argument types.** A new argument list creates a second function
  beside the old one. Callers whose arguments still match keep calling the
  old body, and the old one keeps its grants.

In both cases drop the old signature in the same change, before the new
definition:

```sql
DROP FUNCTION IF EXISTS get_account(int);
CREATE FUNCTION get_account(p_id bigint) RETURNS account
LANGUAGE sql STABLE AS $$
    SELECT * FROM account WHERE id = p_id
$$;
```

A view or another function that depends on the old one blocks the drop. Drop
and recreate the dependants in the same file rather than reaching for
`CASCADE`, which drops them silently, grants included.

## Dynamic SQL

Quote every identifier with `%I` and every value with `%L`, or better, pass
values with `USING` so they are never spliced into the text. `%s` inserts
text verbatim; use it only for a fragment your own code built. Verified to
deploy as written:

```sql runnable target=__test__/test_pgmi_sql_3.sql
DO $$
DECLARE
    v_table text := 'pgmi sql "demo"';
    v_name text := 'O''Brien';
    v_found text;
BEGIN
    EXECUTE format('CREATE TEMP TABLE %I (name text NOT NULL)', v_table);
    EXECUTE format('INSERT INTO %I (name) VALUES (%L)', v_table, v_name);
    EXECUTE format('SELECT name FROM %I WHERE name = $1', v_table)
        INTO v_found
        USING v_name;

    IF v_found IS DISTINCT FROM v_name THEN
        RAISE EXCEPTION 'expected %, got %', v_name, v_found;
    END IF;
END $$;
```

For a qualified name pass schema and object as two `%I` arguments
(`%I.%I`); one `%I` around `'app.account'` quotes the dot into the name.
For a function signature from the catalog, `oid::regprocedure::text` is
already quoted.

## Who may execute a function

PostgreSQL grants `EXECUTE` to `PUBLIC` on every new function and procedure,
so any role that can reach the schema can call it. Three facts decide how to
close that:

1. `ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC` covers
   only routines created afterwards, by the role it names (the current role
   when it names none).
2. Only the form without `IN SCHEMA` removes the built-in default. An
   `IN SCHEMA` revoke cannot take away a privilege granted globally, so it
   changes nothing here.
3. `CREATE OR REPLACE` keeps a function's existing grants. A function created
   before the default was closed stays open until an explicit
   `REVOKE EXECUTE ON FUNCTION ... FROM PUBLIC`.

Check whether this project's deploy.sql already closes the default, then
`GRANT EXECUTE` to each role that calls the function. Verified to deploy as
written:

```sql runnable target=__test__/test_pgmi_sql_4.sql
CREATE SCHEMA pgmi_sql_acl;
CREATE FUNCTION pgmi_sql_acl.answer() RETURNS int LANGUAGE sql AS 'SELECT 1';
GRANT EXECUTE ON FUNCTION pgmi_sql_acl.answer() TO PUBLIC;

ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;
CREATE OR REPLACE FUNCTION pgmi_sql_acl.answer() RETURNS int LANGUAGE sql AS 'SELECT 42';

DO $$
BEGIN
    IF NOT has_function_privilege('public', 'pgmi_sql_acl.answer()', 'EXECUTE') THEN
        RAISE EXCEPTION 'expected the replaced function to keep its PUBLIC grant';
    END IF;

    REVOKE EXECUTE ON FUNCTION pgmi_sql_acl.answer() FROM PUBLIC;

    IF has_function_privilege('public', 'pgmi_sql_acl.answer()', 'EXECUTE') THEN
        RAISE EXCEPTION 'expected an explicit REVOKE to close the existing function';
    END IF;
END $$;
```

## SECURITY DEFINER

A `SECURITY DEFINER` function runs with its owner's privileges, whoever calls
it. Prefer the default, `SECURITY INVOKER`. When you need definer rights:

- Pin the search path in the definition, with `pg_temp` last:
  `SET search_path = app, pg_temp`. Otherwise the caller decides where
  unqualified names resolve, and can put their own objects first.
- Check inside the body that the caller may do what is asked.
- Revoke `EXECUTE` from `PUBLIC` and grant it only to the roles that call it.

## Testing a pure function where it is defined

For an `IMMUTABLE` function that reads no table, a `DO` block right after the
definition, in the same file, checks it on every deploy that runs the file.
Anything that reads or writes data belongs in a `__test__/` directory instead;
see the pgmi-workflow skill.

```sql
CREATE OR REPLACE FUNCTION slugify(p text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT trim(BOTH '-' FROM lower(regexp_replace(p, '[^a-zA-Z0-9]+', '-', 'g')))
$$;

DO $$
BEGIN
    IF slugify('Hello, World!') IS DISTINCT FROM 'hello-world' THEN
        RAISE EXCEPTION 'slugify: got %', slugify('Hello, World!');
    END IF;
END $$;
```
