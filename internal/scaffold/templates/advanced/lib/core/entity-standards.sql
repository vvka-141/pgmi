/*
<pgmi-meta
    id="a7f02000-0002-4000-8000-000000000001"
    idempotent="true">
  <description>
    Entity table standards: DDL event trigger that injects created_at and
    deleted_at columns on tables declaring object_id core.entity_id. Index
    strategy is deliberately left to the schema author — this trigger does not
    create any indexes.
  </description>
  <sortKeys>
    <key>003/002</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing entity table standards'; END $$;

CREATE OR REPLACE FUNCTION core.apply_entity_table_standards(p_table regclass)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, core
AS $$
DECLARE
    v_oid       oid := p_table;
    v_nsp       text;
    v_rel       text;
    v_relkind   "char";
    v_is_part   boolean;
    v_has_col   boolean;
BEGIN
    SELECT n.nspname, c.relname, c.relkind, c.relispartition
    INTO v_nsp, v_rel, v_relkind, v_is_part
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.oid = v_oid;

    IF v_relkind NOT IN ('r', 'p') OR v_is_part THEN
        RETURN;
    END IF;

    IF v_nsp IN ('pg_catalog', 'information_schema', 'pg_temp') OR v_nsp LIKE 'pg_toast%' THEN
        RETURN;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_attribute a
        WHERE a.attrelid = v_oid
          AND a.attname = 'object_id'
          AND a.attnum > 0
          AND NOT a.attisdropped
          AND a.atttypid = 'core.entity_id'::regtype
    ) THEN
        RETURN;
    END IF;

    SELECT EXISTS (
        SELECT 1 FROM pg_attribute a
        WHERE a.attrelid = v_oid AND a.attname = 'created_at'
          AND a.attnum > 0 AND NOT a.attisdropped
    ) INTO v_has_col;

    IF NOT v_has_col THEN
        EXECUTE format(
            'ALTER TABLE %s ADD COLUMN created_at timestamptz NOT NULL DEFAULT now()',
            p_table
        );
        EXECUTE format(
            'COMMENT ON COLUMN %s.created_at IS %L',
            p_table, 'Row creation timestamp (injected by core entity standards trigger)'
        );
    END IF;

    SELECT EXISTS (
        SELECT 1 FROM pg_attribute a
        WHERE a.attrelid = v_oid AND a.attname = 'deleted_at'
          AND a.attnum > 0 AND NOT a.attisdropped
    ) INTO v_has_col;

    IF NOT v_has_col THEN
        EXECUTE format(
            'ALTER TABLE %s ADD COLUMN deleted_at timestamptz',
            p_table
        );
        EXECUTE format(
            'COMMENT ON COLUMN %s.deleted_at IS %L',
            p_table, 'Soft-delete timestamp; NULL while active (injected by core entity standards trigger)'
        );
    END IF;

    RAISE DEBUG 'Entity standards applied to %.%', v_nsp, v_rel;
END;
$$;

COMMENT ON FUNCTION core.apply_entity_table_standards(regclass) IS
    'Injects created_at and deleted_at columns on tables declaring object_id core.entity_id. Does not create indexes — index strategy is the schema author''s decision.';

CREATE OR REPLACE FUNCTION core.entity_table_ddl_hook()
RETURNS event_trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, core
AS $$
DECLARE
    cmd record;
BEGIN
    FOR cmd IN
        SELECT * FROM pg_event_trigger_ddl_commands()
        WHERE object_type = 'table'
    LOOP
        PERFORM core.apply_entity_table_standards(cmd.objid::regclass);
    END LOOP;
END;
$$;

COMMENT ON FUNCTION core.entity_table_ddl_hook() IS
    'Event-trigger function (core_entity_table_standards) that runs core.apply_entity_table_standards on every table created or altered via DDL.';

-- CREATE EVENT TRIGGER requires superuser. Wrap in SET ROLE reset, restore
-- the owner role in all exit paths so a failure here cannot leave the session
-- with elevated privileges.
DO $su$
DECLARE
    v_owner_role text := pg_temp.deployment_setting('database_owner_role');
    v_is_super   boolean;
    v_installed  boolean;
BEGIN
    EXECUTE 'RESET ROLE';

    SELECT rolsuper INTO v_is_super FROM pg_roles WHERE rolname = current_user;
    IF NOT COALESCE(v_is_super, false) THEN
        EXECUTE format('SET ROLE %I', v_owner_role);
        RAISE EXCEPTION 'pgmi advanced template requires a superuser deployment connection; current role % is not superuser', current_user
            USING HINT = 'Connect as a superuser (or a role with CREATEROLE + SUPERUSER) to install the core_entity_table_standards event trigger, or disable entity-standards by removing lib/core/entity-standards.sql from the deployment.';
    END IF;

    EXECUTE 'DROP EVENT TRIGGER IF EXISTS core_entity_table_standards';
    EXECUTE format(
        'CREATE EVENT TRIGGER core_entity_table_standards '
        'ON ddl_command_end '
        'WHEN TAG IN (''CREATE TABLE'', ''ALTER TABLE'') '
        'EXECUTE FUNCTION core.entity_table_ddl_hook()'
    );

    SELECT EXISTS (
        SELECT 1 FROM pg_event_trigger WHERE evtname = 'core_entity_table_standards'
    ) INTO v_installed;
    IF NOT v_installed THEN
        EXECUTE format('SET ROLE %I', v_owner_role);
        RAISE EXCEPTION 'core_entity_table_standards event trigger was not installed despite no error; refusing to continue';
    END IF;

    EXECUTE format('SET ROLE %I', v_owner_role);
EXCEPTION WHEN OTHERS THEN
    EXECUTE format('SET ROLE %I', v_owner_role);
    RAISE;
END $su$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ core.apply_entity_table_standards(regclass) - column injection';
    RAISE NOTICE '  ✓ core_entity_table_standards - DDL event trigger (requires superuser)';
END $$;
