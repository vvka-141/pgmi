# Redeploying onto existing data

## Contents

- Re-run or apply once
- Changing a shape that already exists
- Reference and seed data
- Tracking applied files

## Re-run or apply once

Whether a file runs on every deploy or only once is decided in this
project's `deploy.sql`, sometimes per directory or per file. Read it before
adding a file, and put the file where files of its kind run.

- **Re-run file.** The second run must change nothing and fail nothing.
  `CREATE OR REPLACE` for code; guarded or conditional statements for
  everything else.
- **Run-once file.** It is never retried, so it must be correct against the
  rows that exist when it first runs: adding a `NOT NULL` column to a full
  table needs a default or a backfill in the same file. Never edit it after
  it ran; write a new file. Depending on the project, an edited run-once file
  is skipped or fails the deploy, and neither applies the edit.

## Changing a shape that already exists

`CREATE TABLE IF NOT EXISTS` does not change an existing table (the pgmi-layers
skill, references/physical.md, says why). Write the change itself, make it
converge, and assert the result. The test below shows both halves;
a temporary table stands in for the deployed one:

```sql runnable target=__test__/test_pgmi_design_1.sql
DO $$
DECLARE
    v_change constant text := $change$
        ALTER TABLE design_account ADD COLUMN IF NOT EXISTS email_normalized text;
        UPDATE design_account SET email_normalized = lower(email)
        WHERE email_normalized IS NULL;
        ALTER TABLE design_account ALTER COLUMN email_normalized SET NOT NULL;
    $change$;
    v_value text;
BEGIN
    CREATE TEMP TABLE design_account (id int PRIMARY KEY, email text NOT NULL);
    INSERT INTO design_account VALUES (1, 'Alice@Example.com');

    CREATE TEMP TABLE IF NOT EXISTS design_account (
        id int PRIMARY KEY,
        email text NOT NULL,
        email_normalized text NOT NULL
    );
    IF EXISTS (
        SELECT FROM pg_attribute
        WHERE attrelid = 'design_account'::regclass
          AND attname = 'email_normalized' AND NOT attisdropped
    ) THEN
        RAISE EXCEPTION 'CREATE TABLE IF NOT EXISTS changed an existing table';
    END IF;

    EXECUTE v_change;
    EXECUTE v_change;

    SELECT email_normalized INTO v_value FROM design_account WHERE id = 1;
    IF v_value IS DISTINCT FROM 'alice@example.com' THEN
        RAISE EXCEPTION 'expected alice@example.com after two runs, got %', v_value;
    END IF;
END $$;
```

Every statement in `v_change` is a no-op on the second run, which is what
makes it safe in a re-run file and harmless if a run-once file is ever
replayed. For the project's own change, assert the new shape in a test next
to the code, so a deploy onto an old database fails instead of silently
keeping the old shape.

## Reference and seed data

pgmi loads every project file, not only SQL, so reference data can live in a
JSON, CSV or XML file and be read from the session views (`pgmi ai contract`).
If this project already has a mechanism for reference data, use it (check
`deploy.sql` and the README).

Otherwise treat the file as the source of truth and make the table converge
to it: upsert what the file has, remove what it no longer has. Inserting
with `ON CONFLICT DO NOTHING` alone never applies a changed value and never
removes a row. This test applies three versions of a file and checks the
result:

```sql runnable target=__test__/test_pgmi_design_2.sql
DO $$
DECLARE
    v_file jsonb;
    v_got text;
BEGIN
    CREATE TEMP TABLE design_currency (code text PRIMARY KEY, name text NOT NULL);

    FOREACH v_file IN ARRAY ARRAY[
        '[{"code": "EUR", "name": "Euro"}, {"code": "USD", "name": "Dollar"}]',
        '[{"code": "EUR", "name": "Euro"}, {"code": "JPY", "name": "Yen"}]',
        '[{"code": "EUR", "name": "Euro"}, {"code": "JPY", "name": "Yen"}]'
    ]::jsonb[]
    LOOP
        INSERT INTO design_currency (code, name)
        SELECT f.code, f.name
        FROM jsonb_to_recordset(v_file) AS f(code text, name text)
        ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name;

        DELETE FROM design_currency c
        WHERE NOT EXISTS (
            SELECT FROM jsonb_to_recordset(v_file) AS f(code text, name text)
            WHERE f.code = c.code
        );
    END LOOP;

    SELECT string_agg(code || '=' || name, ',' ORDER BY code) INTO v_got
    FROM design_currency;
    IF v_got IS DISTINCT FROM 'EUR=Euro,JPY=Yen' THEN
        RAISE EXCEPTION 'reference data did not converge: %', v_got;
    END IF;
END $$;
```

In `deploy.sql` the literal becomes the file's content cast to `jsonb`. If
other tables reference a removed row, the `DELETE` fails the deploy; that is
the right outcome, because someone has to decide what happens to the
referencing rows.

Development-only seed data belongs behind an environment parameter, never in
a path that runs in production.

## Tracking applied files

If this project records which files ran, the ledger and its rules are in
`deploy.sql`; follow them. When adding tracking, key the record by the
`<pgmi-meta>` id, falling back to `generic_id`, and store the checksum next
to it, so a renamed file is still recognised (pgmi-metadata).
Track SQL files against the normalized checksum, so a comment or formatting
edit does not count as a change. Track data files against the raw checksum:
the normalization is SQL-aware and can hide a real change in JSON or CSV.
`pgmi ai contract` names both columns.
