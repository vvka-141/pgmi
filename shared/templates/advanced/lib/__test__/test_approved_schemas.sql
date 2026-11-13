-- ============================================================================
-- Test: the application schemas and who may use them are exactly the lists
-- ============================================================================
-- pg_temp.approved_schema_grant() in deploy/settings.sql lists every application
-- schema with each privilege granted on it. A schema not in the list, a listed
-- one missing, and a grant on a schema that the list does not hold or that the
-- database lacks fail the deploy. The jobs login's row counts only once it is
-- a registered service principal. A managed provider's schemas
-- (provider_schemas) are left out, and so is USAGE granted to its own roles
-- (provider_role_pattern), or anything on public; CREATE on a schema the
-- template puts on a search_path still fails, as does CREATE on extensions
-- for any role but its owner.
-- ============================================================================

CREATE FUNCTION pg_temp.schema_drift()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    WITH approved AS (SELECT * FROM pg_temp.approved_schema_grant()),
    app AS (
        SELECT * FROM pg_temp.application_schema()
        WHERE name NOT IN (SELECT pg_temp.provider_schema() EXCEPT SELECT schema_name FROM approved)
    ),
    expected AS (
        SELECT a.schema_name, r.oid AS grantee, a.privilege
        FROM approved a
        JOIN pg_roles r ON r.rolname = pg_temp.deployment_setting(a.role_key, false)
        WHERE a.role_key IS DISTINCT FROM 'database_jobs_role' OR pg_temp.service_registered(r.rolname)
    ),
    actual AS (
        SELECT s.name AS schema_name, g.grantee, g.privilege_type AS privilege
        FROM app s
        JOIN pg_namespace n ON n.oid = s.oid
        CROSS JOIN LATERAL aclexplode(n.nspacl) g
        WHERE g.grantee IS DISTINCT FROM n.nspowner
          AND NOT (pg_temp.provider_role(g.grantee) AND (g.privilege_type = 'USAGE' OR s.name = 'public'))
    )
    SELECT s.name || ': not an approved schema'
    FROM app s
    WHERE s.name NOT IN (SELECT schema_name FROM approved)
    UNION ALL
    SELECT DISTINCT a.schema_name || ': a schema of the template, named in provider_schemas'
    FROM approved a
    WHERE a.schema_name IN (SELECT pg_temp.provider_schema())
    UNION ALL
    SELECT DISTINCT a.schema_name || ': approved, but missing'
    FROM approved a
    WHERE a.schema_name NOT IN (SELECT name FROM app)
    UNION ALL
    SELECT format('%s: %s granted to %s, not in the list', x.schema_name, x.privilege,
                  CASE x.grantee WHEN 0 THEN 'PUBLIC' ELSE x.grantee::regrole::text END)
    FROM (SELECT * FROM actual EXCEPT SELECT * FROM expected) x
    WHERE x.schema_name IN (SELECT schema_name FROM approved)
    UNION ALL
    SELECT format('%s: %s to %s is listed, not granted', x.schema_name, x.privilege, x.grantee::regrole)
    FROM (SELECT * FROM expected EXCEPT SELECT * FROM actual) x
    WHERE x.schema_name IN (SELECT name FROM app)
    UNION ALL
    SELECT format('extensions: CREATE granted to %s', CASE g.grantee WHEN 0 THEN 'PUBLIC' ELSE g.grantee::regrole::text END)
    FROM pg_namespace n
    CROSS JOIN LATERAL aclexplode(n.nspacl) g
    WHERE n.nspname = 'extensions' AND g.privilege_type = 'CREATE' AND g.grantee IS DISTINCT FROM n.nspowner
$$;

DO $$
DECLARE
    v_owner text := current_user;
    v_bad   text;
BEGIN
    SELECT string_agg(d, E'\n  ' ORDER BY d) INTO v_bad FROM pg_temp.schema_drift() d;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: application schemas differ from pg_temp.approved_schema_grant():\n  %', v_bad
            USING HINT = 'A new schema, or a new grant on one, is added to pg_temp.approved_schema_grant() in deploy/settings.sql.';
    END IF;

    CREATE SCHEMA schema_probe;
    EXECUTE format('GRANT USAGE ON SCHEMA internal TO %I', pg_temp.deployment_setting('database_api_role'));
    EXECUTE format('REVOKE USAGE ON SCHEMA internal FROM %I', pg_temp.deployment_setting('database_kernel_role'));
    RESET ROLE;
    GRANT USAGE ON SCHEMA public TO PUBLIC;
    EXECUTE format('SET ROLE %I', v_owner);
    SELECT string_agg(d, '; ' ORDER BY d) INTO v_bad FROM pg_temp.schema_drift() d;
    IF v_bad IS DISTINCT FROM format('internal: USAGE granted to %s, not in the list; internal: USAGE to %s is listed, not granted; '
                                     'public: USAGE granted to PUBLIC, not in the list; schema_probe: not an approved schema',
                                     pg_temp.deployment_setting('database_api_role')::regrole,
                                     pg_temp.deployment_setting('database_kernel_role')::regrole) THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name an unapproved schema, an extra grant, a missing grant and PUBLIC, named: %', v_bad;
    END IF;
END $$;

-- With the provider settings on, the provider's USAGE and schema drop out;
-- its CREATE on a template schema or on extensions, PUBLIC and a template
-- schema named as a provider's still fail.
DO $$
DECLARE
    v_owner text := current_user;
    v_bad   text;
BEGIN
    RESET ROLE;
    CREATE ROLE pgmi_provider_probe NOLOGIN;
    CREATE SCHEMA provider_probe;
    EXECUTE format('SET ROLE %I', v_owner);
    PERFORM set_config('deployment.provider_role_pattern', 'pgmi\_provider\_%', true);
    PERFORM set_config('deployment.provider_schemas', '["provider_probe", "membership"]', true);
    GRANT USAGE ON SCHEMA internal TO pgmi_provider_probe;
    GRANT USAGE ON SCHEMA internal TO PUBLIC;
    GRANT CREATE ON SCHEMA core TO pgmi_provider_probe;
    RESET ROLE;
    GRANT CREATE ON SCHEMA extensions TO pgmi_provider_probe;
    EXECUTE format('SET ROLE %I', v_owner);

    SELECT string_agg(d, '; ' ORDER BY d) INTO v_bad FROM pg_temp.schema_drift() d;
    IF coalesce(v_bad, '') LIKE '%provider\_probe:%'
       OR coalesce(v_bad, '') LIKE '%USAGE granted to pgmi\_provider\_probe%'
       OR coalesce(v_bad, '') NOT LIKE '%core: CREATE granted to pgmi\_provider\_probe, not in the list%'
       OR coalesce(v_bad, '') NOT LIKE '%extensions: CREATE granted to pgmi\_provider\_probe%'
       OR coalesce(v_bad, '') NOT LIKE '%internal: USAGE granted to PUBLIC, not in the list%'
       OR coalesce(v_bad, '') NOT LIKE '%membership: a schema of the template, named in provider_schemas%' THEN
        RAISE EXCEPTION 'TEST FAILED: the provider settings must leave out only the provider''s role and schema, named: %', v_bad;
    END IF;
END $$;
