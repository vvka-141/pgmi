-- ============================================================================
-- Test: the application schemas and who may use them are exactly the lists
-- ============================================================================
-- pg_temp.approved_schema_grant() in deploy.sql lists every application
-- schema with each privilege granted on it. A schema not in the list, a listed
-- one missing, and a grant on a schema that the list does not hold or that the
-- database lacks fail the deploy.
-- ============================================================================

CREATE FUNCTION pg_temp.schema_drift()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    WITH approved AS (SELECT * FROM pg_temp.approved_schema_grant()),
    expected AS (
        SELECT a.schema_name, r.oid AS grantee, a.privilege
        FROM approved a
        JOIN pg_roles r ON r.rolname = pg_temp.deployment_setting(a.role_key, false)
    ),
    actual AS (
        SELECT s.name AS schema_name, g.grantee, g.privilege_type AS privilege
        FROM pg_temp.application_schema() s
        JOIN pg_namespace n ON n.oid = s.oid
        CROSS JOIN LATERAL aclexplode(n.nspacl) g
        WHERE g.grantee IS DISTINCT FROM n.nspowner
    )
    SELECT s.name || ': not an approved schema'
    FROM pg_temp.application_schema() s
    WHERE s.name NOT IN (SELECT schema_name FROM approved)
    UNION ALL
    SELECT DISTINCT a.schema_name || ': approved, but missing'
    FROM approved a
    WHERE a.schema_name NOT IN (SELECT name FROM pg_temp.application_schema())
    UNION ALL
    SELECT format('%s: %s granted to %s, not in the list', x.schema_name, x.privilege,
                  CASE x.grantee WHEN 0 THEN 'PUBLIC' ELSE x.grantee::regrole::text END)
    FROM (SELECT * FROM actual EXCEPT SELECT * FROM expected) x
    WHERE x.schema_name IN (SELECT schema_name FROM approved)
    UNION ALL
    SELECT format('%s: %s to %s is listed, not granted', x.schema_name, x.privilege, x.grantee::regrole)
    FROM (SELECT * FROM expected EXCEPT SELECT * FROM actual) x
    WHERE x.schema_name IN (SELECT name FROM pg_temp.application_schema())
$$;

DO $$
DECLARE
    v_bad text;
BEGIN
    SELECT string_agg(d, E'\n  ' ORDER BY d) INTO v_bad FROM pg_temp.schema_drift() d;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: application schemas differ from pg_temp.approved_schema_grant():\n  %', v_bad
            USING HINT = 'A new schema, or a new grant on one, is added to pg_temp.approved_schema_grant() in deploy.sql.';
    END IF;

    CREATE SCHEMA schema_probe;
    EXECUTE format('GRANT USAGE ON SCHEMA core TO %I', pg_temp.deployment_setting('database_kernel_role'));
    EXECUTE format('REVOKE USAGE ON SCHEMA internal FROM %I', pg_temp.deployment_setting('database_kernel_role'));
    GRANT USAGE ON SCHEMA public TO PUBLIC;
    SELECT string_agg(d, '; ' ORDER BY d) INTO v_bad FROM pg_temp.schema_drift() d;
    IF v_bad IS DISTINCT FROM format('core: USAGE granted to %s, not in the list; internal: USAGE to %s is listed, not granted; '
                                     'public: USAGE granted to PUBLIC, not in the list; schema_probe: not an approved schema',
                                     pg_temp.deployment_setting('database_kernel_role')::regrole,
                                     pg_temp.deployment_setting('database_kernel_role')::regrole) THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name an unapproved schema, an extra grant, a missing grant and PUBLIC, named: %', v_bad;
    END IF;
END $$;
