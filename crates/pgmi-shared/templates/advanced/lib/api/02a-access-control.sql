/*
<pgmi-meta
    id="6c0e9b52-3f1d-4a8e-9b27-5d4a1f0c8e31"
    idempotent="false">
  <description>
    Declared access, structure: the permissions a handler declares and the log
    of refused calls. The decision itself is in 02a-access-control-functions.sql.
  </description>
  <sortKeys>
    <key>004/002/001</key>
  </sortKeys>
</pgmi-meta>
*/

-- Any one of a handler's permissions opens it. The vocabulary belongs to the
-- application, which adds the foreign key to its permission table.
CREATE TABLE api.handler_permission (
    handler_object_id uuid NOT NULL REFERENCES api.handler(object_id) ON DELETE CASCADE,
    permission_id     text NOT NULL,
    PRIMARY KEY (handler_object_id, permission_id)
);

CREATE INDEX ix_handler_permission_permission ON api.handler_permission (permission_id);

COMMENT ON TABLE api.handler_permission IS
    'Permissions a handler declared with "permissions": a principal holding any of them may call it. Written by registration; an unknown permission fails the deploy.';

-- Repeats collapse into one row per minute, handler, reason, principal and
-- organization, so a flood of refusals costs one row a minute.
CREATE TABLE api.access_denial (
    object_id                     uuid PRIMARY KEY DEFAULT uuidv7(),
    bucket                        timestamptz NOT NULL,
    handler_object_id             uuid NOT NULL REFERENCES api.handler(object_id) ON DELETE CASCADE,
    reason                        text NOT NULL CONSTRAINT access_denial_reason_check
        CHECK (reason IN ('unauthenticated', 'organization_required', 'permission_missing')),
    actual_principal_object_id    uuid,
    effective_principal_object_id uuid,
    organization_object_id        uuid,
    attempts                      int NOT NULL DEFAULT 1,
    first_at                      timestamptz NOT NULL DEFAULT now(),
    last_at                       timestamptz NOT NULL DEFAULT now(),
    last_method                   text,
    last_path                     text
);

CREATE UNIQUE INDEX uq_access_denial_bucket ON api.access_denial
    (bucket, handler_object_id, reason, actual_principal_object_id, effective_principal_object_id, organization_object_id)
    NULLS NOT DISTINCT;

CREATE INDEX ix_access_denial_bucket ON api.access_denial USING brin (bucket);

COMMENT ON TABLE api.access_denial IS
    'Calls a gateway refused (401/403) before the handler ran: which handler, why, who really called, as whom, in which organization, how often. Collapsed per minute. Retention: api.purge_access_denials().';
COMMENT ON COLUMN api.access_denial.actual_principal_object_id IS
    'The principal that really made the call; differs from effective_principal_object_id when one principal acts as another.';

DO $$
DECLARE
    v_api_role   text := pg_temp.deployment_setting('database_api_role');
    v_admin_role text := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.handler_permission TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.handler_permission, api.access_denial TO %I', v_admin_role);
END $$;

SELECT pg_temp.exempt_from_rls('api.handler_permission', 'the handler registry: framework metadata, written by registration at deploy');
SELECT pg_temp.exempt_from_rls('api.access_denial', 'refusal log: written by the gateways, read through platform.operate handlers');
SELECT pg_temp.exempt_from_fk_index('api.access_denial', '{handler_object_id}', 'handlers are retired with deleted_at, never deleted; the refusal log is read by time');
