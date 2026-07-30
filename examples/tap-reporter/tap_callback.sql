CREATE SEQUENCE pg_temp._pgmi_tap_seq;

CREATE OR REPLACE FUNCTION pg_temp.pgmi_tap_callback(e pg_temp.pgmi_test_event)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_test_count int;
BEGIN
    CASE e.event
        WHEN 'suite_start' THEN
            RAISE NOTICE 'TAP version 14';
            SELECT count(*) INTO v_test_count
            FROM pg_temp.pgmi_test_plan()
            WHERE step_type = 'test';
            RAISE NOTICE '1..%', v_test_count;
        WHEN 'test_end' THEN
            RAISE NOTICE 'ok % - %', nextval('pg_temp._pgmi_tap_seq'), e.path;
        WHEN 'suite_end' THEN
            NULL;
        ELSE
            NULL;
    END CASE;
END;
$$;
