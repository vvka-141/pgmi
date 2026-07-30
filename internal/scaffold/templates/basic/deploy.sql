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

    -- ── Execution model ──────────────────────────────────────────────────
    -- By DEFAULT every migration re-runs on every deploy. That is safe only
    -- because these files are idempotent (CREATE TABLE IF NOT EXISTS, CREATE
    -- OR REPLACE FUNCTION). It is the simplest thing that works, and it means
    -- there is no ledger to get out of sync with reality.
    --
    -- If you would rather have apply-once semantics — a file runs once, ever,
    -- like Flyway or Sqitch — uncomment the three lines marked (A), (B), (C).
    -- That is the whole feature. It is 3 lines of your SQL, not a framework:
    -- you own it, you can read it, and you can delete it.
    --
    -- Trade-off: with tracking on, editing an already-applied migration does
    -- nothing (it is skipped). The stored checksum tells you it changed —
    -- decide there whether you want to warn, fail, or ignore.

    -- (A) CREATE TABLE IF NOT EXISTS _migration (
    -- (A)     path       text PRIMARY KEY,
    -- (A)     checksum   text NOT NULL,
    -- (A)     applied_at timestamptz NOT NULL DEFAULT now()
    -- (A) );

    FOR v_file IN (
        SELECT path, content, pgmi_checksum
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        -- (B) AND NOT EXISTS (SELECT 1 FROM _migration m WHERE m.path = pgmi_source_view.path)
        ORDER BY path
    )
    LOOP
        RAISE DEBUG 'Executing: %', v_file.path;
        BEGIN
            EXECUTE v_file.content;
            -- (C) INSERT INTO _migration (path, checksum) VALUES (v_file.path, v_file.pgmi_checksum);
        EXCEPTION WHEN OTHERS THEN
            -- Keep the original SQLSTATE and DETAIL: a bare RAISE EXCEPTION rewrites
            -- them to P0001 and an empty detail, so a caller cannot classify the
            -- failure (e.g. a retryable 40001 vs a permanent constraint violation).
            DECLARE
                v_sqlstate text;
                v_detail   text;
            BEGIN
                GET STACKED DIAGNOSTICS
                    v_sqlstate = RETURNED_SQLSTATE,
                    v_detail   = PG_EXCEPTION_DETAIL;
                RAISE EXCEPTION 'Failed in %: %', v_file.path, SQLERRM
                    USING ERRCODE = v_sqlstate, DETAIL = v_detail;
            END;
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
