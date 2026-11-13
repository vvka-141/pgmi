/*
<pgmi-meta
    id="a7f02000-0002-4000-8000-000000000001"
    idempotent="true">
  <description>
    Entity table standards: a table that declares object_id core.entity_id
    gets created_at and deleted_at columns. deploy/finalize.sql runs the sweep
    after the last file.
  </description>
  <sortKeys>
    <key>003/002</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION pg_temp.entity_standard_tables()
RETURNS SETOF regclass
LANGUAGE sql STABLE
SET search_path = pg_catalog, core, common
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

CREATE OR REPLACE FUNCTION pg_temp.apply_entity_table_standards(p_table regclass)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, core, common
AS $$
DECLARE
    v_column record;
BEGIN
    IF p_table NOT IN (SELECT pg_temp.entity_standard_tables()) THEN
        RETURN;
    END IF;

    FOR v_column IN
        SELECT * FROM (VALUES
            ('created_at', 'timestamptz NOT NULL DEFAULT now()', 'Row creation timestamp (injected by entity standards reconcile)'),
            ('deleted_at', 'timestamptz', 'Soft-delete timestamp; NULL while active (injected by entity standards reconcile)')
        ) AS c(name, definition, comment)
    LOOP
        CONTINUE WHEN common.has_column(p_table, v_column.name);
        EXECUTE format('ALTER TABLE %s ADD COLUMN %I %s', p_table, v_column.name, v_column.definition);
        EXECUTE format('COMMENT ON COLUMN %s.%I IS %L', p_table, v_column.name, v_column.comment);
    END LOOP;

    RAISE DEBUG 'Entity standards applied to %', p_table;
END;
$$;

CREATE OR REPLACE FUNCTION pg_temp.apply_entity_standards_all()
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, core, common
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
    WHERE NOT common.has_column(t.tbl, 'created_at')
       OR NOT common.has_column(t.tbl, 'deleted_at');

    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'Entity standards conformance failure: tables still missing created_at/deleted_at after sweep: %', v_missing;
    END IF;
END;
$$;
