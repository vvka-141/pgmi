-- ============================================================================
-- deploy/bootstrap.sql
-- Runs as the deploy login, before SET ROLE: the host's grants, the
-- roles, the database settings, the extensions schema and the extensions.
-- Every statement converges, so a redeploy changes nothing that is in place.
-- ============================================================================

-- ############################################################################
-- HOST - what the host must allow, before anything changes
-- ############################################################################
-- What the host must allow, checked before anything changes, so a managed
-- host's missing grant is one clear error instead of a failure halfway. The
-- role attributes matter on a first deploy only; a redeploy finds its roles
-- and its database owner in place.
DO $$
DECLARE
    v_missing text;
    v_allowed text := current_setting('azure.extensions', true);
BEGIN
    SELECT string_agg(a.attribute, ' and ' ORDER BY a.attribute) INTO v_missing
    FROM pg_roles r
    CROSS JOIN LATERAL (VALUES ('CREATEDB', r.rolcreatedb), ('CREATEROLE', r.rolcreaterole)) a(attribute, held)
    WHERE r.rolname = current_user AND NOT r.rolsuper AND NOT a.held
      AND to_regclass('internal.environment') IS NULL;
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'The login % lacks %: a first deploy of the advanced template creates its roles and hands the database to them', current_user, v_missing
            USING ERRCODE = 'insufficient_privilege',
                  HINT = format('Deploy as the server''s administrator login, or have one run: ALTER ROLE %I CREATEDB CREATEROLE;', current_user);
    END IF;

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

-- The quieting helpers below are called by deploy/ledger.sql and the test
-- reporter in deploy/finalize.sql as well. They are defined here, by the
-- deploy login, so every role a test switches to can still execute them.

-- The framework under ./lib/ narrates every object it installs and every
-- assertion it checks: several hundred NOTICE lines, 'already exists, skipping'
-- included, on every green deploy. Those files and tests run at WARNING, so a
-- green run shows your own files' NOTICEs and one line per framework file.
-- Only a session at the default NOTICE level is quieted: --verbose (DEBUG)
-- still sees everything.
CREATE FUNCTION pg_temp.quiet_framework(p_path text)
RETURNS void LANGUAGE sql AS $$
    SELECT set_config('client_min_messages', 'warning', true)
    WHERE p_path LIKE './lib/%' AND current_setting('client_min_messages') = 'notice'
$$;

CREATE FUNCTION pg_temp.unquiet_framework(p_path text)
RETURNS void LANGUAGE sql AS $$
    SELECT set_config('client_min_messages', 'notice', true)
    WHERE p_path LIKE './lib/%' AND current_setting('client_min_messages') = 'warning'
$$;

-- The bootstrap's IF NOT EXISTS statements say "already exists, skipping" on
-- every redeploy. The same rule quiets them: only a NOTICE session, and only
-- back to NOTICE when this lowered it.
CREATE FUNCTION pg_temp.quiet_bootstrap()
RETURNS void LANGUAGE sql AS $$
    SELECT set_config('client_min_messages', 'warning', true), set_config('deploy.bootstrap_quieted', 'on', true)
    WHERE current_setting('client_min_messages') = 'notice'
$$;

CREATE FUNCTION pg_temp.unquiet_bootstrap()
RETURNS void LANGUAGE sql AS $$
    SELECT set_config('client_min_messages', 'notice', true), set_config('deploy.bootstrap_quieted', '', true)
    WHERE current_setting('deploy.bootstrap_quieted', true) = 'on'
$$;

-- ############################################################################
-- ROLES - owner, kernel, api and admin, and the database handed to the owner
-- ############################################################################
DO $roles$
DECLARE
    v_owner_role TEXT := pg_temp.deployment_setting('database_owner_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
    v_admin_password TEXT := pg_temp.deployment_setting('database_admin_password');
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_kernel_role TEXT := pg_temp.deployment_setting('database_kernel_role');
    v_jobs_role TEXT := pg_temp.deployment_setting('database_jobs_role');
    v_messages TEXT := current_setting('client_min_messages');
    v_role TEXT;
    v_foreign TEXT;
BEGIN
    -- Role names follow from the database name, so any CREATEROLE login can
    -- create them first. A role is adopted only when nobody but the deploy
    -- login holds ADMIN on it (a non-superuser creator keeps an ADMIN grant it
    -- cannot drop) and the owner, admin and kernel roles have no unexpected
    -- member. The api and jobs roles' plain members are the operator's to grant.
    SELECT r.rolname, string_agg(DISTINCT format('%I%s', m.rolname, CASE WHEN a.admin_option THEN ' (ADMIN)' END), ', ')
    INTO v_role, v_foreign
    FROM pg_auth_members a
    JOIN pg_roles r ON r.oid = a.roleid
    JOIN pg_roles m ON m.oid = a.member
    WHERE r.rolname IN (v_owner_role, v_admin_role, v_kernel_role, v_api_role, v_jobs_role)
      AND m.rolname <> CURRENT_USER
      AND (a.admin_option
           OR r.rolname = v_admin_role
           OR (r.rolname = v_owner_role AND m.rolname <> v_admin_role)
           OR (r.rolname = v_kernel_role AND m.rolname <> v_owner_role))
    GROUP BY r.rolname
    ORDER BY r.rolname
    LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'role % already exists with members this deploy did not grant: %', v_role, v_foreign
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'A deploy adopts no role another login controls. Drop the role, or have a superuser revoke those memberships. After rotating the deploy login, revoke the old login''s ADMIN or drop that login.';
    END IF;

    -- Owner role (NOLOGIN) - owns all database objects
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_owner_role) THEN
        EXECUTE format('CREATE ROLE %I NOLOGIN', v_owner_role);
        RAISE NOTICE '[pgmi] Created owner role: %', v_owner_role;
    END IF;

    -- Kernel role (NOLOGIN) - owns the SECURITY DEFINER routines that work
    -- across organizations or before one exists (pg_temp.kernel_owns). Forced
    -- RLS lets it pass by its own policies (pg_temp.kernel_passes). The owner
    -- holds it WITH INHERIT FALSE: handlers run as the owner and must never
    -- match a kernel policy; SET TRUE lets the deploy hand routines over.
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_kernel_role) THEN
        EXECUTE format('CREATE ROLE %I NOLOGIN', v_kernel_role);
        RAISE NOTICE '[pgmi] Created kernel role: %', v_kernel_role;
    END IF;

    -- API role (NOLOGIN group) - the gateway's permission bundle: the entry
    -- points deploy/finalize.sql grants it, nothing else
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_api_role) THEN
        EXECUTE format('CREATE ROLE %I NOLOGIN', v_api_role);
        RAISE NOTICE '[pgmi] Created API role: %', v_api_role;
    END IF;

    -- Admin role (LOGIN) - inherits owner + api. Roles belong to the cluster:
    -- an existing admin role that is not yet a member of this database's
    -- owner role belongs to something else, and its password stays its own.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_admin_role)
       AND NOT EXISTS (SELECT 1 FROM pg_auth_members m
                       WHERE m.roleid = to_regrole(quote_ident(v_owner_role))
                         AND m.member = to_regrole(quote_ident(v_admin_role))) THEN
        RAISE EXCEPTION 'database_admin_role % already exists and is not a member of % (this database''s owner role)', v_admin_role, v_owner_role
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'A deploy adopts no login it did not create. Choose another database_admin_role, or grant the owner role to this one on purpose first.';
    END IF;

    -- The handler keeps the expanded statement, password included, out of the
    -- error's CONTEXT, which reaches stderr, --json and the server log.
    BEGIN
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_admin_role) THEN
            EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L CONNECTION LIMIT 10', v_admin_role, v_admin_password);
            RAISE NOTICE '[pgmi] Created admin role: %', v_admin_role;
        ELSE
            EXECUTE format('ALTER ROLE %I WITH PASSWORD %L CONNECTION LIMIT 10', v_admin_role, v_admin_password);
        END IF;
    EXCEPTION WHEN OTHERS THEN
        RAISE EXCEPTION USING
            ERRCODE = SQLSTATE,
            MESSAGE = format('setting the password of role %I failed: %s', v_admin_role, SQLERRM);
    END;

    -- Role hierarchy. Membership in the api role is the trust gate: only its
    -- members' auth.* settings count (membership.fn_is_trusted_context), and
    -- its gateway entry points open sessions and act for whoever a session
    -- names. Re-granting converges the options; its "already granted" notice
    -- on every redeploy is not news.
    PERFORM set_config('client_min_messages', 'warning', true)
    WHERE v_messages = 'notice';
    EXECUTE format('GRANT %I TO %I WITH INHERIT FALSE, SET TRUE', v_kernel_role, v_owner_role);
    EXECUTE format('GRANT %I, %I TO %I', v_owner_role, v_api_role, v_admin_role);

    -- The deploy login: SET ROLE to the owner, and trusted like a gateway, so
    -- tests and seeds can state a request context.
    EXECUTE format('GRANT %I, %I TO CURRENT_USER', v_owner_role, v_api_role);
    PERFORM set_config('client_min_messages', v_messages, true);

    -- Every login takes the database's search_path; a role setting would
    -- override it, and roles outlive the database.
    EXECUTE format('ALTER ROLE %I RESET search_path', v_owner_role);
    EXECUTE format('ALTER ROLE %I RESET search_path', v_admin_role);
    EXECUTE format('ALTER ROLE %I RESET search_path', v_api_role);

    EXECUTE format('ALTER DATABASE %I OWNER TO %I', current_database(), v_owner_role);
    RAISE NOTICE '[pgmi] Database % owned by %', current_database(), v_owner_role;

    -- Every role in the cluster may connect to a new database and create
    -- temporary objects in it, through PUBLIC. Only the template's roles may.
    EXECUTE format('REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC', current_database());
    EXECUTE format('GRANT CONNECT, TEMPORARY ON DATABASE %I TO %I, %I',
                   current_database(), v_admin_role, v_api_role);

    -- PostgreSQL grants EXECUTE to PUBLIC on every routine it creates. Every
    -- project file runs as the owner, so from here on a routine is callable
    -- only by a role it was granted to. Extensions are created by the session
    -- user and keep their PUBLIC grants.
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I REVOKE EXECUTE ON ROUTINES FROM PUBLIC', v_owner_role);
END $roles$;


-- ############################################################################
-- DATABASE SETTINGS - pg_temp.database_setting() in deploy/settings.sql
-- ############################################################################
DO $database_setting$
DECLARE
    v_setting record;
BEGIN
    FOR v_setting IN SELECT * FROM pg_temp.database_setting() LOOP
        PERFORM set_config(v_setting.name, v_setting.value, false);
        EXECUTE format('ALTER DATABASE %I SET %s FROM CURRENT', current_database(), v_setting.name);
    END LOOP;
END $database_setting$;


-- ############################################################################
-- EXTENSIONS - in schema extensions, where lib/ calls them
-- ############################################################################
-- An image or provider that pre-installs an extension in public would turn
-- CREATE EXTENSION IF NOT EXISTS into a silent no-op, so a pre-installed one
-- is moved instead.
SELECT pg_temp.quiet_bootstrap();
CREATE SCHEMA IF NOT EXISTS extensions;
SELECT pg_temp.unquiet_bootstrap();
COMMENT ON SCHEMA extensions IS 'PostgreSQL extensions isolated from application schemas. Keeps extension objects out of search_path conflicts.';
GRANT USAGE ON SCHEMA extensions TO PUBLIC;

-- A provider's event trigger grants its own roles CREATE on every new schema
-- (Lakebase: databricks_writer_<id>, databricks_superuser). A role that can
-- create in a schema on a search_path can shadow a function called there and
-- run as a SECURITY DEFINER caller, so the provider keeps USAGE only.
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

SELECT pg_temp.revoke_provider_create('{extensions}');

-- The same trigger grants TRUNCATE, REFERENCES, TRIGGER and MAINTAIN on every
-- new table (Lakebase: databricks_superuser). They act on the whole table,
-- past row-level security, so deploy/finalize.sql revokes them before the
-- test gate.
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

DO $extensions$
DECLARE
    v_ext record;
BEGIN
    FOR v_ext IN
        SELECT r.name, e.extnamespace::regnamespace::text AS schema, e.extrelocatable
        FROM pg_temp.required_extension() AS r(name)
        LEFT JOIN pg_extension e ON e.extname = r.name
        ORDER BY r.name
    LOOP
        IF v_ext.schema IS NULL THEN
            EXECUTE format('CREATE EXTENSION %I SCHEMA extensions', v_ext.name);
        ELSIF v_ext.schema = 'extensions' THEN
            CONTINUE;
        ELSIF v_ext.extrelocatable THEN
            EXECUTE format('ALTER EXTENSION %I SET SCHEMA extensions', v_ext.name);
            RAISE NOTICE '[pgmi] Moved extension % from schema % to extensions', v_ext.name, v_ext.schema;
        ELSE
            RAISE EXCEPTION 'Extension % is installed in schema %, cannot be moved, and the template needs it in schema extensions', v_ext.name, v_ext.schema
                USING HINT = format('Recreate it there before deploying: DROP EXTENSION %I; CREATE EXTENSION %I SCHEMA extensions; (DROP ... CASCADE also drops what depends on it).', v_ext.name, v_ext.name);
        END IF;
    END LOOP;
END $extensions$;


-- ############################################################################
-- PUBLIC - PostgreSQL's own schema stays closed
-- ############################################################################
-- public stays closed. Who may close it depends on the host: on Azure
-- azure_pg_admin owns it, and only the deploy login belongs to that group; on
-- Lakebase the login's revoke acts for databricks_superuser and removes
-- nothing, while the owner role's does. So both revoke: the login here, the
-- owner role in deploy/schemas.sql.
REVOKE ALL ON SCHEMA public FROM PUBLIC;
