-- ============================================================================
-- deploy.sql - Deployment Orchestrator (Advanced Template)
-- ============================================================================
-- This file controls HOW your SQL gets executed. pgmi loads your files into
-- session-scoped temporary tables, then hands control to this script.
--
-- Available:
--   pg_temp.pgmi_plan_view         - Your SQL files in execution order
--   pg_temp.pgmi_source_view       - Source files
--   pg_temp.pgmi_parameter_view    - CLI params (--param key=value)
--   pg_temp.pgmi_test_source_view  - Test files from __test__/ directories
--   CALL pgmi_test()               - Run tests with savepoint isolation
--   CALL pgmi_test(pattern, callback) - Run filtered tests with callback
--
-- Parameters are declared in session.xml and accessed via:
--   current_setting('deployment.<key>') - Direct PostgreSQL session variable
--   pg_temp.deployment_setting(key)     - Helper with error handling
-- ============================================================================


DROP TABLE IF EXISTS pg_temp.deployment_parameter;
CREATE TEMPORARY TABLE deployment_parameter (
    key TEXT PRIMARY KEY,
    original_key TEXT NOT NULL,
    value TEXT,
    description TEXT,
    is_redundant BOOLEAN NOT NULL DEFAULT FALSE
);

DO $$
DECLARE
    v_xml XML;
BEGIN
    SELECT content::xml INTO v_xml
    FROM pg_temp.pgmi_source_view
    WHERE path = './session.xml';

    IF v_xml IS NULL THEN
        RAISE EXCEPTION 'session.xml not found in project. Create it to declare deployment parameters.';
    END IF;

    INSERT INTO pg_temp.deployment_parameter (key, original_key, value, description, is_redundant)
    SELECT
        COALESCE(xml.normalized_key, cli.normalized_key),
        COALESCE(xml.original_key, cli.key),
        cli.value,
        xml.description,
        xml.original_key IS NULL
    FROM (
        SELECT
            trim(param_key) AS original_key,
            regexp_replace(lower(trim(param_key)), '[^a-z0-9]+', '_', 'g') AS normalized_key,
            NULLIF(trim(COALESCE(param_desc, '')), '') AS description
        FROM XMLTABLE('/session/parameter' PASSING v_xml
            COLUMNS param_key TEXT PATH '@key', param_desc TEXT PATH '@description')
        WHERE param_key IS NOT NULL AND trim(param_key) != ''
    ) xml
    FULL OUTER JOIN (
        SELECT key, value, regexp_replace(lower(key), '[^a-z0-9]+', '_', 'g') AS normalized_key
        FROM pg_temp.pgmi_parameter_view
    ) cli ON xml.normalized_key = cli.normalized_key;
END $$;



UPDATE pg_temp.deployment_parameter
SET value = CASE key
    WHEN 'env' THEN 'dev'
    WHEN 'database_owner_role' THEN current_database()::text || '_owner'
    WHEN 'database_admin_role' THEN current_database()::text || '_admin'
    WHEN 'database_api_role' THEN current_database()::text || '_api'
    WHEN 'database_customer_role' THEN current_database()::text || '_customer'
    ELSE NULL
END
WHERE value IS NULL;

UPDATE pg_temp.deployment_parameter
SET value = 'postgres'
WHERE value IS NULL
  AND key ~ 'password$'
  AND EXISTS (SELECT 1 FROM pg_temp.deployment_parameter WHERE key = 'env' AND value = 'dev');


DO $$
DECLARE
    v_param pg_temp.deployment_parameter;
    v_missing TEXT;
    v_redundant TEXT;
BEGIN
    SELECT
        string_agg(original_key, ', ') FILTER (WHERE value IS NULL),
        string_agg(original_key, ', ') FILTER (WHERE is_redundant)
    INTO v_missing, v_redundant
    FROM pg_temp.deployment_parameter;

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'Missing required parameters: %. Provide via: pgmi deploy --param <key>=<value>', v_missing;
    END IF;

    IF v_redundant IS NOT NULL THEN
        RAISE EXCEPTION 'Unknown parameters: %. These are not declared in session.xml', v_redundant;
    END IF;

    FOR v_param IN SELECT * FROM pg_temp.deployment_parameter ORDER BY key LOOP
        PERFORM set_config('deployment.' || v_param.key, v_param.value, false);
        PERFORM set_config('pgmi.' || v_param.key, v_param.value, false);
        RAISE NOTICE '[pgmi] Parameter: % = %',
            v_param.original_key,
            CASE WHEN v_param.key ~ 'password' THEN '********' ELSE v_param.value END;
    END LOOP;
END $$;


CREATE FUNCTION pg_temp.deployment_setting(p_key text, p_required boolean DEFAULT true)
RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    v_normalized TEXT := 'deployment.' || regexp_replace(lower(p_key), '[^a-z0-9]+', '_', 'g');
    v_value TEXT;
BEGIN
    v_value := current_setting(v_normalized, true);
    IF v_value IS NULL AND p_required THEN
        RAISE EXCEPTION 'Required deployment parameter "%" not set. Check session.xml and --param flags.', p_key;
    END IF;
    RETURN v_value;
END;
$$;



-- ============================================================================
-- SUPERUSER PHASE: Roles, Extensions, Ownership
-- ============================================================================
DO $superuser$
DECLARE
    v_owner_role TEXT := pg_temp.deployment_setting('database_owner_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
    v_admin_password TEXT := pg_temp.deployment_setting('database_admin_password');
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
    v_customer_password TEXT := pg_temp.deployment_setting('database_customer_password');
BEGIN
    -- Owner role (NOLOGIN) - owns all database objects
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_owner_role) THEN
        EXECUTE format('CREATE ROLE %I NOLOGIN', v_owner_role);
        RAISE NOTICE '[pgmi] Created owner role: %', v_owner_role;
    END IF;

    -- API role (NOLOGIN group) - permission bundle for API access
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_api_role) THEN
        EXECUTE format('CREATE ROLE %I NOLOGIN', v_api_role);
        RAISE NOTICE '[pgmi] Created API role: %', v_api_role;
    END IF;

    -- Admin role (LOGIN) - inherits owner + api
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_admin_role) THEN
        EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L CONNECTION LIMIT 10', v_admin_role, v_admin_password);
        RAISE NOTICE '[pgmi] Created admin role: %', v_admin_role;
    ELSE
        EXECUTE format('ALTER ROLE %I WITH PASSWORD %L CONNECTION LIMIT 10', v_admin_role, v_admin_password);
    END IF;

    -- Customer role (LOGIN) - inherits api, RLS-restricted
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_customer_role) THEN
        EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L CONNECTION LIMIT 100', v_customer_role, v_customer_password);
        RAISE NOTICE '[pgmi] Created customer role: %', v_customer_role;
    ELSE
        EXECUTE format('ALTER ROLE %I WITH PASSWORD %L CONNECTION LIMIT 100', v_customer_role, v_customer_password);
    END IF;

    -- Role hierarchy
    EXECUTE format('GRANT %I TO %I', v_owner_role, v_admin_role);
    EXECUTE format('GRANT %I TO %I', v_api_role, v_admin_role);
    EXECUTE format('GRANT %I TO %I', v_api_role, v_customer_role);

    -- Grant owner to current_user for SET ROLE
    EXECUTE format('GRANT %I TO CURRENT_USER', v_owner_role);

    -- Configure search_path for each role
    EXECUTE format('ALTER ROLE %I SET search_path = core, api, internal, extensions, utils, pg_temp', v_owner_role);
    EXECUTE format('ALTER ROLE %I SET search_path = core, api, internal, extensions, utils, pg_temp', v_admin_role);
    EXECUTE format('ALTER ROLE %I SET search_path = api, core, extensions, utils, pg_temp', v_api_role);
    EXECUTE format('ALTER ROLE %I SET search_path = api, membership, core, extensions, utils, pg_temp', v_customer_role);

    -- Transfer database ownership
    EXECUTE format('ALTER DATABASE %I OWNER TO %I', current_database(), v_owner_role);
    RAISE NOTICE '[pgmi] Database % owned by %', current_database(), v_owner_role;
END $superuser$;


-- Extensions (still superuser context)
CREATE SCHEMA IF NOT EXISTS extensions;
COMMENT ON SCHEMA extensions IS 'PostgreSQL extensions isolated from application schemas. Keeps extension objects out of search_path conflicts.';
GRANT USAGE ON SCHEMA extensions TO PUBLIC;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp" SCHEMA extensions;
CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA extensions;
CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA extensions;
CREATE EXTENSION IF NOT EXISTS hstore SCHEMA extensions;
CREATE EXTENSION IF NOT EXISTS plv8;


-- ============================================================================
-- OWNER PHASE: SET ROLE and Create Schemas
-- ============================================================================
DO $owner_phase$
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_owner_role'));
    RAISE NOTICE '[pgmi] Switched to owner role: %', pg_temp.deployment_setting('database_owner_role');
END $owner_phase$;


-- Schemas
DO $schemas$
DECLARE
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    CREATE SCHEMA IF NOT EXISTS internal;
    CREATE SCHEMA IF NOT EXISTS core;
    CREATE SCHEMA IF NOT EXISTS api;
    CREATE SCHEMA IF NOT EXISTS utils;
    CREATE SCHEMA IF NOT EXISTS membership;

    COMMENT ON SCHEMA internal IS 'Deployment infrastructure and system tables. Not for application use.';
    COMMENT ON SCHEMA core IS 'Core domain entities and business logic. The heart of your application.';
    COMMENT ON SCHEMA api IS 'Public API layer. Functions and views exposed to applications and external clients.';
    COMMENT ON SCHEMA utils IS 'Shared utility functions available to all roles.';
    COMMENT ON SCHEMA membership IS 'User identity, organizations, invitations, and access control.';

    REVOKE ALL ON SCHEMA public FROM PUBLIC;
    REVOKE ALL ON SCHEMA utils, api, core, internal, membership FROM PUBLIC;

    EXECUTE format('GRANT USAGE ON SCHEMA utils TO %I, %I, %I', v_admin_role, v_api_role, v_customer_role);
    EXECUTE format('GRANT USAGE ON SCHEMA api TO %I, %I', v_api_role, v_customer_role);
    EXECUTE format('GRANT USAGE ON SCHEMA core TO %I', v_api_role);
    EXECUTE format('GRANT USAGE ON SCHEMA membership TO %I, %I, %I', v_admin_role, v_api_role, v_customer_role);

    EXECUTE format(
        'ALTER DATABASE %I SET search_path = core, api, membership, internal, extensions, utils, pg_temp',
        current_database()
    );
    SET search_path TO core, api, membership, internal, extensions, utils, pg_temp;

    RAISE NOTICE '[pgmi] Schemas configured';
END $schemas$;


-- ============================================================================
-- TRACKING TABLES
-- ============================================================================
-- These tables track which scripts have been executed. They must exist
-- before the deploy loop runs so it can log executions.

CREATE TABLE IF NOT EXISTS internal.deployment_script(
    object_id uuid PRIMARY KEY,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    registered_by TEXT NOT NULL DEFAULT CURRENT_USER
);

COMMENT ON TABLE internal.deployment_script IS 'Registry of unique script identities for idempotency tracking. Each script gets a stable UUID that persists across deployments.';
COMMENT ON COLUMN internal.deployment_script.object_id IS 'Unique script identifier. From <pgmi-meta id="..."> or auto-generated from file path. Stable across content changes.';
COMMENT ON COLUMN internal.deployment_script.registered_at IS 'Timestamp when this script was first seen by the deployment system.';
COMMENT ON COLUMN internal.deployment_script.registered_by IS 'Database role that first deployed this script.';

CREATE TABLE IF NOT EXISTS internal.deployment_script_content(
    checksum text PRIMARY KEY,
    value text NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    registered_by TEXT NOT NULL DEFAULT CURRENT_USER,
    CONSTRAINT checksum_is_sha256 CHECK(checksum = encode(extensions.digest(convert_to(value, 'UTF8'), 'sha256'), 'hex'))
);

COMMENT ON TABLE internal.deployment_script_content IS 'Content-addressable storage for script versions. Deduplicates identical content across scripts and deployments.';
COMMENT ON COLUMN internal.deployment_script_content.checksum IS 'SHA-256 hash of script content. Primary key enables content deduplication.';
COMMENT ON COLUMN internal.deployment_script_content.value IS 'Full script content. Immutable once stored (enforced by checksum constraint).';
COMMENT ON COLUMN internal.deployment_script_content.registered_at IS 'Timestamp when this content version was first deployed.';
COMMENT ON COLUMN internal.deployment_script_content.registered_by IS 'Database role that first deployed this content version.';
COMMENT ON CONSTRAINT checksum_is_sha256 ON internal.deployment_script_content IS 'Ensures checksum matches actual content. Prevents tampering and guarantees content integrity.';

CREATE TABLE IF NOT EXISTS internal.deployment_script_execution_log(
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deployment_script_object_id uuid NOT NULL REFERENCES internal.deployment_script(object_id),
    deployment_script_content_checksum TEXT NOT NULL REFERENCES internal.deployment_script_content(checksum),
    xact_id xid8 NOT NULL,
    file_path TEXT NOT NULL,
    idempotent BOOLEAN NOT NULL,
    sort_key TEXT,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    executed_by TEXT NOT NULL DEFAULT CURRENT_USER
);

COMMENT ON TABLE internal.deployment_script_execution_log IS 'Audit log of every script execution. Used for idempotency checks, deployment history, and debugging.';
COMMENT ON COLUMN internal.deployment_script_execution_log.id IS 'Auto-incrementing execution ID. Provides total ordering of all executions.';
COMMENT ON COLUMN internal.deployment_script_execution_log.deployment_script_object_id IS 'References the script identity. Links execution to its logical script.';
COMMENT ON COLUMN internal.deployment_script_execution_log.deployment_script_content_checksum IS 'References the specific content version executed. Enables content diffing across deployments.';
COMMENT ON COLUMN internal.deployment_script_execution_log.xact_id IS 'PostgreSQL transaction ID (xid8). Correlates with pg_stat_activity and WAL for debugging.';
COMMENT ON COLUMN internal.deployment_script_execution_log.file_path IS 'Original file path at execution time. May change if files are renamed.';
COMMENT ON COLUMN internal.deployment_script_execution_log.idempotent IS 'Whether the script is re-runnable. Non-idempotent scripts execute only once per object_id.';
COMMENT ON COLUMN internal.deployment_script_execution_log.sort_key IS 'Execution ordering key from <pgmi-meta sortKeys="...">. NULL for path-ordered scripts.';
COMMENT ON COLUMN internal.deployment_script_execution_log.executed_at IS 'Timestamp when execution completed successfully.';
COMMENT ON COLUMN internal.deployment_script_execution_log.executed_by IS 'Database role that executed the script (usually the owner role).';

CREATE INDEX IF NOT EXISTS ix_deployment_script_execution_log_object_id
    ON internal.deployment_script_execution_log(deployment_script_object_id, idempotent)
    WHERE idempotent = false;

COMMENT ON INDEX internal.ix_deployment_script_execution_log_object_id IS 'Partial index for fast idempotency checks. Only indexes non-idempotent scripts since idempotent scripts always re-run.';

-- Tracking view (for querying deployment history)
CREATE OR REPLACE VIEW internal.vw_deployment_script AS
SELECT
    s.object_id,
    (SELECT l FROM internal.deployment_script_execution_log l
     WHERE l.deployment_script_object_id = s.object_id
     ORDER BY l.executed_at DESC LIMIT 1) AS last_execution,
    (SELECT COUNT(*) FROM internal.deployment_script_execution_log l
     WHERE l.deployment_script_object_id = s.object_id) AS execution_count
FROM internal.deployment_script s;

COMMENT ON VIEW internal.vw_deployment_script IS 'Deployment status overview. Shows each script''s last execution details and total run count.';
COMMENT ON COLUMN internal.vw_deployment_script.object_id IS 'Script identity UUID.';
COMMENT ON COLUMN internal.vw_deployment_script.last_execution IS 'Most recent execution record (composite type). Access fields via (last_execution).executed_at, etc.';
COMMENT ON COLUMN internal.vw_deployment_script.execution_count IS 'Total number of times this script has been executed across all deployments.';

-- Tracking table permissions
DO $permissions$
DECLARE
    v_owner_role TEXT := pg_temp.deployment_setting('database_owner_role');
BEGIN
    REVOKE ALL ON TABLE internal.deployment_script_execution_log FROM PUBLIC;
    GRANT SELECT ON TABLE internal.deployment_script_execution_log TO PUBLIC;
    EXECUTE format('GRANT INSERT, UPDATE, DELETE ON TABLE internal.deployment_script_execution_log TO %I', v_owner_role);
    EXECUTE format('GRANT USAGE ON SEQUENCE internal.deployment_script_execution_log_id_seq TO %I', v_owner_role);
END $permissions$;


-- ****************************************************************************
--
--                    PHASE 2: DEPLOYMENT
--
--     Runs tracked files from lib/ and your application code.
--     Everything below is executed within a transaction with advisory lock.
--
-- ****************************************************************************


-- ============================================================================
-- DEPLOY FUNCTION
-- ============================================================================
CREATE FUNCTION pg_temp.deploy() RETURNS VOID
LANGUAGE plpgsql AS $fn$
DECLARE
    v_script RECORD;
    v_object_id uuid;
    v_executed int := 0;
    v_skipped int := 0;
BEGIN
    FOR v_script IN (
        SELECT
            p.path, p.id, p.generic_id, p.sort_key, p.idempotent, p.execution_order, p.content,
            encode(extensions.digest(convert_to(p.content, 'UTF8'), 'sha256'), 'hex') AS checksum
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND s.path != './session.xml'
        ORDER BY p.execution_order
    )
    LOOP
        v_object_id := COALESCE(v_script.id, v_script.generic_id);

        IF NOT v_script.idempotent AND EXISTS (
            SELECT 1 FROM internal.deployment_script_execution_log
            WHERE deployment_script_object_id = v_object_id
        ) THEN
            v_skipped := v_skipped + 1;
            CONTINUE;
        END IF;

        INSERT INTO internal.deployment_script(object_id)
        VALUES (v_object_id) ON CONFLICT DO NOTHING;

        INSERT INTO internal.deployment_script_content(checksum, value)
        VALUES (v_script.checksum, v_script.content) ON CONFLICT DO NOTHING;

        RAISE NOTICE '[pgmi] Executing: %', v_script.path;
        BEGIN
            EXECUTE v_script.content;
        EXCEPTION WHEN OTHERS THEN
            RAISE EXCEPTION '[pgmi] Script failed: % - %', v_script.path, SQLERRM;
        END;

        INSERT INTO internal.deployment_script_execution_log(
            deployment_script_object_id, deployment_script_content_checksum,
            xact_id, file_path, idempotent, sort_key
        ) VALUES (
            v_object_id, v_script.checksum,
            pg_current_xact_id(), v_script.path, v_script.idempotent, v_script.sort_key
        );

        v_executed := v_executed + 1;
    END LOOP;

    RAISE NOTICE '[pgmi] Deployment complete: % executed, % skipped', v_executed, v_skipped;
END $fn$;


-- ============================================================================
-- TEST CALLBACK
-- ============================================================================
CREATE OR REPLACE FUNCTION pg_temp.test_observer(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    CASE e.event
        WHEN 'suite_start' THEN RAISE NOTICE '[pgmi] Test suite started';
        WHEN 'suite_end' THEN RAISE NOTICE '[pgmi] Test suite completed (% steps)', e.ordinal;
        WHEN 'fixture_start' THEN RAISE NOTICE '[pgmi] Fixture: %', e.path;
        WHEN 'test_start' THEN RAISE NOTICE '[pgmi] Test: %', e.path;
        WHEN 'test_end' THEN NULL;
        WHEN 'teardown_start' THEN RAISE DEBUG '[pgmi] Teardown: %', e.directory;
        ELSE NULL;
    END CASE;
END $$;


-- ============================================================================
-- EXECUTE DEPLOYMENT
-- ============================================================================
DO $$ BEGIN RAISE NOTICE '[pgmi] Acquiring deployment lock...'; END $$;
SELECT pg_advisory_lock(hashtext('pgmi_deploy_' || current_database()));

BEGIN;
    SELECT pg_temp.deploy();
    SAVEPOINT _tests;
    CALL pgmi_test(NULL, 'pg_temp.test_observer');
    ROLLBACK TO SAVEPOINT _tests;
COMMIT;

SELECT pg_advisory_unlock(hashtext('pgmi_deploy_' || current_database()));
DO $$ 
BEGIN 
    RAISE NOTICE '[pgmi] Deployment lock released'; 
    RAISE NOTICE $ascii$
  ___   ___  _  _ ___ 
 |   \ / _ \| \| | __|
 | |) | (_) | .` | _| 
 |___/ \___/|_|\_|___|                          
    $ascii$; 
END;
$$;
