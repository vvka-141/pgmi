-- ============================================================================
-- deploy/file_api.sql
-- Helpers the project files and tests call: provider roles and schemas,
-- kernel ownership, and the access baseline a deploy must not lose.
-- ============================================================================

-- A managed provider's own roles and schemas (provider_role_pattern,
-- provider_schemas): the schema, routine and membership grant guards in
-- lib/__test__/ and membership/__test__/ leave them out. Row-level security
-- never does.
CREATE FUNCTION pg_temp.provider_role(p_role oid)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT coalesce(pattern <> '' AND r.rolname LIKE pattern, false)
    FROM pg_temp.deployment_setting('provider_role_pattern', false) pattern
    LEFT JOIN pg_roles r ON r.oid = p_role
$$;

CREATE FUNCTION pg_temp.provider_schema()
RETURNS SETOF text LANGUAGE sql STABLE AS $$
    SELECT jsonb_array_elements_text(pg_temp.deployment_setting('provider_schemas', false)::jsonb)
$$;

-- Hands a SECURITY DEFINER routine to the kernel role right after its CREATE:
-- it then runs as the kernel, which the forced tables let pass by
-- pg_temp.kernel_passes. p_owner_executes: whether handlers and gateways
-- (the owner) may call it; false keeps it for other kernels. Never granted
-- to a login. The kernel holds CREATE on the schema only for the transfer.
CREATE FUNCTION pg_temp.kernel_owns(p_routine regprocedure, p_owner_executes boolean DEFAULT true)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_owner  text := pg_temp.deployment_setting('database_owner_role');
    v_kernel text := pg_temp.deployment_setting('database_kernel_role');
    v_schema text := (SELECT pronamespace::regnamespace::text FROM pg_proc WHERE oid = p_routine);
BEGIN
    EXECUTE format('GRANT CREATE ON SCHEMA %s TO %I', v_schema, v_kernel);
    EXECUTE format('ALTER ROUTINE %s OWNER TO %I', p_routine, v_kernel);
    EXECUTE format('REVOKE CREATE ON SCHEMA %s FROM %I', v_schema, v_kernel);
    IF p_owner_executes THEN
        EXECUTE format('SET ROLE %I', v_kernel);
        EXECUTE format('GRANT EXECUTE ON ROUTINE %s TO %I', p_routine, v_owner);
        EXECUTE format('SET ROLE %I', v_caller);
    END IF;
END $$;

-- Lets the kernel's routines perform p_operations on a table: the privileges,
-- and one policy <table>_kernel TO the kernel that passes every row. Called
-- from the run-once file that enables the table's row-level security.
CREATE FUNCTION pg_temp.kernel_passes(p_table regclass, p_operations text[])
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_kernel text := pg_temp.deployment_setting('database_kernel_role');
BEGIN
    EXECUTE format('CREATE POLICY %I ON %s TO %I USING (true) WITH CHECK (true)',
                   (SELECT relname FROM pg_class WHERE oid = p_table) || '_kernel', p_table, v_kernel);
    EXECUTE format('GRANT %s ON %s TO %I', array_to_string(p_operations, ', '), p_table, v_kernel);
END $$;

-- Takes every kernel routine back to the owner before the first file, so the
-- files' CREATE OR REPLACE works; their kernel_owns calls hand each one back.
-- Switches back to the deploy login itself: the owner holds the kernel WITH
-- INHERIT FALSE.
CREATE FUNCTION pg_temp.kernel_reclaim()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_caller  text := current_user;
    v_owner   text := pg_temp.deployment_setting('database_owner_role');
    v_kernel  text := pg_temp.deployment_setting('database_kernel_role');
    v_lent    boolean;
    v_routine regprocedure;
BEGIN
    EXECUTE 'RESET ROLE';
    v_lent := NOT pg_has_role(current_user, v_kernel, 'USAGE');
    IF v_lent THEN
        EXECUTE format('GRANT %I TO CURRENT_USER WITH INHERIT TRUE, SET FALSE', v_kernel);
    END IF;
    FOR v_routine IN SELECT oid FROM pg_proc WHERE proowner = v_kernel::regrole LOOP
        EXECUTE format('ALTER ROUTINE %s OWNER TO %I', v_routine, v_owner);
    END LOOP;
    IF v_lent THEN
        EXECUTE format('REVOKE %I FROM CURRENT_USER', v_kernel);
    END IF;
    EXECUTE format('SET ROLE %I', v_caller);
END $$;


-- ============================================================================
-- ACCESS
-- ============================================================================
-- Every schema the application owns, found in the catalog: the template's five
-- and any a project file creates. Not the system schemas, not extensions, not a
-- schema an extension created.
CREATE FUNCTION pg_temp.application_schema()
RETURNS TABLE (oid oid, name text) LANGUAGE sql STABLE AS $$
    SELECT n.oid, n.nspname::text
    FROM pg_namespace n
    WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'extensions')
      AND n.nspname !~ '^pg_(toast|temp_)'
      AND NOT EXISTS (
          SELECT FROM pg_depend d
          WHERE d.classid = 'pg_namespace'::regclass AND d.objid = n.oid AND d.deptype = 'e')
$$;

-- Each row policy of an application schema, keyed by table and name.
CREATE FUNCTION pg_temp.policy_definition()
RETURNS TABLE (policy text, definition text) LANGUAGE sql STABLE AS $$
    SELECT format('%I.%I %I', p.schemaname, p.tablename, p.policyname),
           concat_ws(' | ', p.permissive, p.cmd, p.roles::text, 'USING ' || p.qual, 'WITH CHECK ' || p.with_check)
    FROM pg_policies p
    JOIN pg_temp.application_schema() s ON s.name = p.schemaname
$$;

-- What a deploy must not take away silently: each grant on an application
-- schema, relation (sequences included), column or routine, each row policy and
-- each user trigger, one row each; plus one row with a NULL entry per object,
-- which tells "the object is gone" from "the object lost an entry". The owner
-- role's privileges are not listed (a kernel routine moves to the owner and
-- back every deploy), nor EXECUTE to PUBLIC, which deploy/finalize.sql
-- revokes on purpose.
CREATE FUNCTION pg_temp.access_entry()
RETURNS TABLE (object text, entry text) LANGUAGE sql STABLE AS $$
    WITH obj AS (
        SELECT format('%I', s.name) AS object, n.nspacl AS acl, n.nspowner AS owner, false AS is_routine
        FROM pg_namespace n JOIN pg_temp.application_schema() s ON s.oid = n.oid
        UNION ALL
        SELECT format('%I.%I', s.name, c.relname), c.relacl, c.relowner, false
        FROM pg_class c JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
        WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
        UNION ALL
        SELECT format('%I.%I.%I', s.name, c.relname, a.attname), a.attacl, c.relowner, false
        FROM pg_attribute a
        JOIN pg_class c ON c.oid = a.attrelid
        JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
        WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND a.attnum > 0 AND NOT a.attisdropped
        UNION ALL
        SELECT format('%I.%I(%s)', s.name, p.proname, pg_get_function_identity_arguments(p.oid)), p.proacl, p.proowner, true
        FROM pg_proc p JOIN pg_temp.application_schema() s ON s.oid = p.pronamespace
    )
    SELECT o.object, NULL::text FROM obj o
    UNION
    SELECT o.object, format('grant %s to %s', a.privilege_type,
                            CASE a.grantee WHEN 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END)
    FROM obj o CROSS JOIN LATERAL aclexplode(o.acl) a
    WHERE a.grantee <> o.owner
      AND a.grantee <> pg_temp.deployment_setting('database_owner_role')::regrole
      AND NOT (o.is_routine AND a.grantee = 0)
    UNION
    SELECT format('%I.%I', s.name, c.relname), 'policy ' || pol.polname
    FROM pg_policy pol
    JOIN pg_class c ON c.oid = pol.polrelid
    JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
    UNION
    SELECT format('%I.%I', s.name, c.relname), 'trigger ' || t.tgname
    FROM pg_trigger t
    JOIN pg_class c ON c.oid = t.tgrelid
    JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
    WHERE NOT t.tgisinternal
$$;

CREATE TEMPORARY TABLE access_baseline (object text NOT NULL, entry text);
CREATE TEMPORARY TABLE access_removal (object text NOT NULL, entry text NOT NULL, PRIMARY KEY (object, entry));

-- Catalog files pg_temp.sync_catalog (lib/core/catalogs.sql) synced in this
-- deploy. A file under data/catalogs/ that none synced fails the deploy.
CREATE TEMPORARY TABLE catalog_synced (path text PRIMARY KEY);

-- Entries the deploy found before its first file ran (pg_temp.access_baseline)
-- and lacks after its last, on an object that still exists. A file that takes
-- one away on purpose says so in the same deploy:
--   INSERT INTO pg_temp.access_removal VALUES ('core.task', 'grant SELECT to app_api');
CREATE FUNCTION pg_temp.access_lost()
RETURNS TABLE (object text, entry text) LANGUAGE sql STABLE AS $$
    WITH now_state AS MATERIALIZED (SELECT * FROM pg_temp.access_entry())
    SELECT b.object, b.entry
    FROM pg_temp.access_baseline b
    WHERE b.entry IS NOT NULL
      AND EXISTS (SELECT FROM now_state n WHERE n.object = b.object AND n.entry IS NULL)
      AND NOT EXISTS (SELECT FROM now_state n WHERE n.object = b.object AND n.entry = b.entry)
      AND NOT EXISTS (SELECT FROM pg_temp.access_removal r WHERE r.object = b.object AND r.entry = b.entry)
$$;
