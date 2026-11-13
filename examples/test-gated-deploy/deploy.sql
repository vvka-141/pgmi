-- deploy.sql - pgmi runs this file first. Nothing else runs unless this file
-- runs it. Read it top to bottom: that is the order of the deployment.
--
-- Before it starts, pgmi has taken the deploy lock (a second deploy exits 15)
-- and loaded every project file and parameter into session views
-- (`pgmi ai contract` lists them). This file runs two folders, in path order:
--
--   physical/   tables, keys, indexes: run once each (<pgmi-meta idempotent="false">)
--   semantic/   views and functions: re-run on every deploy
--
-- The tests in __test__/ folders run through CALL pgmi_test() before the
-- COMMIT. Any other SQL file is refused, so none is loaded and silently skipped.

-- Everything from here to COMMIT is one transaction: any error, a failing test
-- included, rolls back all of it.
BEGIN;
-- Every deploy re-creates the views in semantic/, which locks each one. A lock
-- the deploy cannot get within 5s fails it (55P03) instead of queueing every
-- application query behind it for the rest of the deploy.
SET LOCAL lock_timeout = '5s';

-- env names the kind of database: dev seeds a demo project, test and prod do
-- not. There is no default, so a CI deploy cannot fall back to dev.
DO $$
DECLARE
    v_env text := current_setting('pgmi.env', true);
BEGIN
    IF v_env IS NULL THEN
        RAISE EXCEPTION USING
            ERRCODE = 'invalid_parameter_value',
            MESSAGE = 'Missing required parameter: env (dev, test or prod)',
            HINT = 'For a local try-out: pgmi deploy . -d mydb --param env=dev. In CI: set PGMI_PARAM_ENV.';
    END IF;
    IF v_env NOT IN ('dev', 'test', 'prod') THEN
        RAISE EXCEPTION 'Parameter env must be dev, test or prod, got %', quote_literal(v_env)
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
END $$;

-- PostgreSQL lets PUBLIC execute every new function. Close that default for
-- everything this role creates from now on; grant each role what it needs.
ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON ROUTINES FROM PUBLIC;

-- deployment.run_once is the ledger of run-once files; the first deploy
-- creates it. A run-once file is skipped once it has run, so an edit would
-- reach only fresh databases. Refuse the deploy instead, before anything
-- runs, and name the file.
-- pgmi_checksum ignores comments, keyword case and whitespace outside literals.
DO $$
DECLARE
    v_drift text;
BEGIN
    IF to_regclass('deployment.run_once') IS NULL THEN
        CREATE SCHEMA IF NOT EXISTS deployment;
        CREATE TABLE deployment.run_once (
            id       uuid PRIMARY KEY,
            path     text NOT NULL,
            checksum text NOT NULL,
            ran_at   timestamptz NOT NULL DEFAULT now()
        );
        RETURN;
    END IF;

    SELECT string_agg(COALESCE(m.path, r.path) || ': ' || CASE
               WHEN m.id IS NULL      THEN 'removed from the project, or its <pgmi-meta> id changed'
               WHEN m.path <> r.path  THEN 'moved from ' || r.path
               WHEN m.idempotent      THEN 'now idempotent="true"'
               ELSE 'content edited' END, E'\n  ' ORDER BY r.path)
    INTO v_drift
    FROM deployment.run_once r
    LEFT JOIN pg_temp.pgmi_source_metadata_view m ON m.id = r.id
    LEFT JOIN pg_temp.pgmi_source_view s ON s.path = m.path
    WHERE m.id IS NULL OR m.path <> r.path OR m.idempotent OR s.pgmi_checksum <> r.checksum;

    IF v_drift IS NOT NULL THEN
        RAISE EXCEPTION E'Run-once file(s) changed after they ran on this database:\n  %', v_drift
            USING HINT = format('Restore each file as it ran and put the change in a new run-once file after it. '
                             || 'To rewrite run-once files on a development database, recreate it: pgmi deploy . -d %s --overwrite --param env=dev',
                             CASE WHEN current_database() ~ '^[A-Za-z0-9_.-]+$' THEN current_database()
                                  ELSE '''' || replace(current_database(), '''', '''\''''') || '''' END);
    END IF;
END $$;

-- physical/ holds structure, which runs once per database. A file there
-- without the run-once header would re-run on every deploy and fail the
-- second one; refuse it on the first, before anything runs. A run-once file
-- with several sort keys would run at each key on the first deploy only. An
-- SQL file outside physical/ and semantic/ would load and never run while the
-- deploy reported success.
DO $$
DECLARE
    v_files text;
BEGIN
    SELECT string_agg(m.path, E'\n  ' ORDER BY m.path) INTO v_files
    FROM pg_temp.pgmi_source_metadata_view m
    JOIN pg_temp.pgmi_source_view s ON s.path = m.path
    WHERE s.is_sql_file AND NOT m.idempotent AND cardinality(m.sort_keys) > 1;

    IF v_files IS NOT NULL THEN
        RAISE EXCEPTION E'Run-once file(s) with more than one sort key:\n  %', v_files
            USING HINT = 'A run-once file runs once, at one place in the plan: keep one key, or split the file. Several keys are for re-run files.';
    END IF;

    SELECT string_agg(s.path, E'\n  ' ORDER BY s.path) INTO v_files
    FROM pg_temp.pgmi_source_view s
    LEFT JOIN pg_temp.pgmi_source_metadata_view m ON m.path = s.path
    WHERE s.is_sql_file AND s.path LIKE './physical/%' AND m.idempotent IS DISTINCT FROM false;

    IF v_files IS NOT NULL THEN
        RAISE EXCEPTION E'physical/ file(s) without a run-once header:\n  %', v_files
            USING HINT = 'Start each with /* <pgmi-meta id="<a new uuid, e.g. from uuidgen>" idempotent="false"></pgmi-meta> */, '
                      || 'as physical/001_project.sql does. Views and functions belong in semantic/.';
    END IF;

    SELECT string_agg(s.path, E'\n  ' ORDER BY s.path) INTO v_files
    FROM pg_temp.pgmi_source_view s
    WHERE s.is_sql_file AND s.path !~ '^\./(physical|semantic)/';

    IF v_files IS NOT NULL THEN
        RAISE EXCEPTION E'SQL file(s) outside physical/ and semantic/, which this deploy never runs:\n  %', v_files
            USING HINT = 'Move tables and other structure to physical/ (run once), views and functions to semantic/ (re-run), '
                      || 'and tests to a __test__/ folder; or delete the file.';
    END IF;
END $$;

-- The deployment: every physical/ and semantic/ file in path order, minus the
-- run-once files the ledger holds. A run-once file is recorded in the same
-- transaction it ran in, so a failed deploy records none.
DO $$
DECLARE
    v_env    text := current_setting('pgmi.env');
    v_config jsonb;
    v_file   record;
BEGIN
    -- pgmi loads every file, not only SQL: project.json is data this script reads.
    SELECT content::jsonb INTO STRICT v_config
    FROM pg_temp.pgmi_source_view
    WHERE path = './project.json';

    RAISE NOTICE '[%] Deploying % v%', v_env, v_config ->> 'app_name', v_config ->> 'version';

    FOR v_file IN
        SELECT p.path, p.id, p.idempotent, p.content, p.pgmi_checksum
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file
          AND p.path ~ '^\./(physical|semantic)/'
          AND NOT EXISTS (SELECT FROM deployment.run_once r WHERE r.id = p.id)
        ORDER BY p.execution_order
    LOOP
        RAISE NOTICE '[pgmi] Executing: %', v_file.path;
        -- No exception handler: an error reaches pgmi untouched, which names the
        -- failing file and line and keeps the original SQLSTATE.
        EXECUTE v_file.content;
        IF NOT v_file.idempotent THEN
            INSERT INTO deployment.run_once (id, path, checksum)
            VALUES (v_file.id, v_file.path, v_file.pgmi_checksum)
            ON CONFLICT DO NOTHING;
        END IF;
    END LOOP;

    -- Seed through the writer, like any caller. It refuses a duplicate name
    -- with NULL, so the seed is safe on every deploy.
    IF v_env = 'dev' THEN
        PERFORM fn_create_project('Demo');
    END IF;
END $$;

-- Tests run in the deploy's transaction; their changes roll back here, and a
-- failing test fails the deploy with nothing committed.
SAVEPOINT _tests;
CALL pgmi_test();
ROLLBACK TO SAVEPOINT _tests;

COMMIT;

-- Each statement below commits on its own, as in psql. CREATE INDEX
-- CONCURRENTLY belongs here: it cannot run in a transaction or a DO block.
DO $$
BEGIN
    RAISE NOTICE $ascii$
  ___   ___  _  _ ___
 |   \ / _ \| \| | __|
 | |) | (_) | .` | _|
 |___/ \___/|_|\_|___|
    $ascii$;
END $$;
