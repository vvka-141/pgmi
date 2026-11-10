-- ============================================================================
-- Test: every row policy is as the deploy's files left it
-- ============================================================================
-- Policies live in run-once files, so no deploy repairs one edited by hand:
-- ALTER POLICY ... USING (true) would stay. The deploy records each policy a
-- file creates, changes or drops (internal.policy_fingerprint); this guard
-- fails on a live policy that differs from the record, one no deploy file
-- created, and a recorded policy that is gone. To change a policy on purpose,
-- ALTER or replace it in a new run-once file. The probes below use their own
-- table: a test that altered a template policy would lock its table for the
-- rest of the deploy.
-- ============================================================================

CREATE FUNCTION pg_temp.policy_drift()
RETURNS TABLE (policy text, problem text)
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(n.policy, f.policy),
           CASE WHEN f.policy IS NULL THEN 'created outside the deploy'
                WHEN n.policy IS NULL THEN format('gone (created by %s)', f.file_path)
                ELSE format('changed since %s ran', f.file_path) END
    FROM pg_temp.policy_definition() n
    FULL JOIN internal.policy_fingerprint f ON f.policy = n.policy
    WHERE n.definition IS DISTINCT FROM f.definition
$$;

DO $$
DECLARE
    v_drift text;
BEGIN
    SELECT string_agg(policy || ': ' || problem, E'\n  ' ORDER BY policy) INTO v_drift FROM pg_temp.policy_drift();
    IF v_drift IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: row policies differ from what the deploy''s files left:\n  %', v_drift
            USING HINT = 'Restore the policy, or change it in a new run-once file (ALTER POLICY, or DROP and CREATE).';
    END IF;

    IF (SELECT count(*) FROM internal.policy_fingerprint) < 1 THEN
        RAISE EXCEPTION 'TEST FAILED: no policy is recorded; the deploy loop no longer records them';
    END IF;

    CREATE TABLE core.policy_drift_probe (id int);
    CREATE POLICY policy_drift_probe_altered ON core.policy_drift_probe FOR SELECT USING (id > 0);
    CREATE POLICY policy_drift_probe_dropped ON core.policy_drift_probe FOR UPDATE USING (id > 0);
    PERFORM pg_temp.record_policies('{}', './core/policy_drift_probe.sql');
    ALTER POLICY policy_drift_probe_altered ON core.policy_drift_probe USING (true);
    DROP POLICY policy_drift_probe_dropped ON core.policy_drift_probe;
    CREATE POLICY policy_drift_probe_added ON core.policy_drift_probe FOR DELETE USING (true);

    SELECT string_agg(policy || ': ' || split_part(problem, ' ', 1), '; ' ORDER BY policy) INTO v_drift FROM pg_temp.policy_drift();
    IF v_drift IS DISTINCT FROM
        'core.policy_drift_probe policy_drift_probe_added: created; '
        'core.policy_drift_probe policy_drift_probe_altered: changed; '
        'core.policy_drift_probe policy_drift_probe_dropped: gone' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name an altered, an unrecorded and a dropped policy, named: %', v_drift;
    END IF;
END $$;
