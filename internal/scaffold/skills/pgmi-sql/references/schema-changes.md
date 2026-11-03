# Schema changes

## Contents

- Re-run or run once: deploy.sql decides
- `IF NOT EXISTS` checks names, not shape
- Changing objects that already exist
- Locks on a live database
- Statements that need the top level

## Re-run or run once: deploy.sql decides

Whether a file runs on every deploy or once per database is the project's
choice, written in deploy.sql. Read it first; the two models need different
SQL.

**The default: structure runs once.** Both templates keep tables, keys and
indexes in run-once files (pgmi-layers). Plain DDL is right, and a change to
a table that a kept database already holds is a new run-once file, with a
new id, sorted after the one that created it:

```sql
ALTER TABLE account ADD COLUMN email text;
```

Editing an applied file changes nothing on the databases that ran it, and
deploy.sql refuses the edit. While every database that ran the file may
still be recreated, edit the file itself instead.

**The alternative: structure re-runs on every deploy.** It fits a project
whose deploy.sql has no run-once ledger, or a file it deliberately re-runs
(a schema owned by an extension, a sandbox rebuilt on every deploy). Every
statement must then be safe the second time: `CREATE OR REPLACE` for code,
guarded DDL for structure, and an explicit change for every object whose
shape moved on. The sections below are for that model.

Either way, prove it: deploy onto a fresh database, then again onto the
deployed one.

## `IF NOT EXISTS` checks names, not shape

`CREATE TABLE IF NOT EXISTS` over an older version of the table skips it with
a NOTICE, missing columns and all. `CREATE INDEX IF NOT EXISTS` skips an
index with that name whatever its columns. In a re-run file, add the change
itself after the create. Verified to deploy as written:

```sql runnable target=__test__/test_pgmi_sql_5.sql
CREATE TABLE pgmi_sql_account (id int PRIMARY KEY);

CREATE TABLE IF NOT EXISTS pgmi_sql_account (id int PRIMARY KEY, email text);

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_attribute
               WHERE attrelid = 'pgmi_sql_account'::regclass AND attname = 'email' AND NOT attisdropped) THEN
        RAISE EXCEPTION 'CREATE TABLE IF NOT EXISTS was expected to skip an existing table';
    END IF;
END $$;

ALTER TABLE pgmi_sql_account ADD COLUMN IF NOT EXISTS email text;
ALTER TABLE pgmi_sql_account ADD COLUMN IF NOT EXISTS email text;

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_attribute
                   WHERE attrelid = 'pgmi_sql_account'::regclass AND attname = 'email' AND NOT attisdropped) THEN
        RAISE EXCEPTION 'expected ADD COLUMN IF NOT EXISTS to add email';
    END IF;
END $$;
```

## Changing objects that already exist

In a run-once file each change is plain DDL. In a re-run file guard it:

- Columns: `ADD COLUMN IF NOT EXISTS`, `DROP COLUMN IF EXISTS`. A type change
  or a new `NOT NULL` needs its own guarded statement and, for existing rows,
  a backfill first.
- Constraints have no `IF NOT EXISTS`. Guard with the catalog, and match the
  table as well as the name, because constraint names are unique only per
  table:

```sql
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_constraint
                   WHERE conrelid = 'public.account'::regclass
                     AND conname = 'account_email_key') THEN
        ALTER TABLE public.account ADD CONSTRAINT account_email_key UNIQUE (email);
    END IF;
END $$;
```

- Data backfills are one set-based `UPDATE ... WHERE <not yet done>`, so a
  second run touches nothing.
- `DROP ... CASCADE` silently drops every dependent object, with its grants.
  Name the dependants and recreate them in the same change instead.

## Locks on a live database

`ALTER TABLE` takes an `ACCESS EXCLUSIVE` lock. While it waits behind a long
transaction, every query on that table queues behind it. Bound the wait so
the deploy fails instead of stalling the application:

```sql
SET LOCAL lock_timeout = '5s';
ALTER TABLE account ADD COLUMN IF NOT EXISTS email text;
```

`SET LOCAL` lasts until the end of the current transaction. A lock timeout
fails with SQLSTATE `55P03`; deploy again when the blocker is gone. Check
first whether deploy.sql already sets one.

## Statements that need the top level

`CREATE INDEX CONCURRENTLY`, `VACUUM` and `CREATE DATABASE` cannot run inside
a function, a `DO` block or `EXECUTE` (`25001 ... cannot be executed from a
function`), nor inside a transaction block. A project file run by a plan loop
can never contain them. Put each one as a top-level statement in deploy.sql,
where `pgmi ai` says statements run in their own transaction, and write it so
a re-run is safe:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS account_email_idx ON account (email);
```

A concurrent build that fails leaves an index marked invalid, and
`IF NOT EXISTS` then skips it on every later deploy. Fail the deploy when one
exists:

```sql
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_index WHERE NOT indisvalid
               AND indexrelid = 'public.account_email_idx'::regclass) THEN
        RAISE EXCEPTION 'account_email_idx is invalid: DROP INDEX CONCURRENTLY it and deploy again';
    END IF;
END $$;
```
