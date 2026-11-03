-- ============================================================================
-- Test: every application table is forced to row-level security
-- ============================================================================
-- Handlers run as the owner, and row-level security binds a table's owner only
-- when the table is forced. So every table the owner has in an application
-- schema is ENABLE + FORCE ROW LEVEL SECURITY with at least one policy
-- (core.apply_org_rls for an organization's rows, pg_temp.kernel_passes for
-- the kernel's), or declared exempt with a reason
-- (pg_temp.exempt_from_rls, recorded in internal.rls_exemption). Beyond that:
--
--   force without enable  FORCE on a table whose row-level security is off
--                         protects nothing and reads as if it did
--   child unprotected     a table without an organization column
--                         (pg_temp.tenant_column) whose foreign key points at
--                         a table with one: its rows belong to an organization
--                         too, so an exemption does not cover it
--   granted without RLS   a table with an organization column and row-level
--                         security off, readable by a role other than the
--                         owner, the admin login or the kernel
--   stale exemption       an exemption whose table is gone, or now forced
--                         with a policy
-- ============================================================================

CREATE FUNCTION pg_temp.rls_gap()
RETURNS TABLE (relation text, problem text)
LANGUAGE sql AS $$
    WITH rel AS (
        SELECT c.oid, format('%I.%I', s.name, c.relname) AS name,
               c.relrowsecurity AS enabled, c.relforcerowsecurity AS forced, c.relacl,
               c.relowner = pg_temp.deployment_setting('database_owner_role')::regrole AS owned,
               c.relrowsecurity AND c.relforcerowsecurity
                   AND EXISTS (SELECT FROM pg_policy p WHERE p.polrelid = c.oid) AS protected,
               EXISTS (SELECT FROM pg_attribute a
                       WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
                         AND a.attname IN (SELECT pg_temp.tenant_column())) AS tenant,
               EXISTS (SELECT FROM internal.rls_exemption e WHERE e.relation = c.oid) AS exempt
        FROM pg_class c
        JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
        WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
    ),
    trusted(oid) AS (
        SELECT pg_temp.deployment_setting(r)::regrole::oid
        FROM unnest(ARRAY['database_owner_role', 'database_admin_role', 'database_kernel_role']) r
    )
    SELECT name, CASE WHEN NOT enabled THEN 'row-level security not enabled'
                      WHEN NOT forced THEN 'row-level security not forced'
                      ELSE 'no policy' END
    FROM rel WHERE owned AND NOT protected AND NOT exempt
    UNION ALL
    SELECT name, 'forced without being enabled' FROM rel WHERE forced AND NOT enabled
    UNION ALL
    SELECT ch.name, 'child of an organization''s table without forced row-level security' FROM rel ch
    WHERE NOT ch.tenant AND NOT ch.protected
      AND EXISTS (SELECT FROM pg_constraint k JOIN rel pa ON pa.oid = k.confrelid
                  WHERE k.conrelid = ch.oid AND k.contype = 'f' AND pa.tenant)
    UNION ALL
    SELECT name, 'organization rows readable without row-level security' FROM rel
    WHERE tenant AND NOT enabled
      AND EXISTS (SELECT FROM aclexplode(relacl) a
                  WHERE a.privilege_type = 'SELECT' AND a.grantee NOT IN (SELECT oid FROM trusted))
    UNION ALL
    SELECT CASE WHEN c.oid IS NULL THEN e.relation::oid::text ELSE format('%s.%I', c.relnamespace::regnamespace, c.relname) END,
           'stale exemption: ' || CASE WHEN c.oid IS NULL THEN 'the table is gone' ELSE 'the table is forced with a policy' END
    FROM internal.rls_exemption e
    LEFT JOIN pg_class c ON c.oid = e.relation
    WHERE c.oid IS NULL OR (c.relrowsecurity AND c.relforcerowsecurity
                            AND EXISTS (SELECT FROM pg_policy p WHERE p.polrelid = c.oid));
$$;

DO $$
DECLARE
    v_gap text;
BEGIN
    SELECT string_agg(relation || ': ' || problem, ', ' ORDER BY relation, problem) INTO v_gap FROM pg_temp.rls_gap();
    IF v_gap IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: table(s) neither forced to row-level security with a policy nor exempt: %', v_gap
            USING HINT = 'In the run-once file that creates the table: SELECT core.apply_org_rls(''<table>''); for an organization''s rows, or SELECT pg_temp.exempt_from_rls(''<table>'', ''<reason>'');';
    END IF;

    IF NOT EXISTS (SELECT FROM pg_class c JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
                   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname IN (SELECT pg_temp.tenant_column())
                   WHERE c.relkind = 'r' AND c.relforcerowsecurity) THEN
        RAISE EXCEPTION 'TEST FAILED: no forced table has an organization column; pg_temp.tenant_column() names none the database uses';
    END IF;

    CREATE TABLE core.rls_gap_probe (organization_id uuid, created_by_user_id uuid);
    IF (SELECT problem FROM pg_temp.rls_gap() WHERE relation = 'core.rls_gap_probe') IS DISTINCT FROM 'row-level security not enabled' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name a table without row-level security';
    END IF;
    ALTER TABLE core.rls_gap_probe ENABLE ROW LEVEL SECURITY;
    IF (SELECT problem FROM pg_temp.rls_gap() WHERE relation = 'core.rls_gap_probe') IS DISTINCT FROM 'row-level security not forced' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name a table that is enabled, not forced';
    END IF;
    ALTER TABLE core.rls_gap_probe FORCE ROW LEVEL SECURITY;
    IF (SELECT problem FROM pg_temp.rls_gap() WHERE relation = 'core.rls_gap_probe') IS DISTINCT FROM 'no policy' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name a forced table without a policy';
    END IF;
    PERFORM core.apply_org_rls('core.rls_gap_probe');
    IF EXISTS (SELECT FROM pg_temp.rls_gap() WHERE relation = 'core.rls_gap_probe') THEN
        RAISE EXCEPTION 'TEST FAILED: a table under core.apply_org_rls is not a gap';
    END IF;

    CREATE TABLE core.rls_exempt_probe (id int);
    PERFORM pg_temp.exempt_from_rls('core.rls_exempt_probe', 'test probe');
    IF EXISTS (SELECT FROM pg_temp.rls_gap() WHERE relation = 'core.rls_exempt_probe') THEN
        RAISE EXCEPTION 'TEST FAILED: an exempt table is not a gap';
    END IF;
    BEGIN
        PERFORM pg_temp.exempt_from_rls('core.rls_exempt_probe', ' ');
        RAISE EXCEPTION 'TEST FAILED: an exemption without a reason was accepted';
    EXCEPTION WHEN check_violation THEN
        NULL;
    END;
END $$;

DO $$
DECLARE
    v_api     text := pg_temp.deployment_setting('database_api_role');
    v_planted text[] := ARRAY[
        'core.rls_child_probe: child of an organization''s table without forced row-level security',
        'core.rls_force_probe: forced without being enabled',
        'core.rls_log_probe: organization rows readable without row-level security',
        'core.rls_stale_probe: stale exemption: the table is forced with a policy'];
    v_found   text[];
    v_gone    oid;
BEGIN
    CREATE TABLE core.rls_parent_probe (object_id uuid PRIMARY KEY, organization_id uuid NOT NULL);
    PERFORM core.apply_org_rls('core.rls_parent_probe', p_has_created_by => false);
    CREATE TABLE core.rls_child_probe (parent_id uuid REFERENCES core.rls_parent_probe);
    PERFORM pg_temp.exempt_from_rls('core.rls_child_probe', 'an exemption does not cover a child');
    CREATE TABLE core.rls_force_probe (id int);
    PERFORM pg_temp.exempt_from_rls('core.rls_force_probe', 'test probe');
    ALTER TABLE core.rls_force_probe FORCE ROW LEVEL SECURITY;
    CREATE TABLE core.rls_log_probe (organization_id uuid);
    PERFORM pg_temp.exempt_from_rls('core.rls_log_probe', 'test probe');
    EXECUTE format('GRANT SELECT ON core.rls_log_probe TO %I', v_api);
    CREATE TABLE core.rls_stale_probe (organization_id uuid);
    PERFORM pg_temp.exempt_from_rls('core.rls_stale_probe', 'test probe');
    PERFORM core.apply_org_rls('core.rls_stale_probe', p_has_created_by => false);

    SELECT array_agg(relation || ': ' || problem ORDER BY relation, problem) INTO v_found FROM pg_temp.rls_gap();
    IF v_found IS DISTINCT FROM v_planted THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name exactly the planted violations, named: %', v_found;
    END IF;

    CREATE TABLE core.rls_gone_probe (id int);
    PERFORM pg_temp.exempt_from_rls('core.rls_gone_probe', 'test probe');
    v_gone := 'core.rls_gone_probe'::regclass;
    DROP TABLE core.rls_gone_probe;
    IF NOT EXISTS (SELECT FROM pg_temp.rls_gap() WHERE relation = v_gone::text AND problem = 'stale exemption: the table is gone') THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name the exemption of a dropped table';
    END IF;
END $$;
