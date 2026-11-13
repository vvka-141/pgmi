-- ============================================================================
-- Test: the background-job contract
-- ============================================================================
-- What runs inside the deploy's transaction: the driver's refusals, which come
-- before it takes any lock, and the run-log writers. The committing loop and
-- two sessions racing for one job are in
-- internal/scaffold/advanced_jobs_integration_test.go.
-- ============================================================================

CREATE FUNCTION pg_temp.job_locks_held()
RETURNS bigint LANGUAGE sql AS $$
    SELECT count(*) FROM pg_locks
    WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND objsubid = 2
      AND classid::int4 = hashtext('api.job_run');
$$;

-- A failing test releases the job lock it may hold, and nothing else: an
-- unlock-all would also drop pgmi's session-level deploy lock.
CREATE FUNCTION pg_temp.release_job_lock(p_job text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_key int[] := api.job_lock_key(p_job);
BEGIN
    WHILE EXISTS (SELECT FROM pg_locks
                  WHERE locktype = 'advisory' AND mode = 'ExclusiveLock' AND pid = pg_backend_pid()
                    AND objsubid = 2 AND classid::int4 = v_key[1] AND objid::int4 = v_key[2]) LOOP
        PERFORM pg_advisory_unlock(v_key[1], v_key[2]);
    END LOOP;
END $$;

DO $$
DECLARE
    v_case  text;
    v_state text;
BEGIN
    RAISE DEBUG '-> Jobs: bad arguments are refused before any lock';
    SET LOCAL plpgsql.check_asserts = off;

    FOREACH v_case IN ARRAY ARRAY[
        $q$CALL api.job_run('probe', 'api.job_purge_exchanges_batch(int)', NULL)$q$,
        $q$CALL api.job_run('probe', 'api.job_purge_exchanges_batch(int)', 0)$q$,
        $q$CALL api.job_run('probe', 'api.job_purge_exchanges_batch(int)', -5)$q$,
        $q$CALL api.job_run('probe', 'api.job_purge_exchanges_batch(int)', 10, 0)$q$,
        $q$CALL api.job_run('probe', 'api.job_purge_exchanges_batch(int)', 10, -1)$q$,
        $q$CALL api.job_run('probe', NULL)$q$,
        $q$CALL api.job_run('probe', 'api.exchange_retention()')$q$,
        $q$CALL api.job_run('probe', 'api.job_lock_key(text)')$q$,
        $q$SELECT api.job_run_started('Bad-Name')$q$,
        $q$SELECT api.job_run_started(NULL)$q$]
    LOOP
        v_state := NULL;
        BEGIN
            EXECUTE v_case;
        EXCEPTION WHEN OTHERS THEN
            v_state := SQLSTATE;
        END;
        IF v_state IS DISTINCT FROM '22023' THEN
            RAISE EXCEPTION 'TEST FAILED: % must raise 22023, got %', v_case, coalesce(v_state, 'success');
        END IF;
        IF pg_temp.job_locks_held() > 0 THEN
            PERFORM pg_temp.release_job_lock('probe');
            RAISE EXCEPTION 'TEST FAILED: % took a job lock before refusing', v_case;
        END IF;
    END LOOP;
END $$;

-- A run starts with the job's lock and marks the job's unfinished runs
-- abandoned; it finishes only in the session that started it.
DO $$
DECLARE
    v_dead  uuid;
    v_other uuid;
    v_run   uuid;
    v_state text;
    v_row   internal.job_run_log;
BEGIN
    RAISE DEBUG '-> Jobs: the run-log writers';

    INSERT INTO internal.job_run_log (job_name, backend_pid) VALUES ('probe', 0) RETURNING object_id INTO v_dead;
    INSERT INTO internal.job_run_log (job_name, backend_pid) VALUES ('other_probe', 0) RETURNING object_id INTO v_other;

    v_run := api.job_run_started('probe');
    IF v_run IS NULL OR pg_temp.job_locks_held() IS DISTINCT FROM 1::bigint THEN
        RAISE EXCEPTION 'TEST FAILED: job_run_started must open a run and hold one lock, got run % and % lock(s)',
            v_run, pg_temp.job_locks_held();
    END IF;
    IF (SELECT outcome FROM internal.job_run_log WHERE object_id = v_dead) IS DISTINCT FROM 'abandoned'
       OR (SELECT outcome FROM internal.job_run_log WHERE object_id = v_other) IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: starting probe must abandon its own unfinished run and no other job''s';
    END IF;
    SELECT * INTO v_row FROM internal.job_run_log WHERE object_id = v_run;
    IF v_row.database_role IS DISTINCT FROM session_user OR v_row.backend_pid IS DISTINCT FROM pg_backend_pid()
       OR v_row.outcome IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a new run must be live and name this session, got %', row_to_json(v_row);
    END IF;

    BEGIN
        PERFORM api.job_run_finished(v_other, 1, 1);
    EXCEPTION WHEN OTHERS THEN
        v_state := SQLSTATE;
    END;
    IF v_state IS DISTINCT FROM '55000' THEN
        PERFORM pg_temp.release_job_lock('probe');
        RAISE EXCEPTION 'TEST FAILED: finishing another session''s run must raise 55000, got %', coalesce(v_state, 'success');
    END IF;

    PERFORM api.job_run_finished(v_run, 42, 3);
    SELECT * INTO v_row FROM internal.job_run_log WHERE object_id = v_run;
    IF v_row.outcome IS DISTINCT FROM 'succeeded' OR v_row.rows_affected IS DISTINCT FROM 42::bigint
       OR v_row.batches IS DISTINCT FROM 3 OR v_row.finished_at IS NULL
       OR pg_temp.job_locks_held() IS DISTINCT FROM 0::bigint THEN
        PERFORM pg_temp.release_job_lock('probe');
        RAISE EXCEPTION 'TEST FAILED: finishing must record the counts and release the lock, got % with % lock(s)',
            row_to_json(v_row), pg_temp.job_locks_held();
    END IF;
END $$;

-- A job CALLed inside a transaction block cannot commit: workers CALL at top
-- level. The driver's first COMMIT refuses it before it takes the job's lock.
DO $$
DECLARE
    v_state text;
BEGIN
    RAISE DEBUG '-> Jobs: a CALL inside a transaction block is refused';
    BEGIN
        CALL api.job_purge_exchanges();
    EXCEPTION WHEN OTHERS THEN
        v_state := SQLSTATE;
    END;
    IF pg_temp.job_locks_held() > 0 THEN
        PERFORM pg_temp.release_job_lock('purge_exchanges');
        RAISE EXCEPTION 'TEST FAILED: a CALL refused inside a transaction block must not keep the job lock';
    END IF;
    IF v_state IS DISTINCT FROM '2D000' THEN
        RAISE EXCEPTION 'TEST FAILED: a job CALLed inside a transaction block must raise 2D000, got %', coalesce(v_state, 'success');
    END IF;
END $$;
