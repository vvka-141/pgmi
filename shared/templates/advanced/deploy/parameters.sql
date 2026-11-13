-- ============================================================================
-- deploy/parameters.sql
-- Parameters declared in deploy/parameters.xml, their defaults, their checks
-- and the environment label. Ends with each value set as pgmi.<key>, read
-- through pg_temp.deployment_setting.
-- ============================================================================

-- ############################################################################
-- VALUES - each declared parameter with its given value or its default
-- ############################################################################
CREATE TEMPORARY TABLE deployment_parameter (
    key TEXT PRIMARY KEY,
    original_key TEXT NOT NULL,
    value TEXT,
    default_value TEXT,
    description TEXT,
    is_redundant BOOLEAN NOT NULL DEFAULT FALSE
);

-- --param Database-Admin-Password and the declared database_admin_password
-- name one parameter.
CREATE FUNCTION pg_temp.parameter_key(p_key text)
RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT regexp_replace(lower(p_key), '[^a-z0-9]+', '_', 'g')
$$;

DO $$
DECLARE
    v_xml XML;
BEGIN
    SELECT content::xml INTO v_xml
    FROM pg_temp.pgmi_source_view
    WHERE path = './deploy/parameters.xml';

    IF v_xml IS NULL THEN
        RAISE EXCEPTION 'deploy/parameters.xml not found in project. Create it to declare deployment parameters.'
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
            pg_temp.parameter_key(trim(param_key)) AS normalized_key,
            NULLIF(trim(COALESCE(param_desc, '')), '') AS description
        FROM XMLTABLE('/parameters/parameter' PASSING v_xml
            COLUMNS param_key TEXT PATH '@key', param_desc TEXT PATH '@description')
        WHERE param_key IS NOT NULL AND trim(param_key) != ''
    ) xml
    FULL OUTER JOIN (
        SELECT key, value, pg_temp.parameter_key(key) AS normalized_key
        FROM pg_temp.pgmi_parameter_view
    ) cli ON xml.normalized_key = cli.normalized_key;
END $$;



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
    WHEN 'provider_role_pattern' THEN pg_temp.provider_default(key)
    WHEN 'provider_schemas' THEN pg_temp.provider_default(key)
END;

UPDATE pg_temp.deployment_parameter
SET value = default_value
WHERE value IS NULL;

CREATE FUNCTION pg_temp.parameter_value(p_key text)
RETURNS text LANGUAGE sql STABLE AS $$
    SELECT value FROM pg_temp.deployment_parameter WHERE key = p_key
$$;

-- ############################################################################
-- CHECKS - each value's form, before anything reads it
-- ############################################################################
-- A misspelt parameter would otherwise be ignored and its default used.
DO $$
DECLARE
    v_redundant text;
BEGIN
    SELECT string_agg(original_key, ', ' ORDER BY original_key) FILTER (WHERE is_redundant)
    INTO v_redundant
    FROM pg_temp.deployment_parameter;

    IF v_redundant IS NOT NULL THEN
        RAISE EXCEPTION 'Unknown parameters: %. These are not declared in deploy/parameters.xml', v_redundant
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
END $$;

-- env has no default: a deploy that does not say which environment it targets
-- must not be treated as dev.
DO $$
DECLARE
    v_env     text := pg_temp.parameter_value('env');
    v_relabel text := pg_temp.parameter_value('relabel_environment');
BEGIN
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
END $$;

-- These values reach a setting, a header and a URL path as they are.
DO $$
BEGIN
    IF pg_temp.parameter_value('platform_mfa_required') NOT IN ('true', 'false') THEN
        RAISE EXCEPTION 'Parameter platform_mfa_required must be true or false'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF pg_temp.parameter_value('exchange_retention_days') !~ '^0*[1-9][0-9]{0,4}$' THEN
        RAISE EXCEPTION 'Parameter exchange_retention_days must be a whole number of days from 1 to 99999'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF pg_temp.parameter_value('www_authenticate') ~ '[[:cntrl:]]' THEN
        RAISE EXCEPTION 'Parameter www_authenticate must be one header value, without control characters'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF pg_temp.parameter_value('api_base_path') !~ '^(/|(/[A-Za-z0-9._~-]+)+)$' THEN
        RAISE EXCEPTION 'Parameter api_base_path must be / or a path like /api, without a trailing slash'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
END $$;

-- PostgreSQL truncates a longer name on CREATE ROLE, so the existence checks
-- would miss the role it made, and two defaults could truncate to one name.
DO $$
DECLARE
    v_too_long text;
BEGIN
    SELECT string_agg(format('%s (%s bytes)', key, octet_length(value)), ', ' ORDER BY key) INTO v_too_long
    FROM pg_temp.deployment_parameter
    WHERE key ~ '^database_[a-z]+_role$'
      AND octet_length(value) > current_setting('max_identifier_length')::int;
    IF v_too_long IS NOT NULL THEN
        RAISE EXCEPTION 'Role name(s) longer than % bytes: %', current_setting('max_identifier_length'), v_too_long
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Use a shorter database name, or pass shorter role names as those parameters.';
    END IF;
END $$;

-- The guards skip what the provider owns, so a pattern that matches the
-- template's own roles would switch the guards off for them.
DO $$
DECLARE
    v_provider text := pg_temp.parameter_value('provider_schemas');
    v_matched  text;
BEGIN
    IF (CASE WHEN pg_input_is_valid(v_provider, 'jsonb')
             THEN jsonb_typeof(v_provider::jsonb) IS DISTINCT FROM 'array'
                  OR jsonb_path_exists(v_provider::jsonb, '$[*] ? (@.type() != "string" || @ == "")')
             ELSE true END) THEN
        RAISE EXCEPTION 'Parameter provider_schemas must be a JSON array of schema names, e.g. ["__db_system"]'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT string_agg(format('%s (%s)', r.key, r.value), ', ' ORDER BY r.key) INTO v_matched
    FROM pg_temp.deployment_parameter r, pg_temp.deployment_parameter p
    WHERE p.key = 'provider_role_pattern' AND p.value <> ''
      AND r.key ~ '^database_[a-z]+_role$' AND r.value LIKE p.value;
    IF v_matched IS NOT NULL THEN
        RAISE EXCEPTION 'Parameter provider_role_pattern matches the template''s own role(s): %', v_matched
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'The guards ignore grants to roles that match it; it must name only the provider''s roles, such as databricks\_%.';
    END IF;
END $$;

-- An upgrade keeps the database's label, so it is never a relabel.
DO $$
DECLARE
    v_upgrade text := pg_temp.parameter_value('upgrade');
    v_relabel text := pg_temp.parameter_value('relabel_environment');
BEGIN
    IF v_upgrade NOT IN ('true', 'false') THEN
        RAISE EXCEPTION 'Parameter upgrade must be true or false, got %', quote_literal(v_upgrade)
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF v_upgrade = 'true' AND v_relabel IS DISTINCT FROM '' THEN
        RAISE EXCEPTION 'upgrade=true and relabel_environment=% together: an upgrade never changes the environment label', v_relabel
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
END $$;

-- ############################################################################
-- ENVIRONMENT - the label a database keeps, and the dev password default
-- ############################################################################
-- The environment the database was last deployed as: NULL for one never
-- deployed, '(none)' for one whose label row is gone.
CREATE FUNCTION pg_temp.environment_label()
RETURNS text LANGUAGE plpgsql STABLE AS $$
BEGIN
    IF to_regclass('internal.environment') IS NULL THEN
        RETURN NULL;
    END IF;
    RETURN COALESCE((SELECT label FROM internal.environment), '(none)');
END;
$$;

-- A redeploy must name the environment the database was deployed as. This
-- runs before any role is touched: env=dev against a prod database would
-- otherwise reset its role passwords to the dev default.
DO $$
DECLARE
    v_env     text := pg_temp.parameter_value('env');
    v_relabel text := pg_temp.parameter_value('relabel_environment');
    v_label   text := pg_temp.environment_label();
BEGIN
    IF v_label IS NULL THEN
        IF pg_temp.parameter_value('upgrade') = 'true' THEN
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
    END IF;
END $$;

-- The dev password default is a local convenience, for a new database or one
-- already labelled dev. It reads the label before the relabel below: a
-- database relabelled to dev needs explicit passwords. Lakebase refuses a weak
-- password when the transaction commits, so there it is a generated one.
DO $$
DECLARE
    v_defaulted int;
BEGIN
    IF pg_temp.parameter_value('env') = 'dev' AND COALESCE(pg_temp.environment_label(), 'dev') = 'dev' THEN
        UPDATE pg_temp.deployment_parameter
        SET value = CASE WHEN pg_temp.on_lakebase()
                         THEN replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', '')
                         ELSE 'postgres' END
        WHERE value IS NULL AND key ~ 'password$';
        GET DIAGNOSTICS v_defaulted = ROW_COUNT;
        IF v_defaulted > 0 AND pg_temp.on_lakebase() THEN
            RAISE WARNING 'pgmi: env=dev on Databricks Lakebase, which refuses a weak password: role password(s) set to a generated value. Set PGMI_PARAM_DATABASE_ADMIN_PASSWORD to choose one.';
        ELSIF v_defaulted > 0 THEN
            RAISE WARNING 'pgmi: env=dev — role password(s) defaulted to ''postgres''. This is for local/disposable databases only. For any shared deploy use env=test or env=prod and set each *_password through PGMI_PARAM_<NAME> or --params-file -, never --param.';
        END IF;
    END IF;
END $$;

-- The label check let a labelled database with another label through only
-- when relabel_environment names env: the label changes on purpose.
DO $$
DECLARE
    v_env   text := pg_temp.parameter_value('env');
    v_label text := pg_temp.environment_label();
BEGIN
    IF v_label IS NOT NULL AND v_label <> v_env THEN
        INSERT INTO internal.environment (label) VALUES (v_env)
        ON CONFLICT (singleton) DO UPDATE SET label = excluded.label, labelled_at = now();
        RAISE WARNING 'pgmi: database % relabelled % -> %. A copy of a production database holds real data: scrub it before using it as dev.',
            current_database(), v_label, v_env;
    END IF;
END $$;

-- ############################################################################
-- PUBLISH - every value complete, then set as pgmi.<key>
-- ############################################################################
-- Each missing value is named with its PGMI_PARAM_ variable: it is usually a
-- secret the CI did not pass.
DO $$
DECLARE
    v_missing     text;
    v_missing_env text;
BEGIN
    SELECT string_agg(original_key, ', ' ORDER BY original_key) FILTER (WHERE value IS NULL),
           string_agg('PGMI_PARAM_' || upper(original_key), ', ' ORDER BY original_key) FILTER (WHERE value IS NULL)
    INTO v_missing, v_missing_env
    FROM pg_temp.deployment_parameter;

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'Missing required parameters: %. Set % (a CI secret) or use --params-file -; never --param for a secret.', v_missing, v_missing_env
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
END $$;

-- Every value becomes pgmi.<key>, read through pg_temp.deployment_setting; the
-- log lists them with secrets masked.
DO $$
DECLARE
    v_param pg_temp.deployment_parameter;
BEGIN
    FOR v_param IN SELECT * FROM pg_temp.deployment_parameter ORDER BY key LOOP
        PERFORM set_config('pgmi.' || v_param.key, v_param.value, false);
        RAISE NOTICE '[pgmi] Parameter: % = %',
            v_param.original_key,
            CASE WHEN lower(v_param.key) ~ '(password|secret|token|key|credential|auth)'
                 THEN '********' ELSE v_param.value END;
    END LOOP;
END $$;

CREATE FUNCTION pg_temp.deployment_setting(p_key text, p_required boolean DEFAULT true)
RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    v_normalized TEXT := 'pgmi.' || pg_temp.parameter_key(p_key);
    v_value TEXT;
BEGIN
    v_value := current_setting(v_normalized, true);
    IF v_value IS NULL AND p_required THEN
        RAISE EXCEPTION 'Required deployment parameter "%" not set. Declare it in deploy/parameters.xml and set PGMI_PARAM_%.', p_key, upper(p_key)
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    RETURN v_value;
END;
$$;
