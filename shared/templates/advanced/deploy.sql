-- ============================================================================
-- deploy.sql - pgmi runs this file first. Nothing else runs unless this file
-- runs it. Read it top to bottom: that is the order of the deployment.
-- ============================================================================
-- Before this file starts, pgmi has taken the deploy lock (a second deploy
-- exits 15) and loaded every project file and parameter into session views:
-- pg_temp.pgmi_source_view, pgmi_plan_view, pgmi_parameter_view and
-- pgmi_test_source_view (`pgmi ai contract` lists their columns).
--
-- The setup code lives in deploy/, one file per concern, each run by name
-- below. Add code to the file whose name fits; a new deploy/ file is a design
-- change. deploy/ files never COMMIT or build an index CONCURRENTLY: they run
-- inside the deploy's transaction.
--   deploy/preflight.sql      server version, a database this template can deploy onto
--   deploy/settings.sql       the lists a project edits: extensions, settings, grants, guards
--   deploy/parameters.sql     session.xml parameters, defaults, checks, environment label
--   deploy/project_rules.sql  file rules: dev/ folders, sort keys, run-once vs re-run
--   deploy/bootstrap.sql      host grants, roles, database settings, extensions
--   deploy/schemas.sql        application schemas and the deploy's own tables
--   deploy/file_api.sql       helpers project files and tests call
--   deploy/ledger.sql         the run ledger behind the loop
--   deploy/finalize.sql       privileges and access after the last file; test reporter
-- ============================================================================

-- Everything from here to COMMIT is one transaction: any error rolls back all
-- of it, roles and extensions included.
BEGIN;
-- A table lock the deploy cannot get within 5s fails it (55P03) instead of
-- queueing every application query behind it for the rest of the deploy.
SET LOCAL lock_timeout = '5s';

-- Runs one deploy/ file. No exception handler: an error reaches pgmi as
-- PostgreSQL raised it, and pgmi names the file by its content.
CREATE FUNCTION pg_temp.run_file(p_path text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_sql text;
BEGIN
    SELECT content INTO v_sql FROM pg_temp.pgmi_source_view WHERE path = p_path;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'deploy.sql runs %, which is not in the project', p_path;
    END IF;
    EXECUTE v_sql;
END $$;


-- ############################################################################
-- PREFLIGHT - stop before any change when the server, the parameters or the
-- project's files are wrong
-- ############################################################################
SELECT pg_temp.run_file('./deploy/preflight.sql');
SELECT pg_temp.run_file('./deploy/settings.sql');
SELECT pg_temp.run_file('./deploy/parameters.sql');
SELECT pg_temp.run_file('./deploy/project_rules.sql');


-- ############################################################################
-- BOOTSTRAP - roles, extensions and schemas the project files build on; from
-- the owner switch on, everything is created and owned by the owner role
-- ############################################################################
SELECT pg_temp.run_file('./deploy/bootstrap.sql');

DO $$
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_owner_role'));
    RAISE NOTICE '[pgmi] Switched to owner role: %', pg_temp.deployment_setting('database_owner_role');
END $$;

SELECT pg_temp.run_file('./deploy/schemas.sql');
SELECT pg_temp.run_file('./deploy/file_api.sql');
SELECT pg_temp.run_file('./deploy/ledger.sql');


-- ############################################################################
-- DEPLOY - every project file in plan order; a run-once file that already ran
-- here is skipped, everything else runs and is logged
-- ############################################################################
-- Before the first file: the access baseline finalize.sql compares against,
-- and every kernel routine back to the owner so CREATE OR REPLACE works.
INSERT INTO pg_temp.access_baseline SELECT * FROM pg_temp.access_entry();
SELECT pg_temp.kernel_reclaim();
INSERT INTO internal.deployment (xact_id, environment)
VALUES (pg_current_xact_id(), current_setting('deployment.env'));

DO $deploy$
DECLARE
    f          pg_temp.deploy_plan;
    v_policies jsonb;
    v_executed int := 0;
    v_skipped  int := 0;
BEGIN
    PERFORM pg_temp.refuse_run_once_drift();

    FOR f IN SELECT * FROM pg_temp.deploy_plan ORDER BY execution_order LOOP
        IF pg_temp.ran_once(f) THEN
            v_skipped := v_skipped + 1;
            CONTINUE;
        END IF;

        RAISE NOTICE '[pgmi] Executing: %', f.path;
        v_policies := pg_temp.before_file(f);
        -- No exception handler: the error reaches pgmi untouched, which names
        -- the file and line and keeps the SQLSTATE the retry contract reads.
        EXECUTE f.content;
        PERFORM pg_temp.after_file(f, v_policies);
        v_executed := v_executed + 1;
    END LOOP;

    RAISE NOTICE '[pgmi] Deployment complete: % executed, % skipped', v_executed, v_skipped;
END $deploy$;


-- ############################################################################
-- VERIFY - converge privileges, refuse lost access, run every test. A failure
-- here rolls back everything above.
-- ############################################################################
SELECT pg_temp.run_file('./deploy/finalize.sql');

SAVEPOINT _tests;
CALL pgmi_test(NULL, 'pg_temp.test_observer');
ROLLBACK TO SAVEPOINT _tests;
COMMIT;


-- ############################################################################
-- TAIL - each statement below commits on its own, as in psql
-- ############################################################################
-- CREATE INDEX CONCURRENTLY cannot run in a transaction block or a function,
-- so a project's concurrent index builds go here, one top-level statement
-- each, written CREATE INDEX CONCURRENTLY IF NOT EXISTS so a re-run skips a
-- built index. A build that fails leaves an INVALID index that IF NOT EXISTS
-- would then skip forever; the check below fails the deploy instead.

DO $$
DECLARE
    v_invalid text;
BEGIN
    SELECT string_agg(format('%I.%I', s.name, c.relname), ', ' ORDER BY s.name, c.relname)
    INTO v_invalid
    FROM pg_index i
    JOIN pg_class c ON c.oid = i.indexrelid
    JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
    WHERE NOT i.indisvalid
      AND NOT EXISTS (SELECT FROM pg_stat_progress_create_index b WHERE b.index_relid = i.indexrelid);

    IF v_invalid IS NOT NULL THEN
        RAISE EXCEPTION 'Invalid indexes in the database: %', v_invalid
            USING HINT = 'A concurrent index build failed or was cancelled. Fix the cause (duplicate rows for a unique index, a timeout), then DROP INDEX CONCURRENTLY each one and deploy again to rebuild it.';
    END IF;
END $$;

DO $$
BEGIN
    RAISE NOTICE $ascii$
  ___   ___  _  _ ___ 
 |   \ / _ \| \| | __|
 | |) | (_) | .` | _| 
 |___/ \___/|_|\_|___|                          
    $ascii$; 
END;
$$;
