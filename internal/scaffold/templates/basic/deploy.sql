-- ============================================================================
-- deploy.sql - Deployment Orchestrator (Basic Template)
-- ============================================================================
-- This file controls HOW your SQL gets executed. pgmi loads your files into
-- session-scoped temporary tables, then hands control to this script.
--
-- Available:
--   pg_temp.pgmi_source_view     - Your SQL files (path, content, directory, etc.)
--   pg_temp.pgmi_parameter_view  - CLI params (--param key=value)
--   current_setting('pgmi.key', true) - Get parameter value (NULL if not set)
--   CALL pgmi_test()             - Run tests with savepoint isolation
-- ============================================================================


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
    -- Parameters are passed via: pgmi deploy . --param admin_email=you@example.com
    v_admin_email := COALESCE(current_setting('pgmi.admin_email', true), 'admin@example.com');
    EXECUTE format('SELECT * FROM upsert_user(%L, %L)', v_admin_email, 'Administrator')
        INTO v_user;
    RAISE NOTICE 'Admin user ready: % (id=%)', v_user.email, v_user.id;
END $$;

-- Run tests (savepoint ensures test side effects roll back)
SAVEPOINT _tests;
CALL pgmi_test();
ROLLBACK TO SAVEPOINT _tests;

COMMIT;

DO $$ 
BEGIN 
    RAISE NOTICE $ascii$
  ___   ___  _  _ ___ 
 |   \ / _ \| \| | __|
 | |) | (_) | .` | _| 
 |___/ \___/|_|\_|___|                          
    $ascii$; 
END $$;