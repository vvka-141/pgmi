/*
<pgmi-meta
    id="a7f02000-0004-4000-8000-000000000012"
    idempotent="true">
  <description>
    Maintenance: background jobs and exchange retention. A job is a procedure
    api.job_name(p_batch_size, p_max_batches) that CALLs the generic driver
    api.job_run with its batch function, a function (int) RETURNS int that
    does one batch and returns the rows it processed (0 when done). The
    driver logs the run (api.job_run_started, api.job_run_finished) and
    commits after the start and after every batch, so a worker CALLs a job at
    top level, outside a transaction block. Exchange retention:
    api.exchange_retention() from the deploy parameter exchange_retention_days,
    one batch purge of the three exchange tables (api.purge_exchanges_batch)
    shared by the admin endpoint and the purge_exchanges job. Their btree
    indexes on enqueued_at live in 06-queue-infrastructure.sql.
  </description>
  <sortKeys>
    <key>004/012</key>
  </sortKeys>
</pgmi-meta>
*/

-- ============================================================================
-- Background jobs
-- ============================================================================

CREATE OR REPLACE FUNCTION api.job_lock_key(p_job_name text)
RETURNS int[] LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT ARRAY[hashtext('api.job_run'), hashtext(p_job_name)];
$$;

COMMENT ON FUNCTION api.job_lock_key(text) IS
    'The two keys of a job''s session-level advisory lock: pg_try_advisory_lock(k[1], k[2]).';

-- Takes the job's session-level advisory lock (a transaction-level one would
-- end at the driver's first COMMIT) and opens a run. NULL when another session
-- holds the lock. Holding it, no earlier unfinished run of the job is live:
-- their sessions died, so they are marked abandoned.
CREATE OR REPLACE FUNCTION api.job_run_started(p_job_name text)
RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, common, pg_temp
AS $$
DECLARE
    v_key int[] := api.job_lock_key(p_job_name);
    v_run uuid;
BEGIN
    IF p_job_name IS NULL OR p_job_name !~ '^[a-z][a-z0-9_]{0,62}$' THEN
        RAISE EXCEPTION 'job name must match ^[a-z][a-z0-9_]{0,62}$, got %', p_job_name
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    -- Session-level advisory locks stack, and job_run_finished releases one:
    -- a session whose earlier run failed keeps its lock, and taking it again
    -- would leave it held after this run finishes, skipping the job for good.
    IF NOT EXISTS (SELECT FROM pg_locks l
                   WHERE l.locktype = 'advisory' AND l.pid = pg_backend_pid() AND l.objsubid = 2
                     AND l.classid::int4 = v_key[1] AND l.objid::int4 = v_key[2])
       AND NOT pg_try_advisory_lock(v_key[1], v_key[2]) THEN
        RETURN NULL;
    END IF;
    UPDATE internal.job_run_log SET outcome = 'abandoned'
    WHERE job_name = p_job_name AND outcome IS NULL;
    INSERT INTO internal.job_run_log (job_name) VALUES (p_job_name)
    RETURNING object_id INTO v_run;
    RETURN v_run;
END $$;

COMMENT ON FUNCTION api.job_run_started(text) IS
    'Opens a run of a job and takes its session-level advisory lock, once per session; NULL when another session holds the lock. Marks the job''s unfinished runs abandoned. 22023 for a malformed job name.';

-- Closes a live run this session started, then releases the job's lock.
CREATE OR REPLACE FUNCTION api.job_run_finished(p_run_object_id uuid, p_rows_affected bigint, p_batches int)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, common, pg_temp
AS $$
DECLARE
    v_job text;
    v_key int[];
BEGIN
    UPDATE internal.job_run_log
    SET finished_at = clock_timestamp(), outcome = 'succeeded',
        rows_affected = p_rows_affected, batches = p_batches
    WHERE object_id = p_run_object_id AND outcome IS NULL AND backend_pid = pg_backend_pid()
    RETURNING job_name INTO v_job;
    IF v_job IS NULL THEN
        RAISE EXCEPTION 'run % is not a live run of this session', p_run_object_id
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
    v_key := api.job_lock_key(v_job);
    PERFORM pg_advisory_unlock(v_key[1], v_key[2]);
END $$;

COMMENT ON FUNCTION api.job_run_finished(uuid, bigint, int) IS
    'Closes a live run started by this session as succeeded with its counts and releases the job''s advisory lock. 55000 for any other run.';

-- SECURITY INVOKER with no SET clause: either would forbid the COMMITs. So
-- every name is schema-qualified. A failed run keeps its session's lock and
-- leaves its row live: other sessions skip the job until that session ends,
-- and the next run marks the row abandoned.
CREATE OR REPLACE PROCEDURE api.job_run(
    p_job_name    text,
    p_batch       regprocedure,
    p_batch_size  int DEFAULT 5000,
    p_max_batches int DEFAULT NULL)
LANGUAGE plpgsql
AS $$
DECLARE
    v_call    text;
    v_run     uuid;
    v_rows    bigint := 0;
    v_batches int := 0;
    v_n       int;
BEGIN
    IF p_batch_size IS NULL OR p_batch_size < 1 THEN
        RAISE EXCEPTION 'batch size must be positive, got %', p_batch_size
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_max_batches < 1 THEN
        RAISE EXCEPTION 'max batches must be positive or NULL (no limit), got %', p_max_batches
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT pg_catalog.format('SELECT %I.%I($1)', n.nspname, p.proname) INTO v_call
    FROM pg_catalog.pg_proc p
    JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
    WHERE p.oid = p_batch AND p.prokind = 'f' AND NOT p.proretset
      AND p.prorettype = 'pg_catalog.int4'::pg_catalog.regtype
      AND p.pronargs = 1 AND p.proargtypes[0] = 'pg_catalog.int4'::pg_catalog.regtype;
    IF v_call IS NULL THEN
        RAISE EXCEPTION 'batch function must be a function (int) RETURNS int, got %', p_batch
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    -- Inside a transaction block this refuses (2D000) before the job's
    -- session lock exists: a rolled-back caller would otherwise keep it.
    COMMIT;
    v_run := api.job_run_started(p_job_name);
    IF v_run IS NULL THEN
        RAISE NOTICE 'job % skipped: another session holds its lock', p_job_name;
        RETURN;
    END IF;
    COMMIT;

    LOOP
        EXIT WHEN v_batches >= p_max_batches;
        EXECUTE v_call INTO v_n USING p_batch_size;
        IF v_n IS NULL OR v_n < 0 THEN
            RAISE EXCEPTION 'batch function % returned %, not a row count', p_batch, v_n
                USING ERRCODE = 'data_exception';
        END IF;
        v_rows := v_rows + v_n;
        v_batches := v_batches + 1;
        COMMIT;
        EXIT WHEN v_n = 0;
    END LOOP;

    PERFORM api.job_run_finished(v_run, v_rows, v_batches);
    COMMIT;
END $$;

COMMENT ON PROCEDURE api.job_run(text, regprocedure, int, int) IS
    'Runs a job: takes its advisory lock (raises a NOTICE and returns when another session holds it), logs the run, calls the batch function (int) RETURNS int until it returns 0 or p_max_batches batches ran, committing after each. CALL it at top level, outside a transaction block: inside one it is refused (2D000) before the lock. 22023 for a bad batch size, max batches, job name or batch function, refused before the lock.';

-- ============================================================================
-- Exchange retention
-- ============================================================================

DO $$
BEGIN
    EXECUTE format($fn$
        CREATE OR REPLACE FUNCTION api.exchange_retention()
        RETURNS interval LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $body$
            SELECT make_interval(days => %s);
        $body$
    $fn$, pg_temp.deployment_setting('exchange_retention_days')::int);
END $$;

COMMENT ON FUNCTION api.exchange_retention() IS
    'How long the exchange log keeps a row: the deploy parameter exchange_retention_days.';

-- Arguments are checked with IF ... RAISE, not ASSERT: plpgsql.check_asserts
-- = off would skip an ASSERT and let a NULL cutoff purge nothing or everything.
CREATE OR REPLACE FUNCTION api.purge_exchanges_batch(p_cutoff timestamptz, p_batch_size int)
RETURNS TABLE (rest_deleted int, rpc_deleted int, mcp_deleted int)
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_cutoff IS NULL OR p_cutoff > now() THEN
        RAISE EXCEPTION 'cutoff must be a moment in the past, got %', p_cutoff
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size NOT BETWEEN 1 AND 100000 THEN
        RAISE EXCEPTION 'batch_size must be 1 to 100000, got %', p_batch_size
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    -- EXTENSION POINT: archive before delete, e.g.
    --   INSERT INTO archive.rest_exchange SELECT * FROM api.rest_exchange WHERE enqueued_at < p_cutoff LIMIT p_batch_size;
    DELETE FROM api.rest_exchange
    WHERE ctid = ANY (ARRAY(SELECT ctid FROM api.rest_exchange WHERE enqueued_at < p_cutoff LIMIT p_batch_size));
    GET DIAGNOSTICS rest_deleted = ROW_COUNT;

    DELETE FROM api.rpc_exchange
    WHERE ctid = ANY (ARRAY(SELECT ctid FROM api.rpc_exchange WHERE enqueued_at < p_cutoff LIMIT p_batch_size));
    GET DIAGNOSTICS rpc_deleted = ROW_COUNT;

    DELETE FROM api.mcp_exchange
    WHERE ctid = ANY (ARRAY(SELECT ctid FROM api.mcp_exchange WHERE enqueued_at < p_cutoff LIMIT p_batch_size));
    GET DIAGNOSTICS mcp_deleted = ROW_COUNT;

    RETURN NEXT;
END;
$$;

COMMENT ON FUNCTION api.purge_exchanges_batch(timestamptz, int) IS
    'Deletes up to p_batch_size exchanges older than p_cutoff from each protocol table; returns the counts. 22023 for a NULL or future cutoff or a batch size outside 1..100000.';

-- One batch of the purge_exchanges job: up to p_batch_size exchanges past
-- the retention window from each protocol table.
CREATE OR REPLACE FUNCTION api.job_purge_exchanges_batch(p_batch_size int)
RETURNS int
LANGUAGE sql
SECURITY DEFINER
SET search_path = api, common, pg_temp
AS $$
    SELECT b.rest_deleted + b.rpc_deleted + b.mcp_deleted
    FROM api.purge_exchanges_batch(now() - api.exchange_retention(), p_batch_size) b;
$$;

COMMENT ON FUNCTION api.job_purge_exchanges_batch(int) IS
    'One batch of the purge_exchanges job: deletes up to p_batch_size exchanges older than api.exchange_retention() from each protocol table; returns the rows deleted, 0 when none are left.';

CREATE OR REPLACE PROCEDURE api.job_purge_exchanges(p_batch_size int DEFAULT 5000, p_max_batches int DEFAULT NULL)
LANGUAGE plpgsql
AS $$
BEGIN
    CALL api.job_run('purge_exchanges', 'api.job_purge_exchanges_batch(int)', p_batch_size, p_max_batches);
END $$;

COMMENT ON PROCEDURE api.job_purge_exchanges(int, int) IS
    'The exchange retention job: purges the exchange log past api.exchange_retention() in committed batches through api.job_run. CALL it at top level, outside a transaction block.';
