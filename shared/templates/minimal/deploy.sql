-- Your SQL files are rows in pg_temp.pgmi_source_view. This loop runs them in
-- path order, inside one transaction. Change the query to decide what runs and
-- in which order: filter by directory, skip files, run some only once.
BEGIN;

DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE is_sql_file
        ORDER BY path
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;
