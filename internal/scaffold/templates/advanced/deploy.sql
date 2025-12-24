-- ============================================================================
-- deploy.sql - Deployment Orchestrator (Advanced Template)
-- ============================================================================
-- This file controls HOW your SQL gets executed. pgmi loads your files
-- into session-scoped temporary tables and lets YOU decide execution order.
--
-- Available session tables:
--   pg_temp.pgmi_source           - Your SQL files (migrations, schemas, etc.)
--   pg_temp.pgmi_unittest_script  - Your test files (from __test__/ directories)
--   pg_temp.pgmi_parameter        - CLI params from --param key=value
--   pg_temp.pgmi_plan             - Execution plan (populated by helpers below)
--
-- Helper functions:
--   pgmi_declare_param(key, type, ...)         - Declare parameter with type validation and defaults
--   pgmi_get_param(key, default)               - Get parameter value with fallback
--   pgmi_plan_command(sql)                     - Add raw SQL to execution plan
--   pgmi_plan_notice(msg, args...)             - Add log message to plan
--   pgmi_plan_file(path)                       - Add file content to plan
--   pgmi_plan_do(plpgsql_code)                 - Add PL/pgSQL block to plan
--   pgmi_plan_tests(pattern)                   - Execute unit tests with optional path filtering
-- ============================================================================


SELECT pg_temp.pgmi_plan_command($$
    BEGIN;
    SELECT pg_temp.provision();
    SELECT pg_temp.deploy();
    SAVEPOINT before_application_tests;
$$);
SELECT pg_temp.pgmi_plan_tests();
SELECT pg_temp.pgmi_plan_command($$
    ROLLBACK TO SAVEPOINT before_application_tests;
    SELECT pg_temp.persist_unittest_metadata();
    COMMIT;
$$);



-- ============================================================================
-- STEP 0: Declare Parameters (Type-Safe Configuration)
-- ============================================================================
-- Parameters are automatically available as PostgreSQL session variables
-- with the 'pgmi.' namespace prefix (no manual initialization needed).
--
-- Parameter Contract:
--   database_owner_role      - Database owner role (NOLOGIN) - defaults to <dbname>_owner
--   database_admin_role      - Admin role (LOGIN) - defaults to <dbname>_admin
--   database_admin_password  - Admin password - REQUIRED (no default)
--   database_api_role        - API client role (LOGIN) - defaults to <dbname>_api
--   database_api_password    - API password - REQUIRED (no default)
--   env                      - Deployment environment - defaults to 'development'
--   httpincomingqueuepartitions - HTTP framework queue partitions - defaults to 30
--
-- Example:
--   pgmi deploy . \
--     --param database_admin_password=SecureAdminP@ss \
--     --param database_api_password=SecureApiP@ss
--
-- Note: CLI parameters (--param) are already loaded as session variables.
--       Declarations add defaults and type validation.

-- Declare deployment parameters using compact VALUES+SELECT pattern
SELECT pg_temp.pgmi_declare_param(
    p_key => declarations.key,
    p_type => declarations.type,
    p_default_value => declarations.default_value,
    p_required => declarations.required,
    p_description => declarations.description
)
FROM (VALUES
    -- Database role configuration
    ('database_owner_role',          'name', current_database()::TEXT || '_owner', false, 'Database owner role (NOLOGIN)'),
    ('database_admin_role',          'name', current_database()::TEXT || '_admin', false, 'Admin role with LOGIN capability'),
    ('database_admin_password',      'text', NULL,                                 true,  'Password for database_admin_role (REQUIRED)'),
    ('database_api_role',            'name', current_database()::TEXT || '_api',   false, 'API client role with LOGIN capability'),
    ('database_api_password',        'text', NULL,                                 true,  'Password for database_api_role (REQUIRED)'),

    -- Environment configuration
    ('env',                          'text', 'development',                        false, 'Deployment environment (development, staging, production)'),

    -- HTTP framework configuration
    ('httpincomingqueuepartitions',  'int',  '30',                                 false, 'HTTP incoming request queue partition count')
) AS declarations(key, type, default_value, required, description);


-- ============================================================================
-- STEP 1: Bootstrap Infrastructure (Single Superuser Phase)
-- ============================================================================
-- Consolidates ALL privileged operations into one block, then performs
-- a SINGLE handoff to the database owner role. After the SET ROLE command,
-- the deployment NEVER returns to superuser context.
--
-- Superuser Responsibilities (BEFORE SET ROLE):
--   1. Create role hierarchy (owner, admin, api)
--   2. Create extensions schema (owned by superuser)
--   3. Install PostgreSQL extensions
--   4. Assign database ownership to owner_role
--
-- Owner Role Responsibilities (AFTER SET ROLE):
--   5. Create internal schema
--   6. Create tracking table
--   7. All remaining deployment operations
--
-- This single-handoff model ensures:
--   - Minimal privilege escalation surface
--   - Clear security boundaries
--   - No role switching during deployment

CREATE FUNCTION pg_temp.provision() RETURNS VOID 
LANGUAGE plpgsql
AS 
$infrastructure$
DECLARE
    v_owner_role TEXT := pg_temp.pgmi_get_param('database_owner_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
BEGIN
    RAISE DEBUG '═══════════════════════════════════════════════════════════════';
    RAISE DEBUG 'Bootstrap: Creating deployment infrastructure';
    RAISE DEBUG '═══════════════════════════════════════════════════════════════';

    -- ═══════════════════════════════════════════════════════════════════════
    -- SUPERUSER PHASE: Privileged Operations
    -- ═══════════════════════════════════════════════════════════════════════

    RAISE DEBUG '';
    RAISE DEBUG '→ Phase 1: Superuser Provisioning';

    -- Create database owner role if it doesn't exist
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_owner_role) THEN
        EXECUTE format('CREATE ROLE %I NOLOGIN', v_owner_role);
        RAISE NOTICE '  ✓ Created owner role: %', v_owner_role;
    ELSE
        RAISE DEBUG '  • Owner role exists: %', v_owner_role;
    END IF;

    -- Grant owner role to current user (allows SET ROLE to owner)
    -- INHERIT FALSE prevents cross-database interference
    -- SET TRUE allows switching to this role via SET ROLE
    EXECUTE format('GRANT %I TO CURRENT_USER WITH INHERIT FALSE, SET TRUE', v_owner_role);
    RAISE DEBUG '  ✓ Granted % to % (INHERIT FALSE, SET TRUE)', v_owner_role, CURRENT_USER;

    -- Create admin and API roles (LOGIN roles require superuser privileges)
    -- These must be created BEFORE SET ROLE since only superuser can create LOGIN roles
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_admin_role) THEN
        EXECUTE format(
            'CREATE ROLE %I LOGIN PASSWORD %L CONNECTION LIMIT 10',
            v_admin_role,
            pg_temp.pgmi_get_param('database_admin_password')
        );
        RAISE NOTICE '  ✓ Created admin role: % (LOGIN)', v_admin_role;
    ELSE
        -- Update password and connection limit if role exists
        EXECUTE format(
            'ALTER ROLE %I WITH PASSWORD %L CONNECTION LIMIT 10',
            v_admin_role,
            pg_temp.pgmi_get_param('database_admin_password')
        );
        RAISE DEBUG '  • Admin role exists (password updated): %', v_admin_role;
    END IF;

    EXECUTE format(
        'COMMENT ON ROLE %I IS %L',
        v_admin_role,
        format('Database administrator for %s with LOGIN capability and owner privileges',
               current_database())
    );

    -- Grant owner to admin (INHERIT TRUE for automatic permissions)
    EXECUTE format(
        'GRANT %I TO %I WITH INHERIT TRUE',
        v_owner_role,
        v_admin_role
    );
    RAISE NOTICE '  ✓ Granted % to % (INHERIT TRUE)', v_owner_role, v_admin_role;

    -- Create API role (LOGIN)
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_api_role) THEN
        EXECUTE format(
            'CREATE ROLE %I LOGIN PASSWORD %L CONNECTION LIMIT 100',
            v_api_role,
            pg_temp.pgmi_get_param('database_api_password')
        );
        RAISE NOTICE '  ✓ Created API role: % (LOGIN)', v_api_role;
    ELSE
        -- Update password and connection limit if role exists
        EXECUTE format(
            'ALTER ROLE %I WITH PASSWORD %L CONNECTION LIMIT 100',
            v_api_role,
            pg_temp.pgmi_get_param('database_api_password')
        );
        RAISE DEBUG '  • API role exists (password updated): %', v_api_role;
    END IF;

    EXECUTE format(
        'COMMENT ON ROLE %I IS %L',
        v_api_role,
        format('API client role for %s with restricted EXECUTE-only permissions on api schema',
               current_database())
    );

    RAISE NOTICE '  ✓ Role hierarchy created: owner → admin → api';

    -- Configure default search_path for all roles (requires superuser)
    -- Note: Actual schemas don't exist yet, but PostgreSQL allows setting paths to non-existent schemas
    EXECUTE format(
        'ALTER ROLE %I SET search_path = core, api, internal, extensions, utils, pg_temp',
        v_owner_role
    );

    EXECUTE format(
        'ALTER ROLE %I SET search_path = core, api, internal, extensions, utils, pg_temp',
        v_admin_role
    );

    EXECUTE format(
        'ALTER ROLE %I SET search_path = api, core, extensions, utils, pg_temp',
        v_api_role
    );

    RAISE DEBUG '  ✓ Search path configured for all roles (persists across connections)';

    -- Create extensions schema AS SUPERUSER
    -- Extension schemas typically remain owned by the superuser role
    CREATE SCHEMA IF NOT EXISTS extensions;
    COMMENT ON SCHEMA extensions IS 'PostgreSQL extensions installed by superuser';
    RAISE DEBUG '  ✓ Created extensions schema (owner: %)', CURRENT_USER;

    -- Grant USAGE + CREATE to owner role (allows extension function deployment)
    EXECUTE format('GRANT USAGE, CREATE ON SCHEMA extensions TO %I', v_owner_role);
    RAISE DEBUG '  ✓ Granted USAGE, CREATE on extensions schema to %', v_owner_role;

    GRANT USAGE ON SCHEMA extensions TO PUBLIC;
    RAISE DEBUG '  ✓ Granted USAGE on extensions schema to PUBLIC';

    -- Install PostgreSQL extensions AS SUPERUSER
    -- Extension installation requires superuser privileges
    RAISE DEBUG '  → Installing PostgreSQL extensions...';

    CREATE EXTENSION IF NOT EXISTS "uuid-ossp" SCHEMA extensions;
    CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA extensions;
    CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA extensions;
    CREATE EXTENSION IF NOT EXISTS hstore SCHEMA extensions;
    CREATE EXTENSION IF NOT EXISTS plv8;  -- PL/V8 must be in pg_catalog (procedural language)

    RAISE DEBUG '  ✓ Installed 5 extensions (4 in extensions, plv8 in pg_catalog)';

    -- Transfer database ownership to owner role (requires superuser)
    EXECUTE format('ALTER DATABASE %I OWNER TO %I', current_database(), v_owner_role);
    RAISE NOTICE '  ✓ Database ownership transferred to: %', v_owner_role;

    RAISE NOTICE '';
    RAISE NOTICE '→ Phase 2: Superuser Handoff (SINGLE SWITCH - NO RETURN)';

    -- ═══════════════════════════════════════════════════════════════════════
    -- HANDOFF: Switch to owner role PERMANENTLY
    -- ═══════════════════════════════════════════════════════════════════════
    -- After this SET ROLE command:
    --   - ALL subsequent operations execute as owner_role
    --   - NO RESET ROLE occurs
    --   - NO privilege escalation
    --   - Deployment completes entirely in owner context


    CREATE OR REPLACE FUNCTION pg_temp.set_dbowner_role() RETURNS VOID
    LANGUAGE plpgsql
    AS $body$
    BEGIN
        EXECUTE format('SET ROLE %I', current_setting('pgmi.database_owner_role'));
    END;
    $body$;

    PERFORM pg_temp.set_dbowner_role();

    RAISE DEBUG '  ✓ Switched to owner role: %', current_setting('role');
    RAISE DEBUG '  ⚠ All subsequent operations execute as owner (no privilege escalation)';

    -- ═══════════════════════════════════════════════════════════════════════
    -- OWNER PHASE: Standard Operations
    -- ═══════════════════════════════════════════════════════════════════════

    RAISE DEBUG '';
    RAISE DEBUG '→ Phase 3: Owner Role Operations';

    -- Create internal schema AS OWNER
    CREATE SCHEMA IF NOT EXISTS internal;
    COMMENT ON SCHEMA internal IS 'Infrastructure and deployment tracking';
    RAISE DEBUG '  ✓ Created internal schema (owner: %)', v_owner_role;

    CREATE OR REPLACE FUNCTION internal.is_relative_file_path(text) RETURNS BOOLEAN
    LANGUAGE sql
    IMMUTABLE
    AS $$SELECT $1 ~* '^\./' $$;


    CREATE TABLE IF NOT EXISTS internal.deployment_script(
        object_id uuid PRIMARY KEY,
        registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        registered_by TEXT NOT NULL DEFAULT CURRENT_USER
    );


    CREATE TABLE IF NOT EXISTS internal.deployment_script_content(
        "checksum" text NOT NULL PRIMARY KEY,
        "value" text NOT NULL,
        registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        registered_by TEXT NOT NULL DEFAULT CURRENT_USER,
        CONSTRAINT checksum_is_sha256_of_value CHECK("checksum" = encode(extensions.digest(convert_to("value", 'UTF8'), 'sha256'), 'hex'))
    );

    CREATE TABLE IF NOT EXISTS internal.deployment_script_execution_log(
        deployment_script_object_id uuid NOT NULL REFERENCES internal.deployment_script(object_id),
        deployment_script_content_checksum TEXT NOT NULL REFERENCES internal.deployment_script_content("checksum"),
        xact_id xid8 NOT NULL,
        id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
        file_path TEXT NOT NULL,
        idempotent BOOLEAN NOT NULL,
        sort_key TEXT,
        executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        executed_by TEXT NOT NULL DEFAULT CURRENT_USER,
        CONSTRAINT is_relative_file_path CHECK (internal.is_relative_file_path(file_path))
    );

    CREATE INDEX IF NOT EXISTS ix_deployment_script_execution_log_object_id
        ON internal.deployment_script_execution_log(deployment_script_object_id, idempotent)
        WHERE idempotent = false;

    CREATE OR REPLACE VIEW internal.vw_deployment_script AS
    SELECT
        c_deployment_script.object_id,
        FIRST_VALUE(c_deployment_script_execution_log) OVER w_desc AS last_log,
        FIRST_VALUE(c_deployment_script_content) OVER w_desc AS last_content,
        ARRAY_AGG(c_deployment_script_execution_log.file_path) OVER w_desc AS file_path_history,
        COUNT(*) OVER w_desc AS times_executed_count,
        (SELECT COUNT(DISTINCT log.deployment_script_content_checksum)
         FROM internal.deployment_script_execution_log log
         WHERE log.deployment_script_object_id = c_deployment_script.object_id) AS total_changed
    FROM internal.deployment_script AS c_deployment_script
    LEFT JOIN internal.deployment_script_execution_log AS c_deployment_script_execution_log
        ON c_deployment_script_execution_log.deployment_script_object_id = c_deployment_script.object_id
    LEFT JOIN internal.deployment_script_content AS c_deployment_script_content
        ON c_deployment_script_content.checksum = c_deployment_script_execution_log.deployment_script_content_checksum
    WINDOW w_desc AS (PARTITION BY c_deployment_script.object_id
                      ORDER BY c_deployment_script_execution_log.executed_at DESC
                      ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING);



    -- Restrict permissions: only database owner role can modify tracking table
    REVOKE ALL ON TABLE internal.deployment_script_execution_log FROM PUBLIC;
    GRANT SELECT ON TABLE internal.deployment_script_execution_log TO PUBLIC;
    EXECUTE format('GRANT INSERT, UPDATE, DELETE ON TABLE internal.deployment_script_execution_log TO %I', v_owner_role);

    -- Grant USAGE on sequence to owner role (allows nextval() in deploy_script function)
    EXECUTE format('GRANT USAGE ON SEQUENCE internal.deployment_script_execution_log_id_seq TO %I', v_owner_role);

    RAISE DEBUG '  ✓ Created deployment tracking tables (owner: %)', v_owner_role;

    CREATE TABLE IF NOT EXISTS internal.unittest_script (
        execution_order INT NOT NULL PRIMARY KEY,
        step_type TEXT NOT NULL CHECK (step_type IN ('setup', 'test', 'teardown')),
        script_path TEXT NOT NULL,
        script_directory TEXT NOT NULL,
        savepoint_id TEXT NOT NULL,
        content TEXT NOT NULL,
        deployed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        deployed_by TEXT NOT NULL DEFAULT CURRENT_USER
    );

    CREATE INDEX IF NOT EXISTS ix_unittest_script_step_type
        ON internal.unittest_script(step_type);

    COMMENT ON TABLE internal.unittest_script IS
    'Persisted test scripts from last deployment. Query this to inspect tests. Use internal.generate_test_script() to generate executable SQL.';

    REVOKE ALL ON TABLE internal.unittest_script FROM PUBLIC;
    GRANT SELECT ON TABLE internal.unittest_script TO PUBLIC;
    EXECUTE format('GRANT INSERT, UPDATE, DELETE ON TABLE internal.unittest_script TO %I', v_owner_role);

    RAISE DEBUG '  ✓ Created unittest tracking table (owner: %)', v_owner_role;
    RAISE DEBUG '  ✓ Tracking table permissions: owner-only writes, public reads';

    RAISE DEBUG '';
    RAISE DEBUG '═══════════════════════════════════════════════════════════════';
    RAISE DEBUG 'Bootstrap Complete: Single-Handoff Model Active';
    RAISE DEBUG '═══════════════════════════════════════════════════════════════';
    RAISE DEBUG '  Current role: %', current_setting('role');
    RAISE DEBUG '  Database owner: %', v_owner_role;
    RAISE DEBUG '  All remaining deployment executes as: %', v_owner_role;
    RAISE DEBUG '═══════════════════════════════════════════════════════════════';
    RAISE DEBUG '';
END $infrastructure$;





CREATE FUNCTION pg_temp.deploy() RETURNS VOID 
LANGUAGE plpgsql
AS
$deployment$
DECLARE 
    v_script_object_id uuid;
    v_script RECORD;
BEGIN
    FOR v_script IN (
        SELECT 
            path, 
            id, 
            generic_id,
            sort_key, 
            description, 
            idempotent, 
            execution_order,
            content,
            encode(extensions.digest(convert_to(content, 'UTF8'), 'sha256'), 'hex') AS "checksum"
        FROM pg_temp.pgmi_plan_view
        WHERE id IS NOT NULL  -- Deploy only scripts with explicit <pgmi:meta> blocks
        ORDER BY execution_order ASC
    )
    LOOP

        v_script_object_id := COALESCE(v_script.id, v_script.generic_id);
        INSERT INTO internal.deployment_script(object_id)
        VALUES(v_script_object_id)
        ON CONFLICT(object_id) DO NOTHING;

        INSERT INTO internal.deployment_script_content("checksum", "value")
        VALUES(v_script.checksum, v_script.content)
        ON CONFLICT("checksum") DO NOTHING;

        IF EXISTS (
            SELECT 1 FROM internal.vw_deployment_script 
            WHERE object_id = v_script_object_id 
            AND NOT v_script.idempotent) THEN
            CONTINUE;
        END IF;

        EXECUTE v_script.content;

        INSERT INTO internal.deployment_script_execution_log(
            deployment_script_object_id,
            deployment_script_content_checksum,
            xact_id,
            file_path,
            idempotent,
            sort_key)
        VALUES(
            v_script_object_id,
            v_script.checksum,
            pg_current_xact_id(),
            v_script.path,
            v_script.idempotent,
            v_script.sort_key);


    END LOOP;

END;
$deployment$;


-- ============================================================================
-- STEP 3: Persist Unit Test Metadata (Called Before COMMIT)
-- ============================================================================
-- Copies test execution plan from session-scoped pg_temp.pgmi_unittest_plan
-- to persistent internal.unittest_script table. This enables:
--   - Power users to inspect deployed tests via SQL
--   - Test execution independent of pgmi via internal.generate_test_script()
--   - Audit trail of what tests were deployed

CREATE OR REPLACE FUNCTION pg_temp.persist_unittest_metadata()
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    TRUNCATE internal.unittest_script;

    INSERT INTO internal.unittest_script (
        execution_order, step_type, script_path, script_directory, savepoint_id, content
    )
    SELECT
        p.execution_order,
        p.step_type,
        p.script_path,
        p.script_directory,
        p.savepoint_id,
        p.executable_sql
    FROM pg_temp.pgmi_unittest_plan p
    ORDER BY p.execution_order;
END;
$$;
