BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_source_view
        WHERE directory = './schema/' AND is_sql_file
        ORDER BY path
    )
    LOOP
        RAISE DEBUG 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

DO $$
DECLARE
    v_callback RECORD;
BEGIN
    SELECT content INTO STRICT v_callback
    FROM pg_temp.pgmi_source_view
    WHERE path = './tap_callback.sql';
    EXECUTE v_callback.content;
END $$;

CALL pgmi_test(NULL, 'pg_temp.pgmi_tap_callback');

COMMIT;
