-- deploy.sql - Deployment Orchestrator (Basic Template)
--
-- pgmi loads your project files into session-scoped temp tables, then runs
-- this script. Available:
--   pg_temp.pgmi_source_view     - ALL project files (SQL, JSON, CSV, etc.)
--   pg_temp.pgmi_parameter_view  - CLI params (--param key=value)
--   current_setting('pgmi.key', true) - Parameter value (NULL if not set)
--   CALL pgmi_test()             - Run tests with savepoint isolation

BEGIN;

DO $$
DECLARE
    v_file    RECORD;
    v_env     TEXT;
    v_config  JSONB;
    v_user    RECORD;
BEGIN
    v_env := COALESCE(current_setting('pgmi.env', true), 'development');

    -- Load project metadata from a non-SQL file (pgmi loads ALL files, not just SQL)
    SELECT content::jsonb INTO STRICT v_config
    FROM pg_temp.pgmi_source_view
    WHERE path = './project.json';

    RAISE NOTICE '[%] Deploying % v% (% file(s) in project)',
        v_env,
        v_config ->> 'app_name',
        v_config ->> 'version',
        (SELECT count(*) FROM pg_temp.pgmi_source_view);

    -- Execute migration files in path order
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    )
    LOOP
        RAISE DEBUG 'Executing: %', v_file.path;
        BEGIN
            EXECUTE v_file.content;
        EXCEPTION WHEN OTHERS THEN
            RAISE EXCEPTION 'Failed in %: %', v_file.path, SQLERRM;
        END;
    END LOOP;

    -- Environment-aware seeding: only in non-production
    IF v_env IS DISTINCT FROM 'production' THEN
        -- Static call works even though the migration loop above just created
        -- upsert_user: PL/pgSQL resolves each statement at first execution.
        SELECT * INTO v_user FROM upsert_user(
            COALESCE(current_setting('pgmi.admin_email', true), 'admin@example.com'),
            'Administrator'
        );
        RAISE NOTICE 'Dev seed: admin user ready (% id=%)', v_user.email, v_user.id;
    END IF;
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
