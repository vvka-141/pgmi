/*
<pgmi-meta
    id="3fe61892-860d-4063-b5cf-b114c68b4cb7"
    idempotent="true">
  <description>
    Change tracking: the trigger function core.capture_change and
    core.apply_change_tracking(table), which attaches it.
  </description>
  <sortKeys>
    <key>005/000/101</key>
  </sortKeys>
</pgmi-meta>
*/

-- An update that changes nothing but housekeeping columns writes no event; one
-- that sets deleted_at is an archive, one that clears it a restore.
CREATE OR REPLACE FUNCTION core.capture_change()
RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_skip    text[] := ARRAY['created_at', 'updated_at', 'deleted_at'];
    v_old     jsonb;
    v_new     jsonb;
    v_row     jsonb;
    v_action  text;
    v_changes jsonb;
BEGIN
    v_old := CASE WHEN TG_OP <> 'INSERT' THEN to_jsonb(OLD) END;
    v_new := CASE WHEN TG_OP <> 'DELETE' THEN to_jsonb(NEW) END;
    v_row := COALESCE(v_new, v_old);

    IF TG_OP = 'UPDATE' THEN
        SELECT COALESCE(jsonb_object_agg(n.key, n.value), '{}') INTO v_changes
        FROM jsonb_each(v_new) n
        WHERE n.key <> ALL (v_skip) AND n.value IS DISTINCT FROM v_old -> n.key;
        v_action := CASE
            WHEN v_old ->> 'deleted_at' IS NULL AND v_new ->> 'deleted_at' IS NOT NULL THEN 'archive'
            WHEN v_old ->> 'deleted_at' IS NOT NULL AND v_new ->> 'deleted_at' IS NULL THEN 'restore'
            WHEN v_changes <> '{}' THEN 'update'
        END;
        IF v_action IS NULL THEN
            RETURN NULL;
        END IF;
    ELSE
        v_action := CASE TG_OP WHEN 'INSERT' THEN 'create' ELSE 'delete' END;
        v_changes := v_row - v_skip;
    END IF;

    INSERT INTO core.change_event (organization_id, relation, object_id, action, changes, principal_id, actual_principal_id, actor_kind)
    VALUES ((v_row ->> 'organization_id')::uuid, format('%I.%I', TG_TABLE_SCHEMA, TG_TABLE_NAME),
            (v_row ->> 'object_id')::uuid, v_action, v_changes,
            api.current_principal_id(), api.current_actual_principal_id(), api.current_actor_kind());
    RETURN NULL;
END $$;

COMMENT ON FUNCTION core.capture_change() IS
    'AFTER ROW trigger of a tracked table: writes the change to core.change_event. Attach it with core.apply_change_tracking.';

-- Attaches the change trigger to a table with object_id and organization_id
-- columns, and lists the table in core.change_tracking. Call it from the
-- run-once file that creates the table.
CREATE OR REPLACE FUNCTION core.apply_change_tracking(p_table regclass)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_missing text;
BEGIN
    SELECT string_agg(c, ', ') INTO v_missing
    FROM unnest(ARRAY['object_id', 'organization_id']) c
    WHERE NOT common.has_column(p_table, c);
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'apply_change_tracking: % has no % column', p_table, v_missing;
    END IF;

    EXECUTE format('CREATE OR REPLACE TRIGGER %I AFTER INSERT OR UPDATE OR DELETE ON %s '
                   'FOR EACH ROW EXECUTE FUNCTION core.capture_change()',
                   'trg_' || (SELECT relname FROM pg_class WHERE oid = p_table) || '_change_tracking', p_table);
    INSERT INTO core.change_tracking (relation) VALUES (p_table) ON CONFLICT (relation) DO NOTHING;
END $$;

COMMENT ON FUNCTION core.apply_change_tracking(regclass) IS
    'Tracks every create, update, archive, restore and delete of the table''s rows in core.change_event. The table needs object_id and organization_id columns.';
