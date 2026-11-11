-- ============================================================================
-- deploy.sql - Deployment Orchestrator (Advanced Template)
-- ============================================================================
-- This file controls HOW your SQL gets executed. pgmi loads your files into
-- session-scoped temporary tables, then hands control to this script.
--
-- Available:
--   pg_temp.pgmi_plan_view         - Every project file in execution order
--                                    (join pgmi_source_view for is_sql_file)
--   pg_temp.pgmi_source_view       - Source files
--   pg_temp.pgmi_parameter_view    - Deployment parameters (PGMI_PARAM_*, --params-file, --param)
--   pg_temp.pgmi_test_source_view  - Test files from __test__/ directories
--   CALL pgmi_test()               - Run tests with savepoint isolation
--   CALL pgmi_test(pattern, callback) - Run filtered tests with callback
--
-- Parameters are declared in session.xml and accessed via:
--   current_setting('deployment.<key>') - Direct PostgreSQL session variable
--   pg_temp.deployment_setting(key)     - Helper with error handling
-- ============================================================================


DO $$
BEGIN
    IF current_setting('server_version_num')::int < 180000 THEN
        RAISE EXCEPTION 'The advanced template requires PostgreSQL 18 or newer; this server is PostgreSQL %.',
            current_setting('server_version')
            USING ERRCODE = 'feature_not_supported',
                  HINT = 'Deploy to a PostgreSQL 18+ server. pgmi itself and the basic template run on older versions.';
    END IF;
END $$;

-- A database built by v0.13 or earlier has the deploy log without the
-- run-once checksum and the old membership model. Nothing upgrades it in place.
DO $$
BEGIN
    IF to_regclass('internal.deployment_script_execution_log') IS NOT NULL
       AND NOT EXISTS (SELECT FROM pg_attribute
                       WHERE attrelid = to_regclass('internal.deployment_script_execution_log')
                         AND attname = 'pgmi_checksum' AND NOT attisdropped) THEN
        RAISE EXCEPTION 'Database % was built by pgmi v0.13 or earlier; this template cannot deploy onto it', current_database()
            USING ERRCODE = 'object_not_in_prerequisite_state',
                  DETAIL = 'v0.14 rebuilt the template''s structure and membership model. Nothing was changed.',
                  HINT = 'Deploy to a new database and move the data across: see docs/advanced/UPGRADING.md, "From v0.13 to v0.14" (https://github.com/vvka-141/pgmi/blob/main/docs/advanced/UPGRADING.md).';
    END IF;
END $$;

CREATE TEMPORARY TABLE deployment_parameter (
    key TEXT PRIMARY KEY,
    original_key TEXT NOT NULL,
    value TEXT,
    default_value TEXT,
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
        RAISE EXCEPTION 'session.xml not found in project. Create it to declare deployment parameters.'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    INSERT INTO pg_temp.deployment_parameter (key, original_key, value, description, is_redundant)
    SELECT
        COALESCE(xml.normalized_key, cli.normalized_key),
        COALESCE(xml.original_key, cli.key),
        CASE WHEN btrim(cli.value) <> '' THEN cli.value END,
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


-- Databricks Lakebase, told by two marks every Lakebase database carries. Its
-- defaults let the guards pass the provider's own roles and schema, so one
-- mark alone does not count.
CREATE FUNCTION pg_temp.on_lakebase()
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT to_regrole('databricks_superuser') IS NOT NULL
       AND to_regnamespace('__db_system') IS NOT NULL
$$;

-- Each parameter's default, beside its value. A parameter without one is
-- required, and a blank value counts as not given. env has none: a deploy that
-- does not say which environment it targets must not be treated as dev.
UPDATE pg_temp.deployment_parameter
SET default_value = CASE key
    WHEN 'database_owner_role' THEN current_database()::text || '_owner'
    WHEN 'database_admin_role' THEN current_database()::text || '_admin'
    WHEN 'database_api_role' THEN current_database()::text || '_api'
    WHEN 'database_kernel_role' THEN current_database()::text || '_kernel'
    WHEN 'database_jobs_role' THEN current_database()::text || '_jobs'
    WHEN 'relabel_environment' THEN ''
    WHEN 'upgrade' THEN 'false'
    WHEN 'platform_operators' THEN '[]'
    WHEN 'platform_mfa_required' THEN 'true'
    WHEN 'oidc_issuers' THEN '[]'
    WHEN 'exchange_retention_days' THEN '30'
    WHEN 'www_authenticate' THEN 'Session'
    WHEN 'api_base_path' THEN '/api'
    WHEN 'provider_role_pattern' THEN CASE WHEN pg_temp.on_lakebase() THEN 'databricks\_%' ELSE '' END
    WHEN 'provider_schemas' THEN CASE WHEN pg_temp.on_lakebase() THEN '["__db_system"]' ELSE '[]' END
END;

UPDATE pg_temp.deployment_parameter
SET value = default_value
WHERE value IS NULL;

-- The database keeps the environment it was deployed as (internal.environment),
-- and a redeploy must name the same one. This runs before any role is touched:
-- env=dev against a prod database would otherwise reset its role passwords to
-- the dev default.
DO $$
DECLARE
    v_param     pg_temp.deployment_parameter;
    v_env       text;
    v_relabel   text;
    v_upgrade   text;
    v_label     text;
    v_missing   text;
    v_missing_env text;
    v_redundant text;
    v_defaulted int;
    v_provider  text;
BEGIN
    SELECT string_agg(original_key, ', ' ORDER BY original_key) FILTER (WHERE is_redundant)
    INTO v_redundant
    FROM pg_temp.deployment_parameter;

    IF v_redundant IS NOT NULL THEN
        RAISE EXCEPTION 'Unknown parameters: %. These are not declared in session.xml', v_redundant
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    SELECT value INTO v_env FROM pg_temp.deployment_parameter WHERE key = 'env';
    SELECT value INTO v_relabel FROM pg_temp.deployment_parameter WHERE key = 'relabel_environment';
    SELECT value INTO v_upgrade FROM pg_temp.deployment_parameter WHERE key = 'upgrade';

    IF v_env IS NULL THEN
        RAISE EXCEPTION 'Missing required parameter: env'
            USING ERRCODE = 'invalid_parameter_value', HINT = 'For a local, disposable database pass --param env=dev. For a shared one use env=test or env=prod, with the passwords in PGMI_PARAM_* variables or --params-file -, never --param.';
    END IF;
    IF v_env NOT IN ('dev', 'test', 'prod') THEN
        RAISE EXCEPTION 'Parameter env must be dev, test or prod, got %', quote_literal(v_env)
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF v_relabel NOT IN ('', v_env) THEN
        RAISE EXCEPTION 'Parameter relabel_environment=% does not name the label env=% asks for', v_relabel, v_env
            USING ERRCODE = 'invalid_parameter_value', HINT = 'To change a database''s label, pass the new label as both env and relabel_environment.';
    END IF;
    IF (SELECT value FROM pg_temp.deployment_parameter WHERE key = 'platform_mfa_required') NOT IN ('true', 'false') THEN
        RAISE EXCEPTION 'Parameter platform_mfa_required must be true or false'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF (SELECT value FROM pg_temp.deployment_parameter WHERE key = 'exchange_retention_days') !~ '^0*[1-9][0-9]{0,4}$' THEN
        RAISE EXCEPTION 'Parameter exchange_retention_days must be a whole number of days from 1 to 99999'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF (SELECT value FROM pg_temp.deployment_parameter WHERE key = 'www_authenticate') ~ '[[:cntrl:]]' THEN
        RAISE EXCEPTION 'Parameter www_authenticate must be one header value, without control characters'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF (SELECT value FROM pg_temp.deployment_parameter WHERE key = 'api_base_path') !~ '^(/|(/[A-Za-z0-9._~-]+)+)$' THEN
        RAISE EXCEPTION 'Parameter api_base_path must be / or a path like /api, without a trailing slash'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    -- PostgreSQL truncates a longer name on CREATE ROLE, so the existence
    -- checks would miss the role it made, and two defaults could truncate to
    -- one name.
    SELECT string_agg(format('%s (%s bytes)', key, octet_length(value)), ', ' ORDER BY key) INTO v_missing
    FROM pg_temp.deployment_parameter
    WHERE key ~ '^database_[a-z]+_role$'
      AND octet_length(value) > current_setting('max_identifier_length')::int;
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'Role name(s) longer than % bytes: %', current_setting('max_identifier_length'), v_missing
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Use a shorter database name, or pass shorter role names as those parameters.';
    END IF;
    SELECT value INTO v_provider FROM pg_temp.deployment_parameter WHERE key = 'provider_schemas';
    IF (CASE WHEN pg_input_is_valid(v_provider, 'jsonb')
             THEN jsonb_typeof(v_provider::jsonb) IS DISTINCT FROM 'array'
                  OR jsonb_path_exists(v_provider::jsonb, '$[*] ? (@.type() != "string" || @ == "")')
             ELSE true END) THEN
        RAISE EXCEPTION 'Parameter provider_schemas must be a JSON array of schema names, e.g. ["__db_system"]'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT string_agg(format('%s (%s)', r.key, r.value), ', ' ORDER BY r.key) INTO v_missing
    FROM pg_temp.deployment_parameter r, pg_temp.deployment_parameter p
    WHERE p.key = 'provider_role_pattern' AND p.value <> ''
      AND r.key ~ '^database_[a-z]+_role$' AND r.value LIKE p.value;
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'Parameter provider_role_pattern matches the template''s own role(s): %', v_missing
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'The guards ignore grants to roles that match it; it must name only the provider''s roles, such as databricks\_%.';
    END IF;
    IF v_upgrade NOT IN ('true', 'false') THEN
        RAISE EXCEPTION 'Parameter upgrade must be true or false, got %', quote_literal(v_upgrade)
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF v_upgrade = 'true' AND v_relabel IS DISTINCT FROM '' THEN
        RAISE EXCEPTION 'upgrade=true and relabel_environment=% together: an upgrade never changes the environment label', v_relabel
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    IF to_regclass('internal.environment') IS NOT NULL THEN
        v_label := COALESCE((SELECT label FROM internal.environment), '(none)');
    END IF;

    IF v_label IS NULL THEN
        IF v_upgrade = 'true' THEN
            RAISE EXCEPTION 'upgrade=true, but database % carries no environment label: it was never deployed, and a first deploy is not an upgrade', current_database();
        END IF;
        IF v_relabel IS DISTINCT FROM '' THEN
            RAISE EXCEPTION 'relabel_environment=%, but database % carries no environment label yet: a first deploy labels it from env', v_relabel, current_database()
                USING HINT = 'Remove relabel_environment.';
        END IF;
    ELSIF v_label = v_env AND v_relabel IS DISTINCT FROM '' THEN
        RAISE EXCEPTION 'relabel_environment=%, but database % is already labelled %', v_relabel, current_database(), v_label
            USING HINT = 'Remove relabel_environment, so it cannot stay switched on.';
    ELSIF v_label IS DISTINCT FROM v_env AND v_relabel = '' THEN
        RAISE EXCEPTION 'env=% does not match database %''s environment label (%)', v_env, current_database(), v_label
            USING HINT = format('Deploy with env=%s. To change the label on purpose, pass the new one as both env and relabel_environment.', v_label);
    ELSIF v_label IS DISTINCT FROM v_env THEN
        INSERT INTO internal.environment (label) VALUES (v_env)
        ON CONFLICT (singleton) DO UPDATE SET label = excluded.label, labelled_at = now();
        RAISE WARNING 'pgmi: database % relabelled % -> %. A copy of a production database holds real data: scrub it before using it as dev.',
            current_database(), v_label, v_env;
    END IF;

    -- The dev password default is a local convenience. It applies only to a
    -- new database or one already labelled dev: relabelling a database to dev
    -- needs explicit passwords.
    IF v_env = 'dev' AND COALESCE(v_label, 'dev') = 'dev' THEN
        UPDATE pg_temp.deployment_parameter
        SET value = 'postgres'
        WHERE value IS NULL AND key ~ 'password$';
        GET DIAGNOSTICS v_defaulted = ROW_COUNT;
        IF v_defaulted > 0 THEN
            RAISE WARNING 'pgmi: env=dev — role password(s) defaulted to ''postgres''. This is for local/disposable databases only. For any shared deploy use env=test or env=prod and set each *_password through PGMI_PARAM_<NAME> or --params-file -, never --param.';
        END IF;
    END IF;

    SELECT string_agg(original_key, ', ' ORDER BY original_key) FILTER (WHERE value IS NULL),
           string_agg('PGMI_PARAM_' || upper(original_key), ', ' ORDER BY original_key) FILTER (WHERE value IS NULL)
    INTO v_missing, v_missing_env
    FROM pg_temp.deployment_parameter;

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'Missing required parameters: %. Set % (a CI secret) or use --params-file -; never --param for a secret.', v_missing, v_missing_env
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    FOR v_param IN SELECT * FROM pg_temp.deployment_parameter ORDER BY key LOOP
        PERFORM set_config('deployment.' || v_param.key, v_param.value, false);
        PERFORM set_config('pgmi.' || v_param.key, v_param.value, false);
        RAISE NOTICE '[pgmi] Parameter: % = %',
            v_param.original_key,
            CASE WHEN lower(v_param.key) ~ '(password|secret|token|key|credential|auth)'
                 THEN '********' ELSE v_param.value END;
    END LOOP;
END $$;

-- A file under a dev/ folder, at any depth, deploys only with env=dev. The plan
-- loop, the file-contract check and the run-once drift check all ask this.
CREATE FUNCTION pg_temp.deploys_file(p_path text, p_env text DEFAULT current_setting('deployment.env'))
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT p_path !~ '/dev/' OR p_env = 'dev'
$$;

-- Tests run in every environment, so a test under a dev/ folder would pass on
-- dev and fail on test and prod, where its objects do not exist.
DO $$
DECLARE
    v_tests text;
BEGIN
    SELECT string_agg(path, ', ' ORDER BY path) INTO v_tests
    FROM pg_temp.pgmi_test_source_view
    WHERE NOT pg_temp.deploys_file(path, 'prod');

    IF v_tests IS NOT NULL THEN
        RAISE EXCEPTION 'Test file(s) under a dev/ folder: %', v_tests
            USING HINT = 'Move them to a __test__/ outside dev/ and branch on current_setting(''deployment.env'') where behaviour differs.';
    END IF;
END $$;

-- Every SQL file states its own place in the plan. Without sortKeys pgmi
-- orders a file by its path, and './' sorts before the framework's '003/'
-- and '004/' keys, so a forgotten header ran the file before the tables and
-- types it uses existed. Stop before anything runs and name the files.
DO $$
DECLARE
    v_untagged text;
    v_multi text;
BEGIN
    -- A run-once file is logged at its first key and skipped at the others.
    SELECT string_agg(m.path, ', ' ORDER BY m.path)
    INTO v_multi
    FROM pg_temp.pgmi_source_metadata_view m
    JOIN pg_temp.pgmi_source_view s ON s.path = m.path
    WHERE s.is_sql_file AND NOT m.idempotent AND cardinality(m.sort_keys) > 1;

    IF v_multi IS NOT NULL THEN
        RAISE EXCEPTION 'Run-once file(s) with more than one sort key: %', v_multi
            USING HINT = 'A run-once file runs once, at one place in the plan: keep one key, or split the file. Several keys are for re-run files.';
    END IF;

    SELECT string_agg(DISTINCT p.path, ', ' ORDER BY p.path)
    INTO v_untagged
    FROM pg_temp.pgmi_plan_view p
    JOIN pg_temp.pgmi_source_view s ON s.path = p.path
    WHERE s.is_sql_file AND p.sort_key = p.path;

    IF v_untagged IS NOT NULL THEN
        RAISE EXCEPTION 'SQL file(s) without a place in the plan: %', v_untagged
            USING HINT = 'Add a <pgmi-meta> block with <sortKeys><key>005/...</key></sortKeys> (or later) at the top of each file. See README "Extending the Framework".';
    END IF;
END $$;

-- Structure lives in run-once files (idempotent="false"), code in re-run files
-- (idempotent="true"). A table in a re-run file is silently skipped when its
-- definition changes, and its index DDL locks writers on every deploy; a
-- function in a run-once file can never be changed by a later deploy.
-- Operators and constraint triggers count as structure: neither has an
-- OR REPLACE form, so a re-run file fails on its second deploy.
-- The check reads statement heads at the start of a line after stripping
-- comments, so DDL built as a string for EXECUTE is not seen.
DO $$
DECLARE
    v_structure text;
    v_code text;
BEGIN
    WITH f AS (
        SELECT DISTINCT p.path, p.idempotent,
               regexp_replace(regexp_replace(s.content, '/\*.*?\*/', '', 'g'), '--[^\n]*', '', 'g') AS body
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND pg_temp.deploys_file(p.path)
    ),
    hit AS (
        SELECT DISTINCT f.path, f.idempotent, upper(regexp_replace(m[1], '\s+', ' ', 'g')) AS head
        FROM f
        CROSS JOIN LATERAL regexp_matches(f.body,
            '^\s*(CREATE\s+(UNIQUE\s+|UNLOGGED\s+)?(TABLE|INDEX|TYPE|DOMAIN|SEQUENCE|POLICY)'
            '|CREATE\s+OPERATOR|CREATE\s+CONSTRAINT\s+TRIGGER'
            '|ALTER\s+TABLE'
            '|CREATE\s+(OR\s+REPLACE\s+)?(FUNCTION|PROCEDURE|VIEW))\M', 'gin') m
    ),
    wrong AS (
        SELECT path, idempotent, string_agg(head, ', ' ORDER BY head) AS heads
        FROM hit
        WHERE idempotent <> (head ~ '(FUNCTION|PROCEDURE|VIEW)$')
        GROUP BY path, idempotent
    )
    SELECT
        string_agg(path || ' (' || heads || ')', '; ' ORDER BY path) FILTER (WHERE idempotent),
        string_agg(path || ' (' || heads || ')', '; ' ORDER BY path) FILTER (WHERE NOT idempotent)
    INTO v_structure, v_code
    FROM wrong;

    IF v_structure IS NOT NULL THEN
        RAISE EXCEPTION 'Re-run file(s) define structure: %', v_structure
            USING HINT = 'Move tables, indexes, types, domains, sequences, policies, operators, constraint triggers and ALTER TABLE into a file with idempotent="false".';
    END IF;
    IF v_code IS NOT NULL THEN
        RAISE EXCEPTION 'Run-once file(s) define code: %', v_code
            USING HINT = 'Move functions, procedures and views into a file with idempotent="true".';
    END IF;
END $$;


CREATE FUNCTION pg_temp.required_extension()
RETURNS SETOF text LANGUAGE sql IMMUTABLE AS $$
    SELECT name FROM (VALUES ('btree_gist'), ('hstore'), ('pg_trgm'), ('pgcrypto'), ('uuid-ossp')) AS r(name)
$$;

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

CREATE FUNCTION pg_temp.deployment_setting(p_key text, p_required boolean DEFAULT true)
RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    v_normalized TEXT := 'deployment.' || regexp_replace(lower(p_key), '[^a-z0-9]+', '_', 'g');
    v_value TEXT;
BEGIN
    v_value := current_setting(v_normalized, true);
    IF v_value IS NULL AND p_required THEN
        RAISE EXCEPTION 'Required deployment parameter "%" not set. Declare it in session.xml and set PGMI_PARAM_%.', p_key, upper(p_key)
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    RETURN v_value;
END;
$$;

-- Optional deploy guards: lib/__test__/test_option_<name>.sql, each off until
-- switched on here (README "Deploy guards"). Off, a file still proves on a
-- planted object that its check works; on, it also holds the database to it.
CREATE FUNCTION pg_temp.guard_enabled(p_name text)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    v_enabled boolean;
BEGIN
    SELECT o.enabled INTO v_enabled FROM (VALUES
        ('strict_fk_index',      false),
        ('rls_index_method',     false),
        ('plpgsql_check',        false),
        ('sql_lint',             false),
        ('naming',               false),
        ('handler_reads_views',  false),
        ('view_column_comments', false),
        ('trigger_allowlist',    false)
    ) o(name, enabled)
    WHERE o.name = p_name;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'No deploy guard option named %', p_name;
    END IF;
    RETURN v_enabled;
END $$;

-- What an enabled option lets pass, one row per object, each with the reason.
-- The object is spelled as the option's error names it; an entry naming
-- nothing the option would flag fails as stale.
CREATE FUNCTION pg_temp.guard_exemption()
RETURNS TABLE (guard text, object text, reason text) LANGUAGE sql IMMUTABLE AS $$
    VALUES ('rls_index_method', 'api.ix_access_denial_bucket (brin on api.access_denial)',
            'BRIN minmax on timestamptz compares with the leakproof btree operators, so the purge by bucket still uses it under row-level security')
$$;

-- Fails the deploy when an enabled option found an object it does not exempt,
-- or holds an exemption that names nothing it found.
CREATE FUNCTION pg_temp.enforce_guard(p_guard text, p_found text[])
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_bad   text;
    v_stale text;
BEGIN
    SELECT string_agg(f, E'\n  ' ORDER BY f) INTO v_bad
    FROM unnest(p_found) f
    WHERE f NOT IN (SELECT e.object FROM pg_temp.guard_exemption() e WHERE e.guard = p_guard);
    SELECT string_agg(e.object, ', ' ORDER BY e.object) INTO v_stale
    FROM pg_temp.guard_exemption() e
    WHERE e.guard = p_guard AND e.object <> ALL (p_found);
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION E'Deploy guard %:\n  %', p_guard, v_bad
            USING HINT = format('Fix each, or add (%L, <object>, <reason>) to pg_temp.guard_exemption() in deploy.sql. lib/__test__/test_option_%s.sql states the rule.', p_guard, p_guard);
    END IF;
    IF v_stale IS NOT NULL THEN
        RAISE EXCEPTION 'Deploy guard %: stale exemption(s), nothing to exempt: %', p_guard, v_stale
            USING HINT = 'Remove them from pg_temp.guard_exemption() in deploy.sql.';
    END IF;
END $$;

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

-- ============================================================================
-- ROLE SETUP: roles, extensions, ownership (CREATEROLE + CREATEDB, no superuser)
-- ============================================================================
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
    -- points listed at the end of the deploy, nothing else
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


-- What the database keeps for every new session, one row per setting. Each is
-- applied to this session too and stored FROM CURRENT, so the database holds
-- the server's own spelling of the value.
CREATE FUNCTION pg_temp.database_setting()
RETURNS TABLE (name text, value text) LANGUAGE sql IMMUTABLE AS $$
    VALUES
        ('search_path', 'core, api, membership, internal, extensions, common, pg_temp')
$$;

DO $database_setting$
DECLARE
    v_setting record;
BEGIN
    FOR v_setting IN SELECT * FROM pg_temp.database_setting() LOOP
        PERFORM set_config(v_setting.name, v_setting.value, false);
        EXECUTE format('ALTER DATABASE %I SET %s FROM CURRENT', current_database(), v_setting.name);
    END LOOP;
END $database_setting$;


-- Extensions live in schema extensions, and lib/ calls them by that name. An
-- image or provider that pre-installs one in public would turn CREATE EXTENSION
-- IF NOT EXISTS into a silent no-op, so a pre-installed one is moved instead.
-- No plv8: unblocks managed cloud PostgreSQL (RDS, Azure Flexible Server,
-- Google Cloud SQL). Handlers ship as plpgsql / sql; add plv8 yourself via
-- a sortKeys-005+ file if you need JavaScript handlers AND your provider
-- supports the extension.
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
-- past row-level security, so they are revoked before the test gate.
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


-- ============================================================================
-- OWNER PHASE: SET ROLE and Create Schemas
-- ============================================================================
DO $owner_phase$
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_owner_role'));
    RAISE NOTICE '[pgmi] Switched to owner role: %', pg_temp.deployment_setting('database_owner_role');
END $owner_phase$;


-- The application schemas and each privilege granted on them, the role named
-- by its deploy parameter; a schema without a grant has one row with a NULL
-- role. public is PostgreSQL's own and stays closed. A project that adds a
-- schema or a grant adds its rows here: lib/__test__/test_approved_schemas.sql
-- fails on any schema or schema grant this list does not hold.
CREATE FUNCTION pg_temp.approved_schema_grant()
RETURNS TABLE (schema_name text, role_key text, privilege text)
LANGUAGE sql IMMUTABLE AS $$
    VALUES ('api',        'database_api_role',    'USAGE'),
           ('api',        'database_kernel_role', 'USAGE'),
           ('api',        'database_jobs_role',   'USAGE'),
           ('common',     'database_admin_role',  'USAGE'),
           ('common',     'database_api_role',    'USAGE'),
           ('common',     'database_kernel_role', 'USAGE'),
           ('core',       'database_api_role',    'USAGE'),
           ('core',       'database_kernel_role', 'USAGE'),
           ('internal',   'database_kernel_role', 'USAGE'),
           ('membership', 'database_admin_role',  'USAGE'),
           ('membership', 'database_api_role',    'USAGE'),
           ('membership', 'database_kernel_role', 'USAGE'),
           ('public',     NULL,                   NULL)
$$;

DO $schemas$
DECLARE
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_kernel_role TEXT := pg_temp.deployment_setting('database_kernel_role');
BEGIN
    PERFORM pg_temp.quiet_bootstrap();
    CREATE SCHEMA IF NOT EXISTS internal;
    CREATE SCHEMA IF NOT EXISTS core;
    CREATE SCHEMA IF NOT EXISTS api;
    CREATE SCHEMA IF NOT EXISTS common;
    CREATE SCHEMA IF NOT EXISTS membership;
    PERFORM pg_temp.unquiet_bootstrap();

    COMMENT ON SCHEMA internal IS 'Deployment infrastructure and system tables. Not for application use.';
    COMMENT ON SCHEMA core IS 'Core domain entities and business logic. The heart of your application.';
    COMMENT ON SCHEMA api IS 'Public API layer. Functions and views exposed to applications and external clients.';
    COMMENT ON SCHEMA common IS 'Cross-cutting primitives (casting, encoding, text) shared across all schemas and roles.';
    COMMENT ON SCHEMA membership IS 'User identity, organizations, invitations, and access control.';

    REVOKE ALL ON SCHEMA public FROM PUBLIC;
    REVOKE ALL ON SCHEMA common, api, core, internal, membership FROM PUBLIC;

    EXECUTE format('GRANT USAGE ON SCHEMA api, common, core, membership TO %I', v_api_role);
    EXECUTE format('GRANT USAGE ON SCHEMA common, membership TO %I', v_admin_role);
    EXECUTE format('GRANT USAGE ON SCHEMA api, common, core, internal, membership TO %I', v_kernel_role);
    -- The kernel calls the helpers of membership and common from the moment
    -- they exist; the request-context readers it calls are granted with them.
    EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA common, membership GRANT EXECUTE ON ROUTINES TO %I', v_kernel_role);
    PERFORM pg_temp.revoke_provider_create('{internal,core,api,common,membership}');

    RAISE NOTICE '[pgmi] Schemas configured';
END $schemas$;


-- ============================================================================
-- TRACKING TABLES
-- ============================================================================
-- These tables track which scripts have been executed. They must exist
-- before the deploy loop runs so it can log executions.

SELECT pg_temp.quiet_bootstrap();

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
    CONSTRAINT deployment_script_content_checksum_check CHECK(checksum = encode(extensions.digest(convert_to(value, 'UTF8'), 'sha256'), 'hex'))
);

COMMENT ON TABLE internal.deployment_script_content IS 'Content-addressable storage for script versions. Deduplicates identical content across scripts and deployments.';
COMMENT ON COLUMN internal.deployment_script_content.checksum IS 'SHA-256 hash of script content. Primary key enables content deduplication.';
COMMENT ON COLUMN internal.deployment_script_content.value IS 'Full script content. Immutable once stored (enforced by checksum constraint).';
COMMENT ON COLUMN internal.deployment_script_content.registered_at IS 'Timestamp when this content version was first deployed.';
COMMENT ON COLUMN internal.deployment_script_content.registered_by IS 'Database role that first deployed this content version.';
COMMENT ON CONSTRAINT deployment_script_content_checksum_check ON internal.deployment_script_content IS 'Ensures checksum matches actual content. Prevents tampering and guarantees content integrity.';

CREATE TABLE IF NOT EXISTS internal.deployment_script_execution_log(
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deployment_script_object_id uuid NOT NULL REFERENCES internal.deployment_script(object_id),
    deployment_script_content_checksum TEXT NOT NULL REFERENCES internal.deployment_script_content(checksum),
    pgmi_checksum TEXT NOT NULL,
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
COMMENT ON COLUMN internal.deployment_script_execution_log.pgmi_checksum IS 'pgmi''s normalized checksum of the content (comments, keyword case and whitespace outside literals ignored). A run-once file whose normalized content no longer matches the one it ran with is refused (pg_temp.run_once_drift).';
COMMENT ON COLUMN internal.deployment_script_execution_log.xact_id IS 'PostgreSQL transaction ID (xid8). Correlates with pg_stat_activity and WAL for debugging.';
COMMENT ON COLUMN internal.deployment_script_execution_log.file_path IS 'Original file path at execution time. May change if files are renamed.';
COMMENT ON COLUMN internal.deployment_script_execution_log.idempotent IS 'Whether the script was re-runnable at execution time. A non-idempotent script runs once after being marked non-idempotent (deploy.sql skips it only when a prior execution logged idempotent = false), so flipping an already-run idempotent script to non-idempotent still runs it once under the new contract.';
COMMENT ON COLUMN internal.deployment_script_execution_log.sort_key IS 'Execution ordering key from the <sortKeys><key> elements of <pgmi-meta>. NULL for path-ordered scripts.';
COMMENT ON COLUMN internal.deployment_script_execution_log.executed_at IS 'Timestamp when execution completed.';
COMMENT ON COLUMN internal.deployment_script_execution_log.executed_by IS 'Database role that executed the script (usually the owner role).';

CREATE INDEX IF NOT EXISTS ix_deployment_script_execution_log_object_id
    ON internal.deployment_script_execution_log(deployment_script_object_id, idempotent)
    WHERE idempotent = false;

COMMENT ON INDEX internal.ix_deployment_script_execution_log_object_id IS 'Partial index for fast idempotency checks. Only indexes non-idempotent scripts since idempotent scripts always re-run.';

CREATE TABLE IF NOT EXISTS internal.deployment(
    xact_id xid8 PRIMARY KEY,
    deployed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deployed_by TEXT NOT NULL DEFAULT SESSION_USER,
    environment TEXT NOT NULL
);

COMMENT ON TABLE internal.deployment IS 'One row per committed deploy. A failed deploy rolls back, its row with it. Read through internal.vw_deployment.';
COMMENT ON COLUMN internal.deployment.xact_id IS 'Transaction of the deploy''s atomic head. Every script it ran carries it in internal.deployment_script_execution_log.';
COMMENT ON COLUMN internal.deployment.deployed_at IS 'Start of the deploy''s transaction.';
COMMENT ON COLUMN internal.deployment.deployed_by IS 'Login role pgmi connected as (session_user), not the owner role the files run as.';
COMMENT ON COLUMN internal.deployment.environment IS 'The env parameter of the deploy: dev, test or prod.';

DO $permissions$
DECLARE
    v_owner_role TEXT := pg_temp.deployment_setting('database_owner_role');
BEGIN
    REVOKE ALL ON TABLE internal.deployment_script_execution_log FROM PUBLIC;
    GRANT SELECT ON TABLE internal.deployment_script_execution_log TO PUBLIC;
    EXECUTE format('GRANT INSERT, UPDATE, DELETE ON TABLE internal.deployment_script_execution_log TO %I', v_owner_role);
    EXECUTE format('GRANT USAGE ON SEQUENCE internal.deployment_script_execution_log_id_seq TO %I', v_owner_role);
END $permissions$;

-- ============================================================================
-- ROW-LEVEL SECURITY AND THE KERNEL ROLE
-- ============================================================================
-- Every table the owner creates in an application schema is forced
-- (ENABLE + FORCE ROW LEVEL SECURITY, so the owner that runs every handler is
-- held to the policies too) and has a policy, or is declared exempt here with
-- the reason. lib/__test__/test_forced_rls.sql fails the deploy otherwise,
-- naming the table.
CREATE TABLE IF NOT EXISTS internal.rls_exemption (
    relation regclass PRIMARY KEY,
    reason   text NOT NULL CONSTRAINT rls_exemption_reason_check CHECK (btrim(reason) <> '')
);

COMMENT ON TABLE internal.rls_exemption IS 'Tables that are not forced to row-level security, each with the reason. Written by pg_temp.exempt_from_rls in the run-once file that creates the table.';

CREATE FUNCTION pg_temp.exempt_from_rls(p_table regclass, p_reason text)
RETURNS void LANGUAGE sql AS $$
    INSERT INTO internal.rls_exemption (relation, reason) VALUES (p_table, p_reason)
    ON CONFLICT (relation) DO UPDATE SET reason = excluded.reason;
$$;

-- A foreign key left without an index leading with it, with the reason;
-- lib/__test__/test_fk_index_coverage.sql fails on any other, and on an entry
-- whose key is indexed or gone. Written by pg_temp.exempt_from_fk_index in the
-- run-once file that creates the key.
CREATE TABLE IF NOT EXISTS internal.fk_index_exemption (
    relation    regclass NOT NULL,
    column_name name     NOT NULL,
    reason      text     NOT NULL CONSTRAINT fk_index_exemption_reason_check CHECK (btrim(reason) <> ''),
    PRIMARY KEY (relation, column_name)
);

COMMENT ON TABLE internal.fk_index_exemption IS 'Foreign-key columns deliberately left without an index leading with them, each with the reason. Written by pg_temp.exempt_from_fk_index.';

CREATE FUNCTION pg_temp.exempt_from_fk_index(p_table regclass, p_columns name[], p_reason text)
RETURNS void LANGUAGE sql AS $$
    INSERT INTO internal.fk_index_exemption (relation, column_name, reason)
    SELECT p_table, c, p_reason FROM unnest(p_columns) c
    ON CONFLICT (relation, column_name) DO UPDATE SET reason = excluded.reason;
$$;

-- The columns that name a row's organization: core tables use organization_id
-- (core.apply_org_rls), membership tables organization_object_id. The RLS and
-- foreign-key guards in lib/__test__/ read them from here.
CREATE FUNCTION pg_temp.tenant_column()
RETURNS SETOF name LANGUAGE sql IMMUTABLE AS $$
    VALUES ('organization_id'::name), ('organization_object_id')
$$;

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

-- Each row policy as the deploy's files left it. Policies live in run-once
-- files, so a policy edited by hand later is repaired by no deploy;
-- lib/__test__/test_policy_drift.sql compares the live policies with this
-- record.
CREATE TABLE IF NOT EXISTS internal.policy_fingerprint (
    policy     text PRIMARY KEY,
    definition text NOT NULL,
    file_path  text NOT NULL
);

COMMENT ON TABLE internal.policy_fingerprint IS 'Each row policy as the deploy file that created or last changed it left it: command, roles, USING and WITH CHECK. Written by the deploy loop; read by lib/__test__/test_policy_drift.sql.';

SELECT pg_temp.unquiet_bootstrap();

SELECT pg_temp.exempt_from_rls(t::regclass, 'deploy bookkeeping: written and read by deploy.sql only')
FROM unnest(ARRAY['internal.deployment_script', 'internal.deployment_script_content',
                  'internal.deployment_script_execution_log', 'internal.deployment',
                  'internal.rls_exemption', 'internal.policy_fingerprint', 'internal.fk_index_exemption']) t;

SELECT pg_temp.exempt_from_fk_index('internal.deployment_script_execution_log', '{deployment_script_content_checksum}',
                                    'deploy bookkeeping: content rows are never deleted and the log is read by script');

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
-- Run as the deploy login: the owner holds the kernel WITH INHERIT FALSE.
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
-- back every deploy), nor EXECUTE to PUBLIC, which the end-of-deploy sweep
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

-- Whether a service principal names the role. Read as the kernel: the
-- table's row-level security shows the owner no rows outside a request.
CREATE FUNCTION pg_temp.service_registered(p_role text)
RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_found  boolean;
BEGIN
    EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_kernel_role'));
    v_found := EXISTS (SELECT FROM membership.service_principal WHERE database_role = p_role);
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_found;
END $$;

-- The optional background-jobs login (README: Background jobs): never created
-- here. When it exists and is a registered service principal it may connect
-- and execute every api.job_* routine, including jobs added since the last
-- deploy, and nothing else. A role of that name nobody registered may have
-- been created by anyone, so it gets nothing and loses what it held. Run as
-- the owner at the end of every deploy.
CREATE FUNCTION pg_temp.grant_jobs()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_jobs    text := pg_temp.deployment_setting('database_jobs_role');
    v_grantee text;
    v_routine text;
BEGIN
    IF to_regrole(quote_ident(v_jobs)) IS NULL THEN
        RETURN;
    END IF;
    IF v_jobs IN (pg_temp.deployment_setting('database_owner_role'), pg_temp.deployment_setting('database_admin_role'),
                  pg_temp.deployment_setting('database_api_role'), pg_temp.deployment_setting('database_kernel_role')) THEN
        RAISE EXCEPTION 'database_jobs_role names another template role: %', v_jobs;
    END IF;
    IF NOT pg_temp.service_registered(v_jobs) THEN
        v_grantee := to_regrole(quote_ident(v_jobs))::text;
        INSERT INTO pg_temp.access_removal
        SELECT e.object, e.entry FROM pg_temp.access_entry() e
        WHERE (e.object = 'api' AND e.entry = 'grant USAGE to ' || v_grantee)
           OR (e.object LIKE 'api.job\_%' AND e.entry = 'grant EXECUTE to ' || v_grantee)
        ON CONFLICT DO NOTHING;
        EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM %I', current_database(), v_jobs);
        EXECUTE format('REVOKE USAGE ON SCHEMA api FROM %I', v_jobs);
        FOR v_routine IN
            SELECT p.oid::regprocedure::text FROM pg_proc p
            WHERE p.pronamespace = 'api'::regnamespace AND p.proname LIKE 'job\_%'
        LOOP
            EXECUTE format('REVOKE EXECUTE ON ROUTINE %s FROM %I', v_routine, v_jobs);
        END LOOP;
        RAISE NOTICE '[pgmi] Jobs login % is no registered service principal: granted nothing. Register it as the admin login (README: The jobs login), then redeploy: SELECT membership.fn_register_service_principal(''service.jobs'', ''Background jobs'', %);', v_jobs, quote_literal(v_jobs);
        RETURN;
    END IF;
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), v_jobs);
    EXECUTE format('GRANT USAGE ON SCHEMA api TO %I', v_jobs);
    FOR v_routine IN
        SELECT p.oid::regprocedure::text FROM pg_proc p
        WHERE p.pronamespace = 'api'::regnamespace AND p.proname LIKE 'job\_%'
    LOOP
        EXECUTE format('GRANT EXECUTE ON ROUTINE %s TO %I', v_routine, v_jobs);
    END LOOP;
END $$;

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

-- ============================================================================
-- DEPLOY FUNCTION
-- ============================================================================
-- A run-once file is skipped once it has run, so a later edit would reach only
-- fresh databases: the databases that ran it keep the old structure, and a
-- fresh deploy no longer builds what they have. One row per run-once file that
-- ran here and changed since: edited, moved, re-sorted, flipped to re-run or
-- removed. A run-once file is known by its <pgmi-meta> id (or its path when it
-- has none, so a moved file without an id shows as removed). Content is
-- compared by pgmi's normalized checksum: a comment edit, or a whitespace edit
-- outside literals, is not drift.
-- Both sides keep only the files this environment deploys: a dev/ file that
-- ran while the database was dev is not drift on a test deploy.
CREATE FUNCTION pg_temp.run_once_drift()
RETURNS TABLE (path text, drift text) LANGUAGE sql STABLE AS $$
    WITH plan AS (
        SELECT COALESCE(p.id, p.generic_id) AS object_id, p.path, p.idempotent, p.pgmi_checksum,
               min(p.sort_key COLLATE "C") AS first_sort_key
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND pg_temp.deploys_file(p.path)
        GROUP BY 1, 2, 3, 4
    ),
    ran AS (
        SELECT DISTINCT ON (l.deployment_script_object_id)
               l.deployment_script_object_id AS object_id, l.file_path, l.sort_key, l.pgmi_checksum
        FROM internal.deployment_script_execution_log l
        WHERE NOT l.idempotent AND pg_temp.deploys_file(l.file_path)
        ORDER BY l.deployment_script_object_id, l.id
    )
    SELECT COALESCE(p.path, r.file_path), d.drift
    FROM ran r
    LEFT JOIN plan p ON p.object_id = r.object_id
    CROSS JOIN LATERAL (VALUES
        (p.object_id IS NULL, 'removed from the project'),
        (p.idempotent, 'now idempotent="true"'),
        (p.path <> r.file_path, format('moved from %s', r.file_path)),
        (p.first_sort_key <> r.sort_key, format('sort key %s, ran at %s', p.first_sort_key, r.sort_key)),
        (p.pgmi_checksum <> r.pgmi_checksum, 'content edited')
    ) AS d(drifted, drift)
    WHERE d.drifted
$$;

-- What a deploy file did to the row policies: each policy it created or
-- changed is recorded as it now stands, each it dropped is forgotten. p_before
-- is pg_temp.policy_definition() before the file ran.
CREATE FUNCTION pg_temp.record_policies(p_before jsonb, p_path text)
RETURNS void LANGUAGE sql AS $$
    WITH now_state AS MATERIALIZED (SELECT * FROM pg_temp.policy_definition()),
    gone AS (
        DELETE FROM internal.policy_fingerprint f
        WHERE p_before ? f.policy AND NOT EXISTS (SELECT FROM now_state n WHERE n.policy = f.policy)
    )
    INSERT INTO internal.policy_fingerprint (policy, definition, file_path)
    SELECT n.policy, n.definition, p_path FROM now_state n
    WHERE (p_before ->> n.policy) IS DISTINCT FROM n.definition
    ON CONFLICT (policy) DO UPDATE SET definition = excluded.definition, file_path = excluded.file_path
$$;

CREATE FUNCTION pg_temp.deploy() RETURNS VOID
LANGUAGE plpgsql AS $fn$
DECLARE
    v_script RECORD;
    v_object_id uuid;
    v_executed int := 0;
    v_skipped int := 0;
    v_drift text;
    v_policies jsonb;
BEGIN
    SELECT string_agg(d.path || ': ' || d.drift, E'\n  ' ORDER BY d.path COLLATE "C", d.drift)
    INTO v_drift
    FROM pg_temp.run_once_drift() d;

    IF v_drift IS NOT NULL THEN
        RAISE EXCEPTION E'Run-once file(s) changed after they ran on this database:\n  %', v_drift
            USING HINT = format('Restore each file as it ran, and put the change in a new run-once file sorted after it. '
                             || 'To rewrite run-once files on a dev database, recreate it: pgmi deploy . -d %s --overwrite --param env=dev',
                             CASE WHEN current_database() ~ '^[A-Za-z0-9_.-]+$' THEN current_database()
                                  ELSE '''' || replace(current_database(), '''', '''\''''') || '''' END);
    END IF;

    FOR v_script IN (
        SELECT
            p.path, p.id, p.generic_id, p.sort_key, p.idempotent, p.execution_order, p.content,
            p.pgmi_checksum,
            encode(extensions.digest(convert_to(p.content, 'UTF8'), 'sha256'), 'hex') AS checksum
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND pg_temp.deploys_file(p.path)
        ORDER BY p.execution_order
    )
    LOOP
        v_object_id := COALESCE(v_script.id, v_script.generic_id);

        -- Skip a run-once file only after it ran as run-once: a re-run file
        -- flipped to idempotent="false" must still run once, not be skipped
        -- for its earlier re-run log rows.
        IF NOT v_script.idempotent AND EXISTS (
            SELECT 1 FROM internal.deployment_script_execution_log
            WHERE deployment_script_object_id = v_object_id
              AND NOT idempotent
        ) THEN
            v_skipped := v_skipped + 1;
            CONTINUE;
        END IF;

        INSERT INTO internal.deployment_script(object_id)
        VALUES (v_object_id) ON CONFLICT DO NOTHING;

        INSERT INTO internal.deployment_script_content(checksum, value)
        VALUES (v_script.checksum, v_script.content) ON CONFLICT DO NOTHING;

        RAISE NOTICE '[pgmi] Executing: %', v_script.path;
        -- No exception handler: an error reaches pgmi untouched, which names the
        -- failing file and line and keeps the original SQLSTATE the retry
        -- contract depends on.
        SELECT jsonb_object_agg(policy, definition) INTO v_policies FROM pg_temp.policy_definition();
        PERFORM pg_temp.quiet_framework(v_script.path);
        EXECUTE v_script.content;
        PERFORM pg_temp.unquiet_framework(v_script.path);
        PERFORM pg_temp.record_policies(COALESCE(v_policies, '{}'), v_script.path);

        INSERT INTO internal.deployment_script_execution_log(
            deployment_script_object_id, deployment_script_content_checksum, pgmi_checksum,
            xact_id, file_path, idempotent, sort_key
        ) VALUES (
            v_object_id, v_script.checksum, v_script.pgmi_checksum,
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
        WHEN 'suite_end' THEN RAISE NOTICE '[pgmi] Test suite passed';
        WHEN 'fixture_start' THEN
            RAISE NOTICE '[pgmi] Fixture: %', e.path;
            PERFORM pg_temp.quiet_framework(e.path);
        WHEN 'fixture_end' THEN PERFORM pg_temp.unquiet_framework(e.path);
        WHEN 'test_start' THEN
            RAISE NOTICE '[pgmi] Test: %', e.path;
            PERFORM pg_temp.quiet_framework(e.path);
        WHEN 'test_end' THEN PERFORM pg_temp.unquiet_framework(e.path);
        WHEN 'teardown_start' THEN RAISE DEBUG '[pgmi] Teardown: %', e.directory;
        ELSE NULL;
    END CASE;
END $$;


-- ============================================================================
-- EXECUTE DEPLOYMENT
-- ============================================================================
-- Transaction-scoped advisory lock: released on COMMIT or ROLLBACK, so a
-- failed deploy never leaves it held while the session stays open.
BEGIN;
    SELECT pg_advisory_xact_lock(hashtext('pgmi_deploy_' || current_database()));
    DO $$ BEGIN RAISE NOTICE '[pgmi] Acquired deployment lock (transaction-scoped)'; END $$;
    -- A table lock the deploy cannot get within 5s fails it (55P03) instead of
    -- queueing every application query behind it for the rest of the deploy.
    SET LOCAL lock_timeout = '5s';
    INSERT INTO pg_temp.access_baseline SELECT * FROM pg_temp.access_entry();
    SELECT pg_temp.kernel_reclaim();
    INSERT INTO internal.deployment (xact_id, environment)
    VALUES (pg_current_xact_id(), current_setting('deployment.env'));
    SELECT pg_temp.deploy();
    DO $catalogs$
    DECLARE
        v_unsynced text;
    BEGIN
        SELECT string_agg(s.path, ', ' ORDER BY s.path) INTO v_unsynced
        FROM pg_temp.pgmi_source_view s
        WHERE s.path LIKE './data/catalogs/%'
          AND NOT EXISTS (SELECT FROM pg_temp.catalog_synced c WHERE c.path = s.path);

        IF v_unsynced IS NOT NULL THEN
            RAISE EXCEPTION 'Catalog file(s) that no file synced: %', v_unsynced
                USING HINT = 'A file data/catalogs/<schema>.<table>.json is synced by SELECT pg_temp.sync_catalog(''<schema>.<table>''); in a re-run file sorted after the table''s run-once file. Remove a file that names no table.';
        END IF;
    END $catalogs$;
    SELECT pg_temp.apply_entity_standards_all();
    DO $privs$
    DECLARE
        v_api_role text := pg_temp.deployment_setting('database_api_role');
        v_caller   text := current_user;
        v_routine  text;
        v_issuer   text;
    BEGIN
        -- The default privileges keep new routines closed; this closes the rest
        -- in every application schema: a routine a file granted to PUBLIC, or
        -- one created by a role other than the owner. A routine callable by
        -- PUBLIC is callable by any role holding USAGE on its schema, and the
        -- api schema holds SECURITY DEFINER gateways that open sessions and act
        -- for whoever a session names.
        FOR v_routine IN
            SELECT format('%I.%I(%s)', s.name, p.proname, pg_get_function_identity_arguments(p.oid))
            FROM pg_proc p
            JOIN pg_temp.application_schema() s ON s.oid = p.pronamespace
            WHERE has_function_privilege('public', p.oid, 'EXECUTE')
              AND NOT EXISTS (
                  SELECT FROM pg_depend d
                  WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
        LOOP
            EXECUTE format('REVOKE EXECUTE ON ROUTINE %s FROM PUBLIC', v_routine);
        END LOOP;

        -- The gateway executes its entry points and nothing else: each one
        -- decides access before any handler runs. The admin login needs no
        -- grant: it is a member of the owner. lib/__test__/test_exact_grants.sql
        -- holds every login to its list.
        EXECUTE format('GRANT EXECUTE ON ROUTINE '
                       'api.rest_invoke(text, text, extensions.hstore, bytea), '
                       'api.rest_invoke(text, text, extensions.hstore, jsonb), '
                       'api.rest_invoke(text, text, extensions.hstore, xml), '
                       'api.rest_route_policy(text, text, text), '
                       'api.rpc_resolve(text), '
                       'api.rpc_invoke(uuid, extensions.hstore, bytea), '
                       'api.rpc_route_policy(text, text), '
                       'api.mcp_handle_request(jsonb, jsonb), '
                       'api.mcp_request_policy(text, text, text, text, text), '
                       'api.catalog_version(), '
                       'api.open_login_session(text, text, text, text, boolean, text[]), '
                       'api.close_login_session(text) TO %I', v_api_role);
        IF current_setting('deployment.env') = 'dev' THEN
            IF to_regprocedure('api.open_dev_login_session(text, text, text, text)') IS NOT NULL THEN
                EXECUTE format('GRANT EXECUTE ON ROUTINE api.open_dev_login_session(text, text, text, text) TO %I', v_api_role);
            END IF;
        ELSE
            -- A database relabelled from dev keeps what dev files created;
            -- a development issuer someone signed in with stays (foreign key) but is inert:
            -- api.open_login_session accepts only oidc issuers.
            DROP FUNCTION IF EXISTS api.open_dev_login_session(text, text, text, text);
            EXECUTE format('SET ROLE %I', pg_temp.deployment_setting('database_kernel_role'));
            UPDATE membership.login_session s
            SET revoked_at = clock_timestamp(), revoke_reason = 'environment relabelled'
            FROM membership.external_identity e
            JOIN membership.identity_issuer i ON i.id = e.issuer_id
            WHERE s.external_identity_object_id = e.object_id
              AND i.kind = 'development' AND s.revoked_at IS NULL;
            EXECUTE format('SET ROLE %I', v_caller);
            FOR v_issuer IN SELECT id FROM membership.identity_issuer WHERE kind = 'development' LOOP
                BEGIN
                    DELETE FROM membership.identity_issuer WHERE id = v_issuer;
                EXCEPTION WHEN foreign_key_violation THEN
                    NULL;
                END;
            END LOOP;
        END IF;
        PERFORM pg_temp.grant_jobs();

        RAISE NOTICE '[pgmi] Privileges converged (no routine callable by PUBLIC, the gateway on its entry points only)';
    END $privs$;
    DO $access$
    DECLARE
        v_lost text;
    BEGIN
        SELECT string_agg(object || ': ' || entry, E'\n  ' ORDER BY object COLLATE "C", entry COLLATE "C")
        INTO v_lost
        FROM pg_temp.access_lost();

        IF v_lost IS NOT NULL THEN
            RAISE EXCEPTION E'This deploy took away grants, policies or triggers the database had before it:\n  %', v_lost
                USING HINT = 'Restore them (a DROP ... CASCADE takes the grants, policies and triggers of what depends on it). To remove one on purpose, the file that removes it declares it: INSERT INTO pg_temp.access_removal (object, entry) VALUES (''<object>'', ''<entry>'');';
        END IF;
    END $access$;
    SELECT pg_temp.revoke_provider_table_privileges();
    SAVEPOINT _tests;
    CALL pgmi_test(NULL, 'pg_temp.test_observer');
    ROLLBACK TO SAVEPOINT _tests;
COMMIT;
DO $$ BEGIN RAISE NOTICE '[pgmi] Deployment lock released'; END $$;

-- ============================================================================
-- TAIL: each statement below commits on its own, as in psql
-- ============================================================================
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
