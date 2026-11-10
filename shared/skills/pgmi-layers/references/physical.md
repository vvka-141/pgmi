# The physical layer

Tables, keys, constraints, indexes and row policies, written so the semantic
layer can trust them.

## Contents

1. Run-once files
2. Final form, then frozen
3. Constraints before code
4. Triggers

## 1. Run-once files

A physical file opens with a `<pgmi-meta>` block marking it run-once, and
holds plain DDL:

```sql
/*
<pgmi-meta id="6f1d2c3a-5b4e-4f70-9a81-2c3d4e5f6a7b" idempotent="false">
  <description>Documents and their reviews</description>
</pgmi-meta>
*/
CREATE TABLE document (
    document_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title       text NOT NULL CONSTRAINT document_title_check CHECK (btrim(title) <> '')
);
```

- The `id` is the file's identity: a fresh UUID per file, never copied. It
  lets the deploy tell a moved file from a new one.
- `deploy.sql` records each run-once file it ran and skips it afterwards.
- Views and functions never go in a run-once file: a later deploy could not
  change them. They go in the semantic layer's re-run files.

## 2. Final form, then frozen

While every database that ran a table's file may be recreated, the table is
written once, complete, and a change edits that file:

| Not | But |
|---|---|
| `CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS` | plain `CREATE TABLE` with every column |
| `ALTER TABLE ... ADD` in the same release | the column in the `CREATE TABLE` |
| a backfill for a column added a moment ago | the column's default or constraint |

`IF NOT EXISTS` checks that a name exists, not that the object matches the
file. `CREATE TABLE IF NOT EXISTS` over an older version of the table skips it
with a NOTICE, missing columns and all, and reports success; `CREATE INDEX IF
NOT EXISTS` skips an index of that name whatever its columns. A change to an
object that already exists is written as the change itself (`ALTER TABLE ...
ADD COLUMN`), and a test asserts the new shape.

Recreate the development database to take the edit:
`pgmi deploy . -d <db> --overwrite --force` (plus the parameters the README lists). Once a database that keeps its data has
run the file, the file is frozen: `deploy.sql` refuses an edited, moved or
removed run-once file and names it. Comments, whitespace outside literals and
keyword case are not edits (the checksum ignores them). The change is a new run-once file
sorted after the old one, with a new id:

```sql
ALTER TABLE document ADD COLUMN archived_at timestamptz;
```

## 3. Constraints before code

The database refuses an invalid row whoever writes it, under concurrency,
forever. Code refuses only on the paths that call it.

- `NOT NULL` and `CHECK` for one row's rules, named so a test can tell which
  one fired (`CONSTRAINT document_title_check CHECK (...)`).
- Several columns that must agree: one `CHECK` across them
  (`CHECK ((status = 'done') = (done_at IS NOT NULL))`).
- "One open per key": a partial unique index (postgres-integrity).
- No overlapping periods: an exclusion constraint (postgres-integrity).
- Every foreign key has an index leading with its columns
  (postgres-indexing); without one a delete of the parent scans the child.
- Keys a caller sees are stable: an identity or a UUID, not a name a user
  may change.

## 4. Triggers

A trigger is the last resort, after constraints, writers and views. It needs
a stated reason. A trigger that keeps a second copy of rows in step is never
right: derive the copy in a view. An audit trail is the common legitimate
case (postgres-change-capture).
