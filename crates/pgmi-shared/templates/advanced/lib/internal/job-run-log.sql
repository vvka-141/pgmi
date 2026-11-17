/*
<pgmi-meta
    id="5d1c7a52-3f0e-4b8a-9c61-2e4f8b0a7d13"
    idempotent="false">
  <description>
    The background-job run log: one row per run of a job (api.job_run), from
    its start to its finish, or abandoned when its session died. Written only
    by api.job_run_started and api.job_run_finished.
  </description>
  <sortKeys>
    <key>002/001</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE internal.job_run_log (
    object_id     uuid        PRIMARY KEY DEFAULT uuidv7(),
    job_name      text        NOT NULL CONSTRAINT job_run_log_job_name_check CHECK (job_name ~ '^[a-z][a-z0-9_]{0,62}$'),
    database_role name        NOT NULL DEFAULT session_user,
    backend_pid   int         NOT NULL DEFAULT pg_backend_pid(),
    started_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at   timestamptz,
    rows_affected bigint,
    batches       int,
    outcome       text        CONSTRAINT job_run_log_outcome_check CHECK (outcome IN ('succeeded', 'abandoned')),
    CONSTRAINT job_run_log_finished_check CHECK ((finished_at IS NOT NULL) = (outcome IS NOT DISTINCT FROM 'succeeded')),
    CONSTRAINT job_run_log_counts_check CHECK (
        (outcome IS NOT DISTINCT FROM 'succeeded') = (rows_affected IS NOT NULL AND batches IS NOT NULL)
        AND rows_affected >= 0 AND batches >= 0),
    CONSTRAINT job_run_log_order_check CHECK (finished_at >= started_at)
);

COMMENT ON TABLE internal.job_run_log IS
    'One row per background-job run. outcome is NULL while the run is live (or its session died unnoticed), succeeded with its counts, or abandoned when the next run of the job found it unfinished. database_role is the session user that ran it.';

CREATE INDEX ix_job_run_log_live ON internal.job_run_log (job_name) WHERE outcome IS NULL;
CREATE INDEX ix_job_run_log_job_started ON internal.job_run_log (job_name, started_at DESC);

SELECT pg_temp.exempt_from_rls('internal.job_run_log', 'job bookkeeping: no organization''s rows, written by the job-run writers only');
