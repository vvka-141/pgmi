-- ============================================================================
-- deploy/ledger.sql
-- The run ledger behind the deploy loop: run-once files that changed after
-- they ran, which files already ran, and what each run records.
-- ============================================================================

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
        SELECT p.object_id, p.path, p.idempotent, p.pgmi_checksum,
               min(p.sort_key COLLATE "C") AS first_sort_key
        FROM pg_temp.deploy_plan p
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

-- Refuses the deploy before its first file when a run-once file changed after
-- it ran on this database.
CREATE FUNCTION pg_temp.refuse_run_once_drift()
RETURNS void LANGUAGE plpgsql AS $fn$
DECLARE
    v_drift text;
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
END $fn$;

-- Skip a run-once file only after it ran as run-once: a re-run file flipped to
-- idempotent="false" must still run once, not be skipped for its earlier
-- re-run log rows.
CREATE FUNCTION pg_temp.ran_once(p_file pg_temp.deploy_plan)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT NOT p_file.idempotent AND EXISTS (
        SELECT FROM internal.deployment_script_execution_log
        WHERE deployment_script_object_id = p_file.object_id AND NOT idempotent)
$$;

-- Before a file runs: its identity and content are registered, a lib/ file is
-- quieted, and the row policies as they stand are returned for after_file.
CREATE FUNCTION pg_temp.before_file(p_file pg_temp.deploy_plan)
RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE
    v_policies jsonb;
BEGIN
    INSERT INTO internal.deployment_script(object_id)
    VALUES (p_file.object_id) ON CONFLICT DO NOTHING;

    INSERT INTO internal.deployment_script_content(checksum, value)
    VALUES (encode(extensions.digest(convert_to(p_file.content, 'UTF8'), 'sha256'), 'hex'), p_file.content)
    ON CONFLICT DO NOTHING;

    SELECT jsonb_object_agg(policy, definition) INTO v_policies FROM pg_temp.policy_definition();
    PERFORM pg_temp.quiet_framework(p_file.path);
    RETURN COALESCE(v_policies, '{}');
END $$;

-- After a file ran: the row policies it changed are recorded and the run is
-- logged in the ledger.
CREATE FUNCTION pg_temp.after_file(p_file pg_temp.deploy_plan, p_policies jsonb)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_temp.unquiet_framework(p_file.path);
    PERFORM pg_temp.record_policies(p_policies, p_file.path);

    INSERT INTO internal.deployment_script_execution_log(
        deployment_script_object_id, deployment_script_content_checksum, pgmi_checksum,
        xact_id, file_path, idempotent, sort_key
    ) VALUES (
        p_file.object_id, encode(extensions.digest(convert_to(p_file.content, 'UTF8'), 'sha256'), 'hex'),
        p_file.pgmi_checksum, pg_current_xact_id(), p_file.path, p_file.idempotent, p_file.sort_key
    );
END $$;
