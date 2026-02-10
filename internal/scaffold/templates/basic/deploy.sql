-- ============================================================================
-- deploy.sql - Deployment Orchestrator (Basic Template)
-- ============================================================================
-- This file controls HOW your SQL gets executed. pgmi loads your files into
-- session-scoped temporary tables, then hands control to this script.
--
-- Available:
--   pg_temp.pgmi_source_view  - Your SQL files (path, content, directory, etc.)
--   pg_temp.pgmi_parameter    - CLI params (--param key=value)
--   pgmi_get_param(key, default)     - Get parameter value
--   pgmi_declare_param(key, ...)     - Declare expected parameters
--   CALL pgmi_test()                 - Run tests with savepoint isolation
-- ============================================================================


-- ============================================================================
-- PARAMETERS
-- ============================================================================
-- Declare expected parameters with types and defaults.
-- Users pass values via: pgmi deploy . --param admin_email=you@example.com

SELECT pg_temp.pgmi_declare_param(
    p_key => 'admin_email',
    p_type => 'text',
    p_default_value => 'admin@example.com',
    p_description => 'Email for the admin user (seeded after migrations)'
);


-- ============================================================================
-- DEPLOYMENT
-- ============================================================================
BEGIN;

-- Run migrations in path order (001_, 002_, etc.)
DO $$
DECLARE
    v_file RECORD;
    v_admin_email TEXT;
    v_user RECORD;
BEGIN
    -- Execute migration files in alphabetical order
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    )
    LOOP
        RAISE DEBUG 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;

    -- Seed admin user using the parameter
    v_admin_email := pg_temp.pgmi_get_param('admin_email', 'admin@example.com');
    EXECUTE format('SELECT * FROM upsert_user(%L, %L)', v_admin_email, 'Administrator')
        INTO v_user;
    RAISE NOTICE 'Admin user ready: % (id=%)', v_user.email, v_user.id;
END $$;

-- Run tests (savepoint ensures test side effects roll back)
SAVEPOINT _tests;
CALL pgmi_test();
ROLLBACK TO SAVEPOINT _tests;

COMMIT;
