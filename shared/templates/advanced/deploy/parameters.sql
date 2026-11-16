-- ============================================================================
-- deploy/parameters.sql
-- The parameters, declared once in pg_temp.parameter_declaration(), their
-- checks and the environment label check. Ends with each value set as
-- pgmi.<key>, read through pg_temp.deployment_setting. Writes nothing to the
-- database.
-- ============================================================================

-- ############################################################################
-- DECLARATION - the one place a parameter is named
-- ############################################################################
-- Every parameter the deploy takes, one row each: its default (NULL means
-- required; a blank value counts as not given), the form a value must have
-- with the message when it does not, and whether the log masks it. env has no
-- default: a deploy that does not say which environment it targets must not be
-- treated as dev. Values come from pgmi.yaml params, PGMI_PARAM_NAME
-- variables, params files and the command line; pass a password as
-- PGMI_PARAM_NAME (a CI secret) or in a params file read from stdin, never on
-- the command line.
CREATE FUNCTION pg_temp.parameter_declaration()
RETURNS TABLE (key text, default_value text, pattern text, message text, description text, is_secret boolean)
LANGUAGE sql STABLE AS $declaration$
    VALUES
        ('database_owner_role', current_database() || '_owner', NULL, NULL,
         'Database owner role (NOLOGIN)',
         false),
        ('database_admin_role', current_database() || '_admin', NULL, NULL,
         'Database admin role (LOGIN, full access)',
         false),
        ('database_admin_password', NULL, NULL, NULL,
         'Database admin password, or a SCRAM verifier of it (SCRAM-SHA-256$...), so the plaintext never reaches the server. Required when the admin role does not exist yet, except with env=dev on a new or dev-labelled database, where it defaults to postgres. Left out once the role exists, the role keeps its password',
         true),
        ('database_api_role', current_database() || '_api', NULL, NULL,
         'API group role (NOLOGIN, permission bundle)',
         false),
        ('database_kernel_role', current_database() || '_kernel', NULL, NULL,
         'Kernel role (NOLOGIN): owns the membership kernels, which work across organizations; no login is ever granted it',
         false),
        ('database_jobs_role', current_database() || '_jobs', NULL, NULL,
         'Background jobs login. The deploy never creates it; when a role of this name exists, every deploy grants it CONNECT, USAGE on schema api and EXECUTE on every api.job_* routine, nothing else. Defaults to <database>_jobs',
         false),
        ('platform_operators', '[]', NULL, NULL,
         'Platform operators as a JSON array: [{"email": "...", "displayName": "...", "issuer": "...", "subject": "..."}], issuer and subject optional. Synced every deploy: a person left out loses the status. Defaults to [] (none)',
         false),
        ('oidc_issuers', '[]', NULL, NULL,
         'OIDC issuers (iss claim URLs) people may sign in with, as a JSON array of strings. Synced every deploy; an issuer still holding logins cannot be dropped. Defaults to [] (none)',
         false),
        ('platform_mfa_required', 'true', '^(true|false)$', 'platform_mfa_required must be true or false',
         'true: platform permissions need a login session whose amr claim includes mfa (dev impersonation sets it). false turns the check off. Defaults to true',
         false),
        ('exchange_retention_days', '30', '^0*[1-9][0-9]{0,4}$', 'exchange_retention_days must be a whole number of days from 1 to 99999',
         'Days the exchange log (api.exchange) keeps a row: the retention of the purge_exchanges job (api.job_purge_exchanges) and the default of POST /admin/maintenance/purge-exchanges. Defaults to 30',
         false),
        ('api_base_path', '/api', '^(/|(/[A-Za-z0-9._~-]+)+)$', 'api_base_path must be / or a path like /api, without a trailing slash',
         'The path the gateway serves REST under, e.g. /api (the reference gateways'' mount). It becomes the OpenAPI servers url, so generated clients and the /docs page call the right paths. / means the root. Defaults to /api',
         false),
        ('www_authenticate', 'Session', '^[^[:cntrl:]]*$', 'www_authenticate must be one header value, without control characters',
         'The challenge a 401 carries in WWW-Authenticate (RFC 9110 15.5.2), e.g. Bearer realm="app". Defaults to Session (the login-session handle)',
         false),
        ('provider_role_pattern', pg_temp.provider_default('provider_role_pattern'), NULL, NULL,
         'Roles a managed PostgreSQL provider grants on its own, as a LIKE pattern (Databricks Lakebase: databricks\_%). Every deploy revokes CREATE from those roles on the template''s schemas and extensions, and TRUNCATE, REFERENCES, TRIGGER and MAINTAIN on its tables. The guards then ignore their USAGE on schemas, their SELECT, INSERT, UPDATE and DELETE on membership tables (row-level security still binds them), and event-trigger functions another role keeps in schema public. Refused when it matches one of the template''s roles. Defaults to databricks\_% on Databricks Lakebase (detected by the role databricks_superuser and the schema __db_system), else empty (none)',
         false),
        ('provider_schemas', pg_temp.provider_default('provider_schemas'), NULL, NULL,
         'Schemas a managed PostgreSQL provider keeps in every database, as a JSON array of names (Databricks Lakebase: ["__db_system"]). The schema guard does not hold them to the approved list. Refused when it names a schema of the template. Defaults to ["__db_system"] on Databricks Lakebase, else [] (none)',
         false),
        ('env', NULL, '^(dev|test|prod)$', 'env must be dev, test or prod',
         'Deployment environment: dev, test or prod. Required, no default. The first deploy labels the database with it (internal.environment) and every redeploy must name the same label. Only env=dev deploys files under a dev/ folder',
         false),
        ('relabel_environment', '', NULL, NULL,
         'Change the database''s environment label to this value, which must equal env. Refused when the database is new or already carries that label, so it cannot stay switched on. Relabelling to dev does not default passwords. Defaults to empty (no relabel)',
         false),
        ('upgrade', 'false', '^(true|false)$', 'upgrade must be true or false',
         'true: refuse the deploy unless the database already carries the label env names, so a pipeline never builds a fresh database by mistake. Cannot be combined with relabel_environment. Defaults to false',
         false)
$declaration$;

-- ############################################################################
-- VALUES - each declared parameter with its given value or its default
-- ############################################################################
CREATE TEMPORARY TABLE deployment_parameter (
    key TEXT PRIMARY KEY,
    original_key TEXT NOT NULL,
    value TEXT,
    is_redundant BOOLEAN NOT NULL DEFAULT FALSE
);

-- --param Database-Admin-Password and the declared database_admin_password
-- name one parameter.
CREATE FUNCTION pg_temp.parameter_key(p_key text)
RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT regexp_replace(lower(p_key), '[^a-z0-9]+', '_', 'g')
$$;

INSERT INTO pg_temp.deployment_parameter (key, original_key, value, is_redundant)
SELECT COALESCE(d.key, given.normalized_key),
       COALESCE(d.key, given.key),
       COALESCE(CASE WHEN btrim(given.value) <> '' THEN given.value END, d.default_value),
       d.key IS NULL
FROM pg_temp.parameter_declaration() d
FULL OUTER JOIN (
    SELECT key, value, pg_temp.parameter_key(key) AS normalized_key
    FROM pg_temp.pgmi_parameter_view
) given ON given.normalized_key = d.key;

-- The value this file settled on, NULL when none was given and none
-- defaulted. Later files read pg_temp.deployment_setting; deploy/bootstrap.sql
-- reads a secret here, because pgmi itself sets pgmi.<key> to whatever the
-- caller passed.
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
        RAISE EXCEPTION 'Unknown parameters: %. These are not declared in pg_temp.parameter_declaration() (deploy/parameters.sql)', v_redundant
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
END $$;

-- A blank secret is one the caller's secret store did not return. Counted as
-- not given, it would keep or default a password the caller meant to set;
-- passed on, it would clear the role's password.
DO $$
DECLARE
    v_blank text;
BEGIN
    SELECT string_agg(d.key, ', ' ORDER BY d.key) INTO v_blank
    FROM pg_temp.parameter_declaration() d
    JOIN pg_temp.pgmi_parameter_view given ON pg_temp.parameter_key(given.key) = d.key
    WHERE d.is_secret AND given.value !~ '[^[:space:]]';

    IF v_blank IS NOT NULL THEN
        RAISE EXCEPTION 'Parameter % is blank', v_blank
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Pass a value, or leave the parameter out: an admin role that exists then keeps its password.';
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
END $$;

-- Each value against the form its declaration gives. A secret is never echoed.
DO $$
DECLARE
    v_bad record;
BEGIN
    FOR v_bad IN
        SELECT d.message, d.is_secret, p.value
        FROM pg_temp.parameter_declaration() d
        JOIN pg_temp.deployment_parameter p USING (key)
        WHERE p.value !~ d.pattern
        ORDER BY d.key
    LOOP
        RAISE EXCEPTION 'Parameter %', v_bad.message || CASE WHEN v_bad.is_secret THEN '' ELSE ', got ' || quote_literal(v_bad.value) END
            USING ERRCODE = 'invalid_parameter_value';
    END LOOP;
END $$;

DO $$
DECLARE
    v_env     text := pg_temp.parameter_value('env');
    v_relabel text := pg_temp.parameter_value('relabel_environment');
BEGIN
    IF v_relabel NOT IN ('', v_env) THEN
        RAISE EXCEPTION 'Parameter relabel_environment=% does not name the label env=% asks for', v_relabel, v_env
            USING ERRCODE = 'invalid_parameter_value', HINT = 'To change a database''s label, pass the new label as both env and relabel_environment.';
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
    IF v_upgrade = 'true' AND v_relabel IS DISTINCT FROM '' THEN
        RAISE EXCEPTION 'upgrade=true and relabel_environment=% together: an upgrade never changes the environment label', v_relabel
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
END $$;

-- ############################################################################
-- ENVIRONMENT - the label a database keeps, and the dev password default
-- (the relabel itself is written by deploy/ledger.sql, as the owner)
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

-- The admin password is needed only to create the admin role: once the role
-- exists, a deploy without the parameter keeps its password, so a pipeline
-- holds the long-lived secret only on the first deploy and on a rotation.
CREATE FUNCTION pg_temp.admin_password_kept()
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT pg_temp.parameter_value('database_admin_password') IS NULL
       AND EXISTS (SELECT FROM pg_roles WHERE rolname = pg_temp.parameter_value('database_admin_role'))
$$;

-- The dev password default is a local convenience, for a new database or one
-- already labelled dev, and only for an admin role that does not exist yet. It
-- reads the label before deploy/ledger.sql relabels. Lakebase refuses a weak
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
        WHERE value IS NULL AND key ~ 'password$' AND NOT pg_temp.admin_password_kept();
        GET DIAGNOSTICS v_defaulted = ROW_COUNT;
        IF v_defaulted > 0 AND pg_temp.on_lakebase() THEN
            RAISE WARNING 'pgmi: env=dev on Databricks Lakebase, which refuses a weak password: role password(s) set to a generated value. Set PGMI_PARAM_DATABASE_ADMIN_PASSWORD to choose one.';
        ELSIF v_defaulted > 0 THEN
            RAISE WARNING 'pgmi: env=dev — role password(s) defaulted to ''postgres''. This is for local/disposable databases only. For any shared deploy use env=test or env=prod and set each *_password through PGMI_PARAM_<NAME> or --params-file -, never --param.';
        END IF;
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
    FROM pg_temp.deployment_parameter
    WHERE NOT (key = 'database_admin_password' AND pg_temp.admin_password_kept());

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'Missing required parameters: %. Set % (a CI secret) or use --params-file -; never --param for a secret.', v_missing, v_missing_env
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
END $$;

-- Every value becomes pgmi.<key>, read through pg_temp.deployment_setting; the
-- log lists them, a secret masked.
DO $$
DECLARE
    v_param record;
BEGIN
    FOR v_param IN
        SELECT p.key, p.original_key, p.value, d.is_secret
        FROM pg_temp.deployment_parameter p
        JOIN pg_temp.parameter_declaration() d USING (key)
        WHERE p.value IS NOT NULL
        ORDER BY p.key
    LOOP
        PERFORM set_config('pgmi.' || v_param.key, v_param.value, false);
        RAISE NOTICE '[pgmi] Parameter: % = %', v_param.original_key,
            CASE WHEN v_param.is_secret THEN '********' ELSE v_param.value END;
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
        RAISE EXCEPTION 'Required deployment parameter "%" not set. Declare it in pg_temp.parameter_declaration() in deploy/parameters.sql and set PGMI_PARAM_%.', p_key, upper(p_key)
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    RETURN v_value;
END;
$$;
