-- deploy.sql — apply-once migrations with drift detection.
-- The ledger, the skip and the drift policy are all below; pgmi supplies the
-- files and their checksums, nothing more.

BEGIN;

CREATE SCHEMA IF NOT EXISTS app;
CREATE TABLE IF NOT EXISTS app.migration_log (
    path       text PRIMARY KEY,
    checksum   text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
);

-- Drift policy: an applied migration whose content changed fails the deploy.
-- pgmi_checksum ignores comments, whitespace and keyword case, so
-- reformatting a file is not drift; changing what it does is. Warn instead
-- of failing, or ignore drift entirely, by editing this block.
DO $$
DECLARE
    v_drift text;
BEGIN
    SELECT string_agg(l.path, ', ' ORDER BY l.path)
      INTO v_drift
      FROM app.migration_log l
      JOIN pg_temp.pgmi_source_view s ON s.path = l.path
     WHERE s.pgmi_checksum IS DISTINCT FROM l.checksum;

    IF v_drift IS NOT NULL THEN
        RAISE EXCEPTION 'applied migration(s) changed since they ran: %', v_drift
            USING HINT = 'Revert the edit and add a new migration for the change.';
    END IF;
END $$;

-- Apply what has not run yet, in byte order, and record each one.
DO $$
DECLARE
    v_file record;
    v_applied int := 0;
BEGIN
    FOR v_file IN
        SELECT s.path, s.content, s.pgmi_checksum
          FROM pg_temp.pgmi_source_view s
         WHERE s.directory = './migrations/' AND s.is_sql_file
           AND NOT EXISTS (SELECT 1 FROM app.migration_log l WHERE l.path = s.path)
         ORDER BY s.path COLLATE "C"
    LOOP
        EXECUTE v_file.content;
        INSERT INTO app.migration_log (path, checksum) VALUES (v_file.path, v_file.pgmi_checksum);
        v_applied := v_applied + 1;
    END LOOP;

    RAISE NOTICE 'apply-once: % migration(s) applied, % already recorded',
        v_applied, (SELECT count(*) FROM app.migration_log) - v_applied;
END $$;

COMMIT;
