-- ============================================================================
-- Test: each role executes exactly its list
-- ============================================================================
-- The gateway (the api role, granted to gateway logins) executes its entry
-- points and nothing else: each one decides access before a handler runs, so
-- one more routine is a door around that decision. Its table privileges are
-- reads of the route registry; the exchange log is written by the entry
-- points as the owner, never by the gateway. The admin login holds no
-- grant of its own (it is a member of the owner). PUBLIC executes nothing.
-- The kernel role cannot log in, calls no SECURITY DEFINER routine of the
-- owner, reaches outside membership and common only the request-context
-- readers and internal.mcp_write_tool_exists, and its own routines are granted to no role but the owner. The
-- optional jobs login, when it exists and is a registered service principal,
-- executes every api.job_* routine and nothing else; unregistered, it executes
-- nothing. The gateway executes no job. With provider_role_pattern set, the
-- routines a managed provider keeps in schema public (owned by a role other
-- than the owner) are not the application's and are left out.
-- ============================================================================

-- One query, one UNION branch per rule; each row names the rule it breaks.
CREATE FUNCTION pg_temp.grant_drift()
RETURNS TABLE (role_name text, routine text, problem text)
LANGUAGE sql
SET search_path = pg_catalog, pg_temp
AS $$
    WITH role AS (
        SELECT pg_temp.deployment_setting('database_owner_role')::regrole AS owner,
               pg_temp.deployment_setting('database_admin_role')::regrole AS admin,
               pg_temp.deployment_setting('database_api_role')::regrole AS api,
               pg_temp.deployment_setting('database_kernel_role')::regrole AS kernel,
               to_regrole(quote_ident(pg_temp.deployment_setting('database_jobs_role'))) AS jobs,
               internal.service_registered(pg_temp.deployment_setting('database_jobs_role')) AS jobs_registered
    ),
    routine AS (
        SELECT p.oid, p.oid::regprocedure::text AS name, p.proowner, p.prosecdef, p.proacl
        FROM pg_proc p
        JOIN pg_temp.application_schema() s ON s.oid = p.pronamespace
        WHERE NOT EXISTS (SELECT FROM pg_depend d
                          WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
          AND NOT (p.pronamespace = 'public'::regnamespace
                   AND p.prorettype = 'pg_catalog.event_trigger'::regtype
                   AND p.proowner IS DISTINCT FROM pg_temp.deployment_setting('database_owner_role')::regrole
                   AND pg_temp.deployment_setting('provider_role_pattern', false) IS DISTINCT FROM '')
    ),
    gateway(name) AS (
        VALUES ('api.rest_invoke(text,text,extensions.hstore,bytea)'),
               ('api.rest_invoke(text,text,extensions.hstore,jsonb)'),
               ('api.rest_invoke(text,text,extensions.hstore,xml)'),
               ('api.rest_route_policy(text,text,text)'),
               ('api.rpc_resolve(text)'),
               ('api.rpc_invoke(uuid,extensions.hstore,bytea)'),
               ('api.rpc_route_policy(text,text)'),
               ('api.mcp_handle_request(jsonb,jsonb)'),
               ('api.mcp_request_policy(text,text,text,text,text)'),
               ('api.catalog_version()'),
               ('api.open_login_session(text,text,text,text,boolean,text[])'),
               ('api.close_login_session(text)'),
               ('api.open_dev_login_session(text,text,text,text)')
    ),
    gateway_reads(name, privilege) AS (
        VALUES ('api.handler', 'SELECT'),
               ('api.handler_permission', 'SELECT'),
               ('api.rest_route', 'SELECT'),
               ('api.rpc_route', 'SELECT'),
               ('api.mcp_route', 'SELECT'),
               ('api.schema_component', 'SELECT'),
               ('api.catalog_state', 'SELECT')
    ),
    kernel_reads(name) AS (
        VALUES ('api.current_principal_id()'),
               ('api.current_actual_principal_id()'),
               ('api.current_actor_kind()'),
               ('api.is_authenticated()'),
               ('api.current_organization_id()'),
               ('api.current_permissions()'),
               ('api.has_permission(text)'),
               ('api.is_platform_operator()'),
               ('api.current_login_session_id()'),
               ('api.current_organization_session_id()'),
               ('internal.transaction_is_read_only()'),
               ('internal.mcp_write_tool_exists(text)')
    )
    SELECT 'api', r.name, 'executable, not on the gateway''s list'
    FROM routine r, role
    WHERE has_function_privilege(role.api, r.oid, 'EXECUTE')
      AND r.name NOT IN (SELECT name FROM gateway)
    UNION ALL
    SELECT 'api', g.name, 'on the gateway''s list, not executable'
    FROM gateway g, role
    WHERE to_regprocedure(g.name) IS NOT NULL
      AND NOT has_function_privilege(role.api, to_regprocedure(g.name), 'EXECUTE')
    UNION ALL
    SELECT 'admin', r.name, 'granted directly'
    FROM routine r, role
    WHERE EXISTS (SELECT FROM aclexplode(r.proacl) a WHERE a.grantee = role.admin)
    UNION ALL
    SELECT 'PUBLIC', r.name, 'executable'
    FROM routine r
    WHERE has_function_privilege('public', r.oid, 'EXECUTE')
    UNION ALL
    SELECT 'kernel', r.name, 'executes a SECURITY DEFINER routine of the owner'
    FROM routine r, role
    WHERE r.prosecdef AND r.proowner = role.owner AND has_function_privilege(role.kernel, r.oid, 'EXECUTE')
    UNION ALL
    SELECT 'kernel', r.name, 'executable outside membership and common, not on the kernel''s list'
    FROM routine r
    JOIN pg_proc p ON p.oid = r.oid, role
    WHERE r.proowner IS DISTINCT FROM role.kernel
      AND p.pronamespace NOT IN ('membership'::regnamespace, 'common'::regnamespace)
      AND has_function_privilege(role.kernel, r.oid, 'EXECUTE')
      AND r.name NOT IN (SELECT name FROM kernel_reads)
    UNION ALL
    SELECT 'kernel', r.name, format('kernel routine granted to %s', CASE a.grantee WHEN 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END)
    FROM routine r, role
    CROSS JOIN LATERAL aclexplode(r.proacl) a
    WHERE r.proowner = role.kernel AND a.grantee NOT IN (role.kernel, role.owner)
      AND NOT pg_temp.provider_role(a.grantee)
    UNION ALL
    SELECT 'jobs', r.name, CASE WHEN has_function_privilege(role.jobs, r.oid, 'EXECUTE')
                                THEN 'executable, not an api.job_* routine of a registered jobs login'
                                ELSE 'an api.job_* routine, not executable' END
    FROM routine r
    JOIN pg_proc p ON p.oid = r.oid, role
    WHERE role.jobs IS NOT NULL
      AND has_function_privilege(role.jobs, r.oid, 'EXECUTE')
          IS DISTINCT FROM (role.jobs_registered AND p.proname LIKE 'job\_%' AND p.pronamespace = 'api'::regnamespace)
    UNION ALL
    SELECT 'api', r.name, 'executes a job'
    FROM routine r
    JOIN pg_proc p ON p.oid = r.oid, role
    WHERE p.pronamespace = 'api'::regnamespace AND p.proname LIKE 'job\_%'
      AND has_function_privilege(role.api, r.oid, 'EXECUTE')
    UNION ALL
    SELECT 'api', format('%s %s', c.oid::regclass, a.privilege_type), 'table or sequence privilege, not on the gateway''s list'
    FROM pg_class c
    JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
    CROSS JOIN LATERAL aclexplode(c.relacl) a, role
    WHERE a.grantee = role.api
      AND (c.oid::regclass::text, a.privilege_type) NOT IN (SELECT name, privilege FROM gateway_reads)
    UNION ALL
    SELECT 'api', format('%s %s', g.name, g.privilege), 'on the gateway''s table list, not granted'
    FROM gateway_reads g, role
    WHERE NOT has_table_privilege(role.api, g.name, g.privilege)
    UNION ALL
    SELECT rolname, NULL, 'can log in'
    FROM pg_roles, role
    WHERE oid IN (role.owner, role.api, role.kernel) AND rolcanlogin;
$$;

DO $$
DECLARE
    v_drift  text;
    v_api    text := pg_temp.deployment_setting('database_api_role');
    v_kernel text := pg_temp.deployment_setting('database_kernel_role');
BEGIN
    RAISE NOTICE '-> Exact grants: each role executes its list';

    SELECT string_agg(format('%s %s: %s', role_name, coalesce(routine, ''), problem), E'\n  ' ORDER BY role_name, routine)
    INTO v_drift FROM pg_temp.grant_drift();
    IF v_drift IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: grants differ from the lists:\n  %', v_drift;
    END IF;

    BEGIN
        EXECUTE format('GRANT EXECUTE ON FUNCTION api.json_response(integer, jsonb) TO %I', v_api);
        EXECUTE format('GRANT EXECUTE ON FUNCTION api.rest_invoke(text, text, extensions.hstore, bytea) TO %I', v_kernel);
        EXECUTE format('GRANT EXECUTE ON FUNCTION internal.rest_route_lookup(text, text) TO %I', v_kernel);
        EXECUTE format('GRANT UPDATE ON api.rest_exchange TO %I', v_api);
        SELECT string_agg(format('%s %s', role_name, routine), ', ' ORDER BY role_name) INTO v_drift FROM pg_temp.grant_drift();
        RAISE EXCEPTION USING ERRCODE = 'P0004', MESSAGE = coalesce(v_drift, '');
    EXCEPTION WHEN assert_failure THEN
        IF SQLERRM NOT LIKE '%api api.json_response(integer,jsonb)%'
           OR SQLERRM NOT LIKE '%kernel api.rest_invoke(text,text,extensions.hstore,bytea)%'
           OR SQLERRM NOT LIKE '%kernel internal.rest_route_lookup(text,text)%'
           OR SQLERRM NOT LIKE '%api api.rest_exchange UPDATE%' THEN
            RAISE EXCEPTION 'TEST FAILED: the guard must name an extra gateway grant, a kernel reaching the owner, a kernel off its list and a gateway write to the exchange log, named: %', SQLERRM;
        END IF;
    END;
END $$;

-- The jobs login as README's Background jobs creates it (here inside the test,
-- rolled back): a service principal, and after the deploy's grants it runs
-- api.job_* routines and is refused anything else.
DO $$
DECLARE
    v_owner   text := current_user;
    v_jobs    text := pg_temp.deployment_setting('database_jobs_role');
    v_service membership.service_principal;
    v_drift   text;
    v_denied  text[] := '{}';
    v_fn      text;
BEGIN
    RAISE DEBUG '-> Exact grants: the jobs login executes api.job_* and nothing else';

    IF to_regrole(quote_ident(v_jobs)) IS NULL THEN
        RESET ROLE;
        EXECUTE format('CREATE ROLE %I LOGIN CONNECTION LIMIT 2', v_jobs);
        EXECUTE format('GRANT %I TO CURRENT_USER WITH INHERIT FALSE, SET TRUE', v_jobs);
        EXECUTE format('SET ROLE %I', v_owner);
        PERFORM pg_temp.test_act_as(NULL);
        v_service := membership.fn_register_service_principal('service.jobs', 'Background jobs', v_jobs);
        IF v_service.database_role IS DISTINCT FROM v_jobs::name
           OR membership.fn_register_service_principal('service.jobs', 'Background jobs', v_jobs) IS DISTINCT FROM v_service THEN
            RAISE EXCEPTION 'TEST FAILED: the jobs login must register once as service principal service.jobs, got %', row_to_json(v_service);
        END IF;
        PERFORM internal.grant_jobs();
    END IF;

    SELECT string_agg(format('%s %s: %s', role_name, coalesce(routine, ''), problem), ', ' ORDER BY role_name, routine)
    INTO v_drift FROM pg_temp.grant_drift();
    IF v_drift IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: with the jobs login, grants differ from the lists: %', v_drift;
    END IF;

    IF NOT internal.service_registered(v_jobs) THEN
        RAISE DEBUG '   jobs login % is no registered service principal; live probe skipped', v_jobs;
        RETURN;
    END IF;

    IF NOT pg_has_role(session_user, v_jobs, 'SET') THEN
        IF NOT pg_has_role(session_user, v_jobs, 'MEMBER WITH ADMIN OPTION') THEN
            RAISE DEBUG '   jobs login % is not the deploy login''s to assume; live probe skipped', v_jobs;
            RETURN;
        END IF;
        RESET ROLE;
        EXECUTE format('GRANT %I TO CURRENT_USER WITH INHERIT FALSE, SET TRUE', v_jobs);
        EXECUTE format('SET ROLE %I', v_owner);
    END IF;

    EXECUTE format('SET ROLE %I', v_jobs);
    PERFORM api.job_purge_exchanges_batch(1);
    FOREACH v_fn IN ARRAY ARRAY[
        $q$SELECT api.catalog_version()$q$,
        $q$SELECT api.purge_exchanges_batch(now() - interval '1 day', 1)$q$,
        $q$SELECT count(*) FROM internal.job_run_log$q$,
        $q$SELECT count(*) FROM api.rest_exchange$q$
    ] LOOP
        BEGIN
            EXECUTE v_fn;
        EXCEPTION WHEN insufficient_privilege THEN
            v_denied := v_denied || v_fn;
        END;
    END LOOP;
    EXECUTE format('SET ROLE %I', v_owner);

    IF cardinality(v_denied) IS DISTINCT FROM 4 THEN
        RAISE EXCEPTION 'TEST FAILED: the jobs login reached beyond api.job_*; refused only: %', v_denied;
    END IF;
END $$;

-- Live: the gateway serves through its entry points and is refused anything else.
DO $$
DECLARE
    v_role   text := current_user;
    v_status int;
    v_denied text[] := '{}';
    v_fn     text;
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_api_role'));
    v_status := (api.rest_invoke('GET', '/openapi.json', ''::extensions.hstore, NULL::bytea)).status_code;
    PERFORM api.catalog_version();
    FOREACH v_fn IN ARRAY ARRAY[
        $q$SELECT api.json_response(200, '{}'::jsonb)$q$,
        $q$SELECT api.current_principal_id()$q$,
        $q$SELECT internal.resolve_session_context('x', 'y')$q$,
        $q$SELECT internal.resolve_connection_context('x')$q$,
        $q$SELECT membership.fn_approve_connection(NULL, 'agent.reader', 'x', 1)$q$,
        $q$SELECT membership.fn_open_login_session('x', 'y', 'z@example.test', NULL, true, '{}')$q$,
        $q$SELECT api.create_or_replace_rest_handler('{}'::jsonb, '')$q$,
        $q$SELECT membership.fn_simulate_access(NULL, NULL, 'GET', '/members')$q$,
        $q$SELECT membership.fn_simulation_context(NULL, NULL)$q$
    ] LOOP
        BEGIN
            EXECUTE v_fn;
        EXCEPTION WHEN insufficient_privilege THEN
            v_denied := v_denied || v_fn;
        END;
    END LOOP;
    EXECUTE format('SET ROLE %I', v_role);

    IF v_status IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: the gateway role GET /openapi.json answered %', v_status;
    END IF;
    IF cardinality(v_denied) IS DISTINCT FROM 9 THEN
        RAISE EXCEPTION 'TEST FAILED: the gateway role reached a routine off its list; refused only: %', v_denied;
    END IF;
END $$;

-- Every SECURITY DEFINER routine pins search_path. Without it the caller
-- chooses where an unqualified name resolves, and the body runs as the definer.
DO $$
DECLARE
    v_unpinned text;
BEGIN
    SELECT string_agg(p.oid::regprocedure::text, ', ' ORDER BY p.oid::regprocedure::text)
    INTO v_unpinned
    FROM pg_proc p
    JOIN pg_temp.application_schema() s ON s.oid = p.pronamespace
    WHERE p.prosecdef
      AND NOT EXISTS (SELECT FROM unnest(coalesce(p.proconfig, '{}')) cfg WHERE cfg LIKE 'search_path=%');
    IF v_unpinned IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: SECURITY DEFINER without SET search_path: %', v_unpinned;
    END IF;
END $$;

-- With provider_role_pattern set, an event-trigger function another role
-- keeps in schema public drops out (it cannot be called directly); a plain
-- routine there, and one of the owner's, PUBLIC-executable, still fail.
DO $$
DECLARE
    v_owner text := current_user;
    v_drift text;
BEGIN
    RESET ROLE;
    CREATE FUNCTION public.provider_probe() RETURNS event_trigger LANGUAGE plpgsql AS 'BEGIN END';
    CREATE FUNCTION public.provider_probe_plain() RETURNS int LANGUAGE sql AS 'SELECT 1';
    EXECUTE format('SET ROLE %I', v_owner);
    CREATE FUNCTION internal.provider_probe_owned() RETURNS int LANGUAGE sql AS 'SELECT 1';
    GRANT EXECUTE ON FUNCTION internal.provider_probe_owned() TO PUBLIC;
    PERFORM set_config('pgmi.provider_role_pattern', '', true);

    SELECT string_agg(format('%s %s', role_name, routine), ', ' ORDER BY role_name, routine) INTO v_drift
    FROM pg_temp.grant_drift() WHERE routine LIKE '%provider_probe%';
    IF coalesce(v_drift, '') NOT LIKE '%PUBLIC public.provider_probe()%'
       OR coalesce(v_drift, '') NOT LIKE '%PUBLIC public.provider_probe_plain()%'
       OR coalesce(v_drift, '') NOT LIKE '%PUBLIC internal.provider_probe_owned()%' THEN
        RAISE EXCEPTION 'TEST FAILED: without provider_role_pattern all three PUBLIC routines must fail, named: %', v_drift;
    END IF;

    PERFORM set_config('pgmi.provider_role_pattern', 'pgmi\_provider\_%', true);
    SELECT string_agg(format('%s %s', role_name, routine), ', ' ORDER BY role_name, routine) INTO v_drift
    FROM pg_temp.grant_drift() WHERE routine LIKE '%provider_probe%';
    IF coalesce(v_drift, '') LIKE '%public.provider_probe()%'
       OR coalesce(v_drift, '') NOT LIKE '%PUBLIC public.provider_probe_plain()%'
       OR coalesce(v_drift, '') NOT LIKE '%PUBLIC internal.provider_probe_owned()%' THEN
        RAISE EXCEPTION 'TEST FAILED: with provider_role_pattern only the event-trigger function may drop out, named: %', v_drift;
    END IF;
END $$;
