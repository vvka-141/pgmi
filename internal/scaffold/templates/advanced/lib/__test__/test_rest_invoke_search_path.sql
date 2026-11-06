-- ============================================================================
-- Test: the gateway entry points do not depend on the caller's search_path,
-- and no template role carries a search_path of its own
-- ============================================================================

DO $$
BEGIN
    SET LOCAL search_path = public;
    IF (api.rest_invoke('GET', '/hello', ''::extensions.hstore, '{}'::jsonb)).status_code IS DISTINCT FROM 200
       OR (api.rest_invoke('GET', '/hello', ''::extensions.hstore, '<x/>'::xml)).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: the jsonb and xml rest_invoke overloads must answer under search_path = public';
    END IF;
    RESET search_path;

    IF EXISTS (SELECT FROM pg_roles r, unnest(r.rolconfig) c
               WHERE r.rolname IN (pg_temp.deployment_setting('database_owner_role'),
                                   pg_temp.deployment_setting('database_admin_role'),
                                   pg_temp.deployment_setting('database_api_role'))
                 AND c LIKE 'search_path=%') THEN
        RAISE EXCEPTION 'TEST FAILED: a template role carries its own search_path, which overrides the database default';
    END IF;

    RAISE NOTICE '  + rest_invoke overloads ignore the caller''s search_path; template roles take the database default';
END $$;
