/*
<pgmi-meta
    id="a7f02000-0002-4000-8000-000000000001"
    idempotent="true">
  <description>
    Entity table standards: deploy-time reconcile that injects created_at and
    deleted_at columns on tables declaring object_id core.entity_id. No
    superuser required. Index strategy is left to the schema author.
  </description>
  <sortKeys>
    <key>003/002</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing entity table standards'; END $$;

CREATE OR REPLACE FUNCTION pg_temp.apply_entity_table_standards(p_table regclass)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, core
AS $$
DECLARE
    v_oid       oid := p_table;
    v_nsp       text;
    v_rel       text;
    v_relkind   "char";
    v_is_part   boolean;
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

    IF NOT EXISTS (
        SELECT 1 FROM pg_attribute a
        WHERE a.attrelid = v_oid AND a.attname = 'created_at'
          AND a.attnum > 0 AND NOT a.attisdropped
    ) THEN
        EXECUTE format(
            'ALTER TABLE %s ADD COLUMN created_at timestamptz NOT NULL DEFAULT now()',
            p_table
        );
        EXECUTE format(
            'COMMENT ON COLUMN %s.created_at IS %L',
            p_table, 'Row creation timestamp (injected by entity standards reconcile)'
        );
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_attribute a
        WHERE a.attrelid = v_oid AND a.attname = 'deleted_at'
          AND a.attnum > 0 AND NOT a.attisdropped
    ) THEN
        EXECUTE format(
            'ALTER TABLE %s ADD COLUMN deleted_at timestamptz',
            p_table
        );
        EXECUTE format(
            'COMMENT ON COLUMN %s.deleted_at IS %L',
            p_table, 'Soft-delete timestamp; NULL while active (injected by entity standards reconcile)'
        );
    END IF;

    RAISE DEBUG 'Entity standards applied to %.%', v_nsp, v_rel;
END;
$$;

CREATE OR REPLACE FUNCTION pg_temp.entity_standard_tables()
RETURNS SETOF regclass
LANGUAGE sql STABLE
SET search_path = pg_catalog, core
AS $$
    SELECT c.oid::regclass
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_attribute a ON a.attrelid = c.oid
    WHERE c.relkind IN ('r', 'p')
      AND NOT c.relispartition
      AND a.attname = 'object_id'
      AND a.attnum > 0
      AND NOT a.attisdropped
      AND a.atttypid = 'core.entity_id'::regtype
      AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_temp')
      AND n.nspname NOT LIKE 'pg_toast%';
$$;

CREATE OR REPLACE FUNCTION pg_temp.apply_entity_standards_all()
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, core
AS $$
DECLARE
    v_tbl regclass;
    v_missing text;
BEGIN
    FOR v_tbl IN SELECT tbl FROM pg_temp.entity_standard_tables() AS t(tbl)
    LOOP
        PERFORM pg_temp.apply_entity_table_standards(v_tbl);
    END LOOP;

    SELECT string_agg(t.tbl::text, ', ')
    INTO v_missing
    FROM pg_temp.entity_standard_tables() AS t(tbl)
    WHERE NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = t.tbl AND attname = 'created_at' AND attnum > 0 AND NOT attisdropped)
       OR NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = t.tbl AND attname = 'deleted_at' AND attnum > 0 AND NOT attisdropped);

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'Entity standards conformance failure: tables still missing created_at/deleted_at after sweep: %', v_missing
            USING HINT = 'This should not happen — investigate pg_temp.apply_entity_table_standards().';
    END IF;
END;
$$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ pg_temp.apply_entity_table_standards(regclass) - column injection';
    RAISE NOTICE '  ✓ pg_temp.apply_entity_standards_all() - deploy-end sweep';
END $$;
