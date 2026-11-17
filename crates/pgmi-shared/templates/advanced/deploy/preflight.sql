-- ============================================================================
-- deploy/preflight.sql
-- What the server and the database must be before anything changes.
-- ============================================================================

DO $$
BEGIN
    IF current_setting('server_version_num')::int < 180000 THEN
        RAISE EXCEPTION 'This project''s code was written for PostgreSQL 18; this server runs %. pgmi itself works on PostgreSQL 15 and newer: adapt the code to your version, then deploy again.',
            split_part(current_setting('server_version'), ' ', 1)
            USING ERRCODE = 'feature_not_supported';
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
