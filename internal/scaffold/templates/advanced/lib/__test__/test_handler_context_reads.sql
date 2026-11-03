-- ============================================================================
-- Test: no handler reads the request context by name
-- ============================================================================
-- A handler learns who calls and where only through the typed readers
-- (api.current_principal_id, api.current_organization_id,
-- api.current_permissions, api.has_permission, ...). Reading an auth.* setting
-- or a session header itself skips the trust gate and the snapshot, so every
-- registered handler body, every api.job_* routine and every core routine is
-- checked, and a planted one must be named. The readers themselves still
-- match, so the pattern cannot have gone blind.

CREATE FUNCTION pg_temp.reads_context_by_name(p_src text)
RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
    SELECT p_src ~* $re$(current_setting|set_config)\s*\(\s*'auth\.|'x-(login-session|organization-session|connection|user-id|user-email|tenant-id)'|->>?\s*'(loginSession|organizationSession|connection)'$re$
$$;

CREATE FUNCTION pg_temp.handlers_reading_context()
RETURNS text[]
LANGUAGE sql STABLE AS $$
    SELECT coalesce(array_agg(r.name ORDER BY r.name), '{}')
    FROM (SELECT h.handler_function_name::text AS name, p.prosrc
          FROM api.handler h
          JOIN pg_proc p ON p.oid = h.handler_func
          WHERE h.deleted_at IS NULL
          UNION ALL
          SELECT p.pronamespace::regnamespace || '.' || p.proname, p.prosrc
          FROM pg_proc p
          WHERE (p.pronamespace = 'api'::regnamespace AND p.proname LIKE 'job\_%')
             OR p.pronamespace = 'core'::regnamespace) r
    WHERE pg_temp.reads_context_by_name(r.prosrc);
$$;

DO $$
DECLARE
    v_found text[];
BEGIN
    RAISE DEBUG '-> Handlers read the context through the typed readers only';

    v_found := pg_temp.handlers_reading_context();
    IF v_found IS DISTINCT FROM '{}' THEN
        RAISE EXCEPTION 'TEST FAILED: handlers, jobs or core routines read auth.* or a session header by name: %', v_found
            USING HINT = 'Use api.current_principal_id(), api.current_organization_id(), api.current_permissions() or api.has_permission().';
    END IF;

    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'ffffffff-ac01-4000-8000-000000000001', 'path', '/test-context-read', 'httpMethod', '^GET$',
        'name', 'context_read_probe', 'access', 'authenticated', 'outputSchema', jsonb_build_object('type', 'object')),
        $b$BEGIN
            RETURN api.json_response(200, jsonb_build_object('org', current_setting('auth.organization_id', true)));
        END;$b$);
    PERFORM api.create_or_replace_rpc_handler(jsonb_build_object(
        'id', 'ffffffff-ac01-4000-8000-000000000002', 'methodName', 'test.context_header_probe', 'access', 'public'),
        $b$BEGIN
            RETURN api.jsonrpc_success(jsonb_build_object('s', (request).headers->'x-login-session'), '1'::jsonb);
        END;$b$);

    CREATE FUNCTION api.job_context_probe_batch(p_batch_size int) RETURNS int LANGUAGE sql
        AS $b$ SELECT length(current_setting('auth.principal_id', true)) $b$;
    CREATE FUNCTION core.context_probe() RETURNS text LANGUAGE sql
        AS $b$ SELECT current_setting('auth.organization_id', true) $b$;

    v_found := pg_temp.handlers_reading_context();
    IF (v_found @> ARRAY['context_read_probe', 'rpc_test_context_header_probe',
                             'api.job_context_probe_batch', 'core.context_probe']) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: the guard missed a handler that reads the context by name, found %', v_found;
    END IF;

    IF (pg_temp.reads_context_by_name((SELECT prosrc FROM pg_proc WHERE oid = 'api.current_organization_id()'::regprocedure))) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: the pattern no longer matches api.current_organization_id(), which reads the context by name; the guard is blind';
    END IF;

    RAISE DEBUG '  + no registered handler reads auth.* or a session header; a planted one is named';
END $$;
