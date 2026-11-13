-- ============================================================================
-- deploy/settings.sql
-- The lists a project edits: required extensions, database settings,
-- schema grants, tenant columns and the optional deploy guards. Pure
-- definitions; nothing here changes the database.
-- ============================================================================

CREATE FUNCTION pg_temp.required_extension()
RETURNS SETOF text LANGUAGE sql IMMUTABLE AS $$
    SELECT name FROM (VALUES ('btree_gist'), ('hstore'), ('pg_trgm'), ('pgcrypto'), ('uuid-ossp')) AS r(name)
$$;

-- What the database keeps for every new session, one row per setting. Each is
-- applied to this session too and stored FROM CURRENT, so the database holds
-- the server's own spelling of the value.
CREATE FUNCTION pg_temp.database_setting()
RETURNS TABLE (name text, value text) LANGUAGE sql IMMUTABLE AS $$
    VALUES
        ('search_path', 'core, api, membership, internal, extensions, common, pg_temp')
$$;

-- The application schemas and each privilege granted on them, the role named
-- by its deploy parameter; a schema without a grant has one row with a NULL
-- role. public is PostgreSQL's own and stays closed. A project that adds a
-- schema or a grant adds its rows here: lib/__test__/test_approved_schemas.sql
-- fails on any schema or schema grant this list does not hold.
CREATE FUNCTION pg_temp.approved_schema_grant()
RETURNS TABLE (schema_name text, role_key text, privilege text)
LANGUAGE sql IMMUTABLE AS $$
    VALUES ('api',        'database_api_role',    'USAGE'),
           ('api',        'database_kernel_role', 'USAGE'),
           ('api',        'database_jobs_role',   'USAGE'),
           ('common',     'database_admin_role',  'USAGE'),
           ('common',     'database_api_role',    'USAGE'),
           ('common',     'database_kernel_role', 'USAGE'),
           ('core',       'database_api_role',    'USAGE'),
           ('core',       'database_kernel_role', 'USAGE'),
           ('internal',   'database_kernel_role', 'USAGE'),
           ('membership', 'database_admin_role',  'USAGE'),
           ('membership', 'database_api_role',    'USAGE'),
           ('membership', 'database_kernel_role', 'USAGE'),
           ('public',     NULL,                   NULL)
$$;

-- The columns that name a row's organization: core tables use organization_id
-- (core.apply_org_rls), membership tables organization_object_id. The RLS and
-- foreign-key guards in lib/__test__/ read them from here.
CREATE FUNCTION pg_temp.tenant_column()
RETURNS SETOF name LANGUAGE sql IMMUTABLE AS $$
    VALUES ('organization_id'::name), ('organization_object_id')
$$;

-- Optional deploy guards: lib/__test__/test_option_<name>.sql, each off until
-- switched on here (README "Deploy guards"). Off, a file still proves on a
-- planted object that its check works; on, it also holds the database to it.
CREATE FUNCTION pg_temp.guard_enabled(p_name text)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    v_enabled boolean;
BEGIN
    SELECT o.enabled INTO v_enabled FROM (VALUES
        ('strict_fk_index',      false),
        ('rls_index_method',     false),
        ('plpgsql_check',        false),
        ('sql_lint',             false),
        ('naming',               false),
        ('handler_reads_views',  false),
        ('view_column_comments', false),
        ('trigger_allowlist',    false)
    ) o(name, enabled)
    WHERE o.name = p_name;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'No deploy guard option named %', p_name;
    END IF;
    RETURN v_enabled;
END $$;

-- What an enabled option lets pass, one row per object, each with the reason.
-- The object is spelled as the option's error names it; an entry naming
-- nothing the option would flag fails as stale.
CREATE FUNCTION pg_temp.guard_exemption()
RETURNS TABLE (guard text, object text, reason text) LANGUAGE sql IMMUTABLE AS $$
    VALUES ('rls_index_method', 'api.ix_access_denial_bucket (brin on api.access_denial)',
            'BRIN minmax on timestamptz compares with the leakproof btree operators, so the purge by bucket still uses it under row-level security')
$$;

-- Fails the deploy when an enabled option found an object it does not exempt,
-- or holds an exemption that names nothing it found.
CREATE FUNCTION pg_temp.enforce_guard(p_guard text, p_found text[])
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_bad   text;
    v_stale text;
BEGIN
    SELECT string_agg(f, E'\n  ' ORDER BY f) INTO v_bad
    FROM unnest(p_found) f
    WHERE f NOT IN (SELECT e.object FROM pg_temp.guard_exemption() e WHERE e.guard = p_guard);
    SELECT string_agg(e.object, ', ' ORDER BY e.object) INTO v_stale
    FROM pg_temp.guard_exemption() e
    WHERE e.guard = p_guard AND e.object <> ALL (p_found);
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION E'Deploy guard %:\n  %', p_guard, v_bad
            USING HINT = format('Fix each, or add (%L, <object>, <reason>) to pg_temp.guard_exemption() in deploy/settings.sql. lib/__test__/test_option_%s.sql states the rule.', p_guard, p_guard);
    END IF;
    IF v_stale IS NOT NULL THEN
        RAISE EXCEPTION 'Deploy guard %: stale exemption(s), nothing to exempt: %', p_guard, v_stale
            USING HINT = 'Remove them from pg_temp.guard_exemption() in deploy/settings.sql.';
    END IF;
END $$;
