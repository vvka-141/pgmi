/*
<pgmi-meta
    id="d3f81a64-29c7-4b0e-a5d2-7e6b19c04f58"
    idempotent="true">
  <description>
    Deploy history over the tracking tables deploy.sql keeps:
    internal.vw_deployment (one row per committed deploy) and
    internal.vw_deployed_script (one row per script that ever ran here).
    Owner-only, like the rest of internal.
  </description>
  <sortKeys>
    <key>003/004</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT core.ensure_view('internal.vw_deployment', $view$
CREATE OR REPLACE VIEW internal.vw_deployment
WITH (security_invoker = true) AS
WITH execution AS (
    SELECT l.xact_id, l.idempotent,
           lag(l.deployment_script_content_checksum) OVER w <> l.deployment_script_content_checksum AS changed,
           lag(l.id) OVER w IS NULL AS new
    FROM internal.deployment_script_execution_log l
    WINDOW w AS (PARTITION BY l.deployment_script_object_id ORDER BY l.id)
)
SELECT d.xact_id,
       d.deployed_at,
       d.deployed_by,
       d.environment,
       count(e.xact_id) AS scripts_run,
       count(e.xact_id) FILTER (WHERE NOT e.idempotent) AS run_once_scripts_run,
       count(e.xact_id) FILTER (WHERE e.new) AS new_scripts,
       count(e.xact_id) FILTER (WHERE e.changed) AS changed_scripts
FROM internal.deployment d
LEFT JOIN execution e ON e.xact_id = d.xact_id
GROUP BY d.xact_id
$view$);

COMMENT ON VIEW internal.vw_deployment IS
    'Deploy history: one row per committed deploy. A failed deploy rolls back entirely and leaves no row.';
COMMENT ON COLUMN internal.vw_deployment.scripts_run IS
    'Scripts the deploy executed. A run-once script that ran before is skipped and not counted.';
COMMENT ON COLUMN internal.vw_deployment.run_once_scripts_run IS
    'Run-once (idempotent="false") scripts among them: structure that ran for the first time.';
COMMENT ON COLUMN internal.vw_deployment.new_scripts IS
    'Scripts that ran here for the first time.';
COMMENT ON COLUMN internal.vw_deployment.changed_scripts IS
    'Scripts whose content differs from their previous run.';

SELECT core.ensure_view('internal.vw_deployed_script', $view$
CREATE OR REPLACE VIEW internal.vw_deployed_script
WITH (security_invoker = true) AS
SELECT h.object_id,
       l.file_path AS path,
       l.idempotent,
       h.first_run_at,
       h.last_run_at,
       h.run_count,
       h.content_versions,
       l.deployment_script_content_checksum AS checksum,
       l.xact_id AS last_xact_id
FROM (
    SELECT deployment_script_object_id AS object_id,
           min(executed_at) AS first_run_at,
           max(executed_at) AS last_run_at,
           count(*) AS run_count,
           count(DISTINCT deployment_script_content_checksum) AS content_versions,
           max(id) AS last_id
    FROM internal.deployment_script_execution_log
    GROUP BY deployment_script_object_id
) h
JOIN internal.deployment_script_execution_log l ON l.id = h.last_id
$view$);

COMMENT ON VIEW internal.vw_deployed_script IS
    'One row per script that ran here, by its <pgmi-meta> id (or path): its path, contract and checksum at its last run, when it first and last ran, how often, and with how many distinct contents.';
COMMENT ON COLUMN internal.vw_deployed_script.idempotent IS
    'false: a run-once script, skipped by every later deploy.';
COMMENT ON COLUMN internal.vw_deployed_script.checksum IS
    'SHA-256 of the content at its last run; the key of internal.deployment_script_content.';
COMMENT ON COLUMN internal.vw_deployed_script.last_xact_id IS
    'The deploy that last ran it (internal.vw_deployment.xact_id).';
