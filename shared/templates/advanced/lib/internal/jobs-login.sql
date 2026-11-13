/*
<pgmi-meta
    id="e791155b-d225-4a5d-ba7a-f826ec4c4d9d"
    idempotent="true">
  <description>
    The optional background-jobs login: granted CONNECT, USAGE on api and
    EXECUTE on every api.job_* routine when it exists and is a registered
    service principal, nothing otherwise. deploy/finalize.sql calls
    internal.grant_jobs at the end of every deploy.
  </description>
  <sortKeys>
    <key>002/003</key>
  </sortKeys>
</pgmi-meta>
*/

-- Whether a service principal names the role. Read as the kernel: the
-- table's row-level security shows the owner no rows outside a request.
CREATE OR REPLACE FUNCTION internal.service_registered(p_role text)
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
-- by the deploy. When it exists and is a registered service principal it may connect
-- and execute every api.job_* routine, including jobs added since the last
-- deploy, and nothing else. A role of that name nobody registered may have
-- been created by anyone, so it gets nothing and loses what it held. Run as
-- the owner by deploy/finalize.sql at the end of every deploy.
CREATE OR REPLACE FUNCTION internal.grant_jobs()
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
    IF NOT internal.service_registered(v_jobs) THEN
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
