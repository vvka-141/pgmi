/*
<pgmi-meta
    id="5141de91-2c73-44b8-bcee-3de73cf5b175"
    idempotent="false">
  <description>
    Change tracking, opt in per table: core.change_event holds one row per
    create, update, archive, restore and delete of a tracked row, and
    core.change_tracking lists the tables core.apply_change_tracking has
    attached the trigger to. Applied to no table here.
  </description>
  <sortKeys>
    <key>005/000/100</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE TABLE core.change_event (
    id                  bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id     uuid        NOT NULL,
    relation            text        NOT NULL,
    object_id           uuid        NOT NULL,
    action              text        NOT NULL CONSTRAINT change_event_action_check
                                    CHECK (action IN ('create', 'update', 'archive', 'restore', 'delete')),
    changes             jsonb       NOT NULL,
    principal_id        uuid,
    actual_principal_id uuid,
    xact_id             xid8        NOT NULL DEFAULT pg_current_xact_id(),
    occurred_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ix_change_event_object ON core.change_event (organization_id, relation, object_id, id);

COMMENT ON TABLE core.change_event IS
    'One row per change of a row in a tracked table (core.apply_change_tracking): create and delete carry the row, update the columns that changed with their new values; archive and restore are deleted_at being set and cleared. Housekeeping columns (created_at, updated_at, deleted_at) are left out. Rows are never updated or deleted. A kernel role writing a tracked table needs SELECT pg_temp.kernel_passes(''core.change_event'', ''{insert}'') in a run-once file.';
COMMENT ON COLUMN core.change_event.relation IS 'The tracked table, schema-qualified, as it was named when the change happened.';
COMMENT ON COLUMN core.change_event.changes IS 'create, delete: the row; update, archive, restore: each changed column and its new value.';
COMMENT ON COLUMN core.change_event.principal_id IS 'api.current_principal_id() of the change; NULL for the deploy and other contexts naming no one.';

ALTER TABLE core.change_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE core.change_event FORCE ROW LEVEL SECURITY;

CREATE POLICY change_event_select ON core.change_event FOR SELECT
    USING (organization_id = (SELECT api.current_organization_id()));
CREATE POLICY change_event_insert ON core.change_event FOR INSERT
    WITH CHECK (organization_id = (SELECT api.current_organization_id())
                OR (SELECT api.current_organization_id()) IS NULL);

CREATE TABLE core.change_tracking (
    relation   regclass    PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE core.change_tracking IS
    'The tables core.apply_change_tracking attached the change trigger to. lib/__test__/test_change_tracking.sql fails when one of them lost its trigger, or a table has the trigger without being listed.';

SELECT pg_temp.exempt_from_rls('core.change_tracking', 'lists tracked tables; holds no organization''s rows');
