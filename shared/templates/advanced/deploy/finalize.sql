-- ============================================================================
-- deploy/finalize.sql
-- After the last project file, before the tests: every catalog file synced,
-- entity standards applied, privileges converged (the grants themselves are
-- lib/'s: internal.grant_gateway_entry_points, retire_dev_login, grant_jobs),
-- nothing taken away silently, the provider's table privileges revoked. Also
-- the test reporter.
-- ============================================================================

DO $catalogs$
DECLARE
    v_unsynced text;
BEGIN
    SELECT string_agg(s.path, ', ' ORDER BY s.path) INTO v_unsynced
    FROM pg_temp.pgmi_source_view s
    WHERE s.path LIKE './data/catalogs/%'
      AND NOT EXISTS (SELECT FROM pg_temp.catalog_synced c WHERE c.path = s.path);

    IF v_unsynced IS NOT NULL THEN
        RAISE EXCEPTION 'Catalog file(s) that no file synced: %', v_unsynced
            USING HINT = 'A file data/catalogs/<schema>.<table>.json is synced by SELECT pg_temp.sync_catalog(''<schema>.<table>''); in a re-run file sorted after the table''s run-once file. Remove a file that names no table.';
    END IF;
END $catalogs$;

SELECT pg_temp.apply_entity_standards_all();

DO $privs$
DECLARE
    v_routine text;
BEGIN
    -- The default privileges keep new routines closed; this closes the rest
    -- in every application schema: a routine a file granted to PUBLIC, or
    -- one created by a role other than the owner. A routine callable by
    -- PUBLIC is callable by any role holding USAGE on its schema, and the
    -- api schema holds SECURITY DEFINER gateways that open sessions and act
    -- for whoever a session names.
    FOR v_routine IN
        SELECT format('%I.%I(%s)', s.name, p.proname, pg_get_function_identity_arguments(p.oid))
        FROM pg_proc p
        JOIN pg_temp.application_schema() s ON s.oid = p.pronamespace
        WHERE has_function_privilege('public', p.oid, 'EXECUTE')
          AND NOT EXISTS (
              SELECT FROM pg_depend d
              WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
    LOOP
        EXECUTE format('REVOKE EXECUTE ON ROUTINE %s FROM PUBLIC', v_routine);
    END LOOP;

    PERFORM internal.grant_gateway_entry_points();
    IF current_setting('pgmi.env') <> 'dev' THEN
        PERFORM internal.retire_dev_login();
    END IF;
    PERFORM internal.grant_jobs();

    RAISE NOTICE '[pgmi] Privileges converged (no routine callable by PUBLIC, the gateway on its entry points only)';
END $privs$;

DO $access$
DECLARE
    v_lost text;
BEGIN
    SELECT string_agg(object || ': ' || entry, E'\n  ' ORDER BY object COLLATE "C", entry COLLATE "C")
    INTO v_lost
    FROM pg_temp.access_lost();

    IF v_lost IS NOT NULL THEN
        RAISE EXCEPTION E'This deploy took away grants, policies or triggers the database had before it:\n  %', v_lost
            USING HINT = 'Restore them (a DROP ... CASCADE takes the grants, policies and triggers of what depends on it). To remove one on purpose, the file that removes it declares it: INSERT INTO pg_temp.access_removal (object, entry) VALUES (''<object>'', ''<entry>'');';
    END IF;
END $access$;

-- The provider's whole-table privileges on new tables (deploy/providers.sql).
SELECT pg_temp.revoke_provider_table_privileges();


-- The callback deploy.sql passes to pgmi_test: names each fixture and test,
-- and quiets lib/ tests the way the loop quiets lib/ files.
CREATE OR REPLACE FUNCTION pg_temp.test_observer(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    CASE e.event
        WHEN 'suite_start' THEN RAISE NOTICE '[pgmi] Test suite started';
        WHEN 'suite_end' THEN RAISE NOTICE '[pgmi] Test suite passed';
        WHEN 'fixture_start' THEN
            RAISE NOTICE '[pgmi] Fixture: %', e.path;
            PERFORM pg_temp.quiet_framework(e.path);
        WHEN 'fixture_end' THEN PERFORM pg_temp.unquiet_framework(e.path);
        WHEN 'test_start' THEN
            RAISE NOTICE '[pgmi] Test: %', e.path;
            PERFORM pg_temp.quiet_framework(e.path);
        WHEN 'test_end' THEN PERFORM pg_temp.unquiet_framework(e.path);
        WHEN 'teardown_start' THEN RAISE DEBUG '[pgmi] Teardown: %', e.directory;
        ELSE NULL;
    END CASE;
END $$;
