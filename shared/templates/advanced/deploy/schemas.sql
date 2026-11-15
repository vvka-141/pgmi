-- ============================================================================
-- deploy/schemas.sql
-- Runs as the owner: the application schemas and their grants, and the
-- deploy's own tables in internal: the run ledger, the RLS and foreign-key
-- exemptions, and the policy fingerprints, with the two helpers project files
-- call to fill the exemptions: pg_temp.exempt_from_rls and
-- pg_temp.exempt_from_fk_index.
-- ============================================================================

DO $schemas$
DECLARE
    v_schema record;
    v_grant  record;
BEGIN
    FOR v_schema IN SELECT * FROM pg_temp.template_schema() LOOP
        IF to_regnamespace(quote_ident(v_schema.name)) IS NULL THEN
            EXECUTE format('CREATE SCHEMA %I', v_schema.name);
        END IF;
        EXECUTE format('COMMENT ON SCHEMA %I IS %L', v_schema.name, v_schema.comment);
        EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC', v_schema.name);
    END LOOP;

    -- The owner role's half of closing public (deploy/bootstrap.sql explains).
    IF has_schema_privilege('public', 'USAGE WITH GRANT OPTION') THEN
        REVOKE ALL ON SCHEMA public FROM PUBLIC;
    END IF;

    -- Grant only: a revoke here would remove access behind the back of the
    -- lost-access check (deploy/finalize.sql).
    FOR v_grant IN
        SELECT g.* FROM pg_temp.approved_schema_grant() g
        JOIN pg_temp.template_schema() s ON s.name = g.schema_name
        WHERE g.role_key <> 'database_jobs_role'
    LOOP
        EXECUTE format('GRANT %s ON SCHEMA %I TO %I', v_grant.privilege, v_grant.schema_name,
                       pg_temp.deployment_setting(v_grant.role_key));
    END LOOP;

    FOR v_grant IN SELECT * FROM pg_temp.approved_default_privilege() LOOP
        EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT %s ON ROUTINES TO %I', v_grant.schema_name,
                       v_grant.privilege, pg_temp.deployment_setting(v_grant.role_key));
    END LOOP;

    PERFORM pg_temp.revoke_provider_create(ARRAY(SELECT name FROM pg_temp.template_schema()));  -- deploy/providers.sql

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
-- RLS AND FOREIGN-KEY INDEX EXEMPTIONS, THE POLICY RECORD
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
