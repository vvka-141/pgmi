# Close PUBLIC EXECUTE on functions and procedures

## Contents
Problem · Use when · Don't use when · Requires · Pattern · Proof · Pitfalls · Source

## Problem
PostgreSQL grants `EXECUTE` to `PUBLIC` on every new function and procedure,
so any role that can reach the schema can call it. Tables start closed and
routines start open: a maintenance routine, or a `SECURITY DEFINER` function
that runs with its owner's rights, is callable by every role in the cluster
unless something revokes the default.

## Use when
- Adding or reviewing a function or procedure that not every role may call.
- Writing a `SECURITY DEFINER` function.

## Don't use when
- The rule is about rows, not routines: use row-level security (see
  row-level-security.md in this skill).

## Requires
- PostgreSQL 15+.
- Extension: none.
- Server settings: none.

## Pattern
Read the project's `deploy.sql` first. Both pgmi templates close the default
there: the basic template runs
`ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC` for the
deploying role before any project file runs; the advanced template sets the
same default for the role that owns the schema objects and, at the end of the
deploy, revokes `EXECUTE` from `PUBLIC` on every routine in its application
schemas that still has it. Keep that mechanism; do not add a second one. Each
file then grants what its routines need:

```sql
CREATE FUNCTION catalog.publish_release(p_release_id bigint) RETURNS void
LANGUAGE sql SECURITY DEFINER
SET search_path = catalog, pg_temp
AS $$ UPDATE catalog.release SET published = true WHERE release_id = p_release_id $$;

REVOKE EXECUTE ON FUNCTION catalog.publish_release(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION catalog.publish_release(bigint) TO editor;
```

The explicit `REVOKE` makes the file correct even where the default was not
closed, at no cost where it was. A `SECURITY DEFINER` function sets
`search_path` to trusted schemas with `pg_temp` last, so no caller can shadow
the objects it uses. A project may fix the search_path members and each
role's grant list in its tests; follow those.

## Proof
The test creates a role and lets it create routines in its own schema, so
none of the project's default privileges apply.

```sql runnable target=__test__/test_security_function_privileges.sql
DO $$
BEGIN
    RESET ROLE;
    CREATE ROLE fn_probe_owner NOLOGIN;
    CREATE SCHEMA fn_probe AUTHORIZATION fn_probe_owner;
    SET LOCAL ROLE fn_probe_owner;

    CREATE FUNCTION fn_probe.open_by_default() RETURNS int LANGUAGE sql AS 'SELECT 1';
    IF NOT has_function_privilege('public', 'fn_probe.open_by_default()', 'EXECUTE') THEN
        RAISE EXCEPTION 'a new function was not executable by PUBLIC';
    END IF;
    REVOKE EXECUTE ON FUNCTION fn_probe.open_by_default() FROM PUBLIC;
    IF has_function_privilege('public', 'fn_probe.open_by_default()', 'EXECUTE') THEN
        RAISE EXCEPTION 'REVOKE ... FROM PUBLIC did not close the function';
    END IF;

    ALTER DEFAULT PRIVILEGES IN SCHEMA fn_probe REVOKE EXECUTE ON ROUTINES FROM PUBLIC;
    CREATE FUNCTION fn_probe.still_open() RETURNS int LANGUAGE sql AS 'SELECT 1';
    IF NOT has_function_privilege('public', 'fn_probe.still_open()', 'EXECUTE') THEN
        RAISE EXCEPTION 'the IN SCHEMA form unexpectedly revoked the global default';
    END IF;

    ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;
    CREATE PROCEDURE fn_probe.closed() LANGUAGE sql AS 'SELECT 1';
    IF has_function_privilege('public', 'fn_probe.closed()', 'EXECUTE')
       OR NOT has_function_privilege('public', 'fn_probe.still_open()', 'EXECUTE') THEN
        RAISE EXCEPTION 'default privileges must close new routines and leave existing ones';
    END IF;
    RESET ROLE;
END $$;
```

## Pitfalls
- `ALTER DEFAULT PRIVILEGES ... IN SCHEMA s REVOKE ... FROM PUBLIC` does
  nothing: per-schema defaults only add to the global ones. Use the form
  without `IN SCHEMA`.
- Default privileges apply only to routines created afterwards, and only to
  those created by the role they name (the current role when `FOR ROLE` is
  omitted). Routines created earlier, or by another role, keep `PUBLIC`.
- `CREATE OR REPLACE FUNCTION` keeps the existing grants, so a later
  `REVOKE` in the same file is still needed when the first version was open.
- A `SECURITY DEFINER` function without `SET search_path` resolves names on
  the caller's path, where the caller's temporary tables come first and can
  shadow any table the function names unqualified.

## Source
- PostgreSQL documentation: Privileges,
  https://www.postgresql.org/docs/current/ddl-priv.html; ALTER DEFAULT
  PRIVILEGES, https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html;
  CREATE FUNCTION, Writing SECURITY DEFINER Functions Safely,
  https://www.postgresql.org/docs/current/sql-createfunction.html.
- pgmi adaptation: how each pgmi template already closes the default, and the
  test that proves the per-function, per-schema and global forms.
