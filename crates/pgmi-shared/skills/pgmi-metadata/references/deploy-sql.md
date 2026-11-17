# Making deploy.sql follow the plan

pgmi orders `pgmi_plan_view`; only `deploy.sql` executes it. If this
project's `deploy.sql` loops over `pgmi_source_view` and you want sort keys to
set the order, replace the loop's source with the plan. Keep the
`is_sql_file` filter: the plan holds every loaded file, and without the filter
a `README.md` or a `001.sql.bak` is executed as SQL.

```sql
DO $$
DECLARE
    v_file record;
BEGIN
    FOR v_file IN
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view AS p
        JOIN pg_temp.pgmi_source_view AS s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './migrations/%'
        ORDER BY p.execution_order
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

Keep whatever directory filter the old loop had. A file with several keys
appears once per key, so this loop runs it once per key.

Switching changes the order of every file that has a key. Run
`pgmi metadata plan .` and confirm the new order before you deploy.

## Honouring `idempotent`

pgmi records the flag and does nothing else with it. Skipping a file that
already ran needs a record of what ran, which is a table this project's
`deploy.sql` creates and writes. If it has one, extend it; do not add a
second.

When you key that record, use the block's id with the path-derived fallback
for files without one, so a renamed file with a block is still recognised.
The plan exposes both; `pgmi ai contract` names the columns.

A skipped file is skipped even after you edit it. Decide in `deploy.sql`
what an edit to a file that already ran should do: fail the deploy, warn, or
nothing. The plan's checksum ignores comments (the pgmi-meta block
included), whitespace outside literals, and the case of keywords and unquoted identifiers, so
it changes only when the SQL does.
