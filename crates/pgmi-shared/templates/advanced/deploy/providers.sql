-- ============================================================================
-- deploy/providers.sql
-- Hosting providers, in one place. The template detects the host from facts in
-- the database; pgmi itself knows no provider.
--
-- Databricks Lakebase. Detected by two marks every Lakebase database carries:
--   the role databricks_superuser and the schema __db_system (one alone does
--   not count). What it forces:
--   - an event trigger grants its roles (databricks_writer_<id>,
--     databricks_superuser) CREATE on every new schema, and TRUNCATE,
--     REFERENCES, TRIGGER and MAINTAIN on every new table: revoked below, the
--     schema grants in bootstrap.sql and schemas.sql, the table grants in
--     finalize.sql before the test gate;
--   - the login's REVOKE on public acts for databricks_superuser and removes
--     nothing, so the owner role revokes too (schemas.sql);
--   - a weak password is refused at commit, so env=dev generates one
--     (parameters.sql).
-- Azure Database for PostgreSQL flexible server. Detected by the
--   azure.extensions setting. What it forces:
--   - only extensions on that allowlist can be created: refused below, before
--     anything changes, with the az command that adds them;
--   - azure_pg_admin owns public, and only the deploy login belongs to it, so
--     the login's REVOKE on public is the one that counts (bootstrap.sql).
--
-- Override: detection only sets defaults for two parameters, which say what
-- the guards tolerate, not which host this is. provider_role_pattern (a LIKE
-- pattern for the provider's roles) and provider_schemas (a JSON array of its
-- schemas) can be passed for any host.
-- ============================================================================

-- Databricks Lakebase, told by two marks every Lakebase database carries. Its
-- defaults let the guards pass the provider's own roles and schema, so one
-- mark alone does not count.
CREATE FUNCTION pg_temp.on_lakebase()
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT to_regrole('databricks_superuser') IS NOT NULL
       AND to_regnamespace('__db_system') IS NOT NULL
$$;

-- The default of a provider parameter on this host.
CREATE FUNCTION pg_temp.provider_default(p_key text)
RETURNS text LANGUAGE sql STABLE AS $$
    SELECT CASE p_key
        WHEN 'provider_role_pattern' THEN CASE WHEN pg_temp.on_lakebase() THEN 'databricks\_%' ELSE '' END
        WHEN 'provider_schemas' THEN CASE WHEN pg_temp.on_lakebase() THEN '["__db_system"]' ELSE '[]' END
    END
$$;

-- Azure: refuses, before anything changes, a required extension the server's
-- allowlist leaves out. Any other host has no azure.extensions and passes.
CREATE FUNCTION pg_temp.refuse_disallowed_extensions()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_missing text;
    v_allowed text := current_setting('azure.extensions', true);
BEGIN
    SELECT string_agg(e, ',' ORDER BY e) INTO v_missing
    FROM pg_temp.required_extension() e
    WHERE v_allowed IS NOT NULL
      AND NOT EXISTS (SELECT FROM pg_extension x WHERE x.extname = e)
      AND upper(e) <> ALL (string_to_array(upper(regexp_replace(v_allowed, '\s', '', 'g')), ','));
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'The server does not allow the extension(s) % that the advanced template needs', v_missing
            USING ERRCODE = 'feature_not_supported',
                  HINT = format('On Azure, add them to the server parameter azure.extensions, keeping what it lists: az postgres flexible-server parameter set --resource-group <group> --server-name <server> --name azure.extensions --value %s',
                                upper(concat_ws(',', NULLIF(regexp_replace(v_allowed, '\s', '', 'g'), ''), v_missing)));
    END IF;
END $$;

-- A role that can create in a schema on a search_path can shadow a function
-- called there and run as a SECURITY DEFINER caller, so the provider keeps
-- USAGE only.
CREATE FUNCTION pg_temp.revoke_provider_create(p_schemas text[])
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_grant record;
BEGIN
    FOR v_grant IN
        SELECT DISTINCT n.nspname, r.rolname
        FROM pg_namespace n
        CROSS JOIN LATERAL aclexplode(n.nspacl) g
        JOIN pg_roles r ON r.oid = g.grantee
        CROSS JOIN pg_temp.deployment_setting('provider_role_pattern', false) pattern
        WHERE n.nspname = ANY (p_schemas) AND g.privilege_type = 'CREATE'
          AND pattern <> '' AND r.rolname LIKE pattern
    LOOP
        EXECUTE format('REVOKE CREATE ON SCHEMA %I FROM %I', v_grant.nspname, v_grant.rolname);
    END LOOP;
END $$;

-- TRUNCATE, REFERENCES, TRIGGER and MAINTAIN act on the whole table, past
-- row-level security: deploy/finalize.sql revokes them before the test gate.
CREATE FUNCTION pg_temp.revoke_provider_table_privileges()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_grant record;
BEGIN
    FOR v_grant IN
        SELECT c.oid::regclass AS rel, r.rolname, string_agg(DISTINCT g.privilege_type, ', ') AS privileges
        FROM pg_class c
        JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
        CROSS JOIN LATERAL aclexplode(c.relacl) g
        JOIN pg_roles r ON r.oid = g.grantee
        CROSS JOIN pg_temp.deployment_setting('provider_role_pattern', false) pattern
        WHERE c.relkind IN ('r', 'p')
          AND g.privilege_type IN ('TRUNCATE', 'REFERENCES', 'TRIGGER', 'MAINTAIN')
          AND pattern <> '' AND r.rolname LIKE pattern
        GROUP BY 1, 2
    LOOP
        EXECUTE format('REVOKE %s ON %s FROM %I', v_grant.privileges, v_grant.rel, v_grant.rolname);
    END LOOP;
END $$;
