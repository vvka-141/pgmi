-- ============================================================================
-- Test: a secret parameter is gone from the session once bootstrap used it
-- ============================================================================
-- deploy/bootstrap.sql blanks every secret's pgmi.<key> setting and takes
-- PUBLIC's SELECT off pgmi's parameter view and table, so a project file or
-- test running as the owner cannot print a secret by accident (current_setting, a
-- dump of the parameter view). Guards against accidents only: a file can
-- RESET ROLE back to the deploy login, which still reads the view.
-- ============================================================================

DO $test$
DECLARE
    v_key  text;
    v_seen text;
BEGIN
    FOR v_key IN SELECT key FROM pg_temp.parameter_declaration() WHERE is_secret LOOP
        v_seen := current_setting('pgmi.' || v_key, true);
        IF COALESCE(v_seen, '') IS DISTINCT FROM '' THEN
            RAISE EXCEPTION 'TEST FAILED: secret parameter % is still readable as pgmi.%', v_key, v_key;
        END IF;
    END LOOP;

    FOR v_key IN SELECT unnest(ARRAY['pg_temp.pgmi_parameter_view', 'pg_temp._pgmi_parameter']) LOOP
        BEGIN
            EXECUTE format('SELECT count(*) FROM %s', v_key);
            RAISE EXCEPTION 'TEST FAILED: the owner role can read %', v_key;
        EXCEPTION WHEN insufficient_privilege THEN
            NULL;
        END;
    END LOOP;
END $test$;
