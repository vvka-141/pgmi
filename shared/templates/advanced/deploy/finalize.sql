-- ============================================================================
-- deploy/finalize.sql
-- After the last project file, before the tests: every catalog file synced,
-- entity standards applied, privileges converged, nothing taken away
-- silently, the provider's table privileges revoked. Also the test reporter.
-- ============================================================================

-- Whether a service principal names the role. Read as the kernel: the
-- table's row-level security shows the owner no rows outside a request.
CREATE FUNCTION pg_temp.service_registered(p_role text)
RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_found  boolean;
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_kernel_role'));
    v_found := EXISTS (SELECT FROM membership.service_principal WHERE database_role = p_role);
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_found;
END $$;

-- The optional background-jobs login (README: Background jobs): never created
-- here. When it exists and is a registered service principal it may connect
-- and execute every api.job_* routine, including jobs added since the last
-- deploy, and nothing else. A role of that name nobody registered may have
-- been created by anyone, so it gets nothing and loses what it held. Run as
-- the owner at the end of every deploy.
CREATE FUNCTION pg_temp.grant_jobs()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_jobs    text := pg_temp.deployment_setting('database_jobs_role');
    v_grantee text;
    v_routine text;
BEGIN
    IF to_regrole(quote_ident(v_jobs)) IS NULL THEN
        RETURN;
    END IF;
    IF v_jobs IN (pg_temp.deployment_setting('database_owner_role'), pg_temp.deployment_setting('database_admin_role'),
                  pg_temp.deployment_setting('database_api_role'), pg_temp.deployment_setting('database_kernel_role')) THEN
        RAISE EXCEPTION 'database_jobs_role names another template role: %', v_jobs;
    END IF;
    IF NOT pg_temp.service_registered(v_jobs) THEN
        v_grantee := to_regrole(quote_ident(v_jobs))::text;
        INSERT INTO pg_temp.access_removal
        SELECT e.object, e.entry FROM pg_temp.access_entry() e
        WHERE (e.object = 'api' AND e.entry = 'grant USAGE to ' || v_grantee)
           OR (e.object LIKE 'api.job\_%' AND e.entry = 'grant EXECUTE to ' || v_grantee)
        ON CONFLICT DO NOTHING;
        EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM %I', current_database(), v_jobs);
        EXECUTE format('REVOKE USAGE ON SCHEMA api FROM %I', v_jobs);
        FOR v_routine IN
            SELECT p.oid::regprocedure::text FROM pg_proc p
            WHERE p.pronamespace = 'api'::regnamespace AND p.proname LIKE 'job\_%'
        LOOP
            EXECUTE format('REVOKE EXECUTE ON ROUTINE %s FROM %I', v_routine, v_jobs);
        END LOOP;
        RAISE NOTICE '[pgmi] Jobs login % is no registered service principal: granted nothing. Register it as the admin login (README: The jobs login), then redeploy: SELECT membership.fn_register_service_principal(''service.jobs'', ''Background jobs'', %);', v_jobs, quote_literal(v_jobs);
        RETURN;
    END IF;
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), v_jobs);
    EXECUTE format('GRANT USAGE ON SCHEMA api TO %I', v_jobs);
    FOR v_routine IN
        SELECT p.oid::regprocedure::text FROM pg_proc p
        WHERE p.pronamespace = 'api'::regnamespace AND p.proname LIKE 'job\_%'
    LOOP
        EXECUTE format('GRANT EXECUTE ON ROUTINE %s TO %I', v_routine, v_jobs);
    END LOOP;
END $$;

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
    v_api_role text := pg_temp.deployment_setting('database_api_role');
    v_caller   text := current_user;
    v_routine  text;
    v_issuer   text;
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

    -- The gateway executes its entry points and nothing else: each one
    -- decides access before any handler runs. The admin login needs no
    -- grant: it is a member of the owner. lib/__test__/test_exact_grants.sql
    -- holds every login to its list.
    EXECUTE format('GRANT EXECUTE ON ROUTINE '
                   'api.rest_invoke(text, text, extensions.hstore, bytea), '
                   'api.rest_invoke(text, text, extensions.hstore, jsonb), '
                   'api.rest_invoke(text, text, extensions.hstore, xml), '
                   'api.rest_route_policy(text, text, text), '
                   'api.rpc_resolve(text), '
                   'api.rpc_invoke(uuid, extensions.hstore, bytea), '
                   'api.rpc_route_policy(text, text), '
                   'api.mcp_handle_request(jsonb, jsonb), '
                   'api.mcp_request_policy(text, text, text, text, text), '
                   'api.catalog_version(), '
                   'api.open_login_session(text, text, text, text, boolean, text[]), '
                   'api.close_login_session(text) TO %I', v_api_role);
    IF current_setting('deployment.env') = 'dev' THEN
        IF to_regprocedure('api.open_dev_login_session(text, text, text, text)') IS NOT NULL THEN
            EXECUTE format('GRANT EXECUTE ON ROUTINE api.open_dev_login_session(text, text, text, text) TO %I', v_api_role);
        END IF;
    ELSE
        -- A database relabelled from dev keeps what dev files created;
        -- a development issuer someone signed in with stays (foreign key) but is inert:
        -- api.open_login_session accepts only oidc issuers.
        DROP FUNCTION IF EXISTS api.open_dev_login_session(text, text, text, text);
        EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_kernel_role'));
        UPDATE membership.login_session s
        SET revoked_at = clock_timestamp(), revoke_reason = 'environment relabelled'
        FROM membership.external_identity e
        JOIN membership.identity_issuer i ON i.id = e.issuer_id
        WHERE s.external_identity_object_id = e.object_id
          AND i.kind = 'development' AND s.revoked_at IS NULL;
        EXECUTE format('SET ROLE %I', v_caller);
        FOR v_issuer IN SELECT id FROM membership.identity_issuer WHERE kind = 'development' LOOP
            BEGIN
                DELETE FROM membership.identity_issuer WHERE id = v_issuer;
            EXCEPTION WHEN foreign_key_violation THEN
                NULL;
            END;
        END LOOP;
    END IF;
    PERFORM pg_temp.grant_jobs();

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
