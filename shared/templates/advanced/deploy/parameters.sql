-- ============================================================================
-- deploy/parameters.sql
-- Parameters declared in session.xml, their defaults, their checks and the
-- environment label. Ends with each value set as deployment.<key>, read
-- through pg_temp.deployment_setting.
-- ============================================================================

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
    -- needs explicit passwords. Lakebase refuses a weak password when the
    -- transaction commits, so there it is a generated one.
    IF v_env = 'dev' AND COALESCE(v_label, 'dev') = 'dev' THEN
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
