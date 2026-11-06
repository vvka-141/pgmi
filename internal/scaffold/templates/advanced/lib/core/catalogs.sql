/*
<pgmi-meta
    id="4e2b7c91-6f0a-4d38-b5e2-1c9a07d3f846"
    idempotent="true">
  <description>
    Catalog sync: reference data kept as files. pg_temp.sync_catalog(table)
    makes the table hold exactly the rows of data/catalogs/schema.table.json
    (a JSON array of objects, one per row, keyed by column name), matched by
    primary key: new rows are inserted, changed rows updated, rows the file no
    longer lists deleted. Call it from a re-run file sorted after the table's
    run-once file.
  </description>
  <sortKeys>
    <key>003/003</key>
  </sortKeys>
</pgmi-meta>
*/

-- Columns no row names are left alone (a new row takes their default). A row
-- that leaves out a column another row names sets it NULL. Refused, before
-- anything changes: a column the table does not have, a row without its
-- primary key, two rows with one key, and deleting a row something still
-- references, whatever the foreign key's ON DELETE action: a catalog edit must
-- not cascade into application data.
CREATE OR REPLACE FUNCTION pg_temp.sync_catalog(p_table regclass, p_rows jsonb, p_source text DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_table   text := (SELECT format('%I.%I', n.nspname, c.relname)
                       FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = p_table);
    v_source  text := coalesce(p_source, v_table);
    v_keys    text[];
    v_cols    text[];
    v_set     text[];
    v_problem text;
    v_fk      record;
    v_refs    text;
    v_changed bigint;
    v_doomed  bigint;
    v_forced  boolean;
BEGIN
    IF (CASE WHEN jsonb_typeof(p_rows) IS DISTINCT FROM 'array' THEN true
             ELSE EXISTS (SELECT FROM jsonb_array_elements(p_rows) r WHERE jsonb_typeof(r) IS DISTINCT FROM 'object') END) THEN
        RAISE EXCEPTION 'Catalog %: expected a JSON array of objects, one per row', v_source;
    END IF;

    SELECT array_agg(a.attname::text ORDER BY k.ord) INTO v_keys
    FROM pg_index i
    CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
    JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
    WHERE i.indrelid = p_table AND i.indisprimary;
    IF v_keys IS NULL THEN
        RAISE EXCEPTION 'Catalog %: table % has no primary key to match rows by', v_source, v_table;
    END IF;

    v_cols := ARRAY(
        SELECT DISTINCT k FROM jsonb_array_elements(p_rows) r, jsonb_object_keys(r) k ORDER BY k);

    SELECT string_agg(c, ', ' ORDER BY c) INTO v_problem
    FROM unnest(v_cols) c
    WHERE NOT EXISTS (
        SELECT FROM pg_attribute a
        WHERE a.attrelid = p_table AND a.attname = c AND a.attnum > 0 AND NOT a.attisdropped);
    IF v_problem IS NOT NULL THEN
        RAISE EXCEPTION 'Catalog %: table % has no column %', v_source, v_table, v_problem;
    END IF;

    SELECT string_agg(r.ord::text, ', ' ORDER BY r.ord) INTO v_problem
    FROM jsonb_array_elements(p_rows) WITH ORDINALITY AS r(row, ord)
    WHERE EXISTS (SELECT FROM unnest(v_keys) k WHERE coalesce(r.row -> k, 'null') = 'null');
    IF v_problem IS NOT NULL THEN
        RAISE EXCEPTION 'Catalog %: row(s) % lack a value for the primary key (%)',
            v_source, v_problem, array_to_string(v_keys, ', ');
    END IF;

    SELECT string_agg(key::text, ', ' ORDER BY key::text) INTO v_problem
    FROM (
        SELECT (SELECT jsonb_object_agg(k, r -> k) FROM unnest(v_keys) k) AS key
        FROM jsonb_array_elements(p_rows) r
        GROUP BY 1 HAVING count(*) > 1
    ) dup;
    IF v_problem IS NOT NULL THEN
        RAISE EXCEPTION 'Catalog %: more than one row with key %', v_source, v_problem;
    END IF;

    -- Lock the rows the sync will delete before checking who references them:
    -- FOR UPDATE conflicts with the FOR KEY SHARE a referencing insert takes,
    -- so one racing the deploy waits and then fails its own key check, and one
    -- already committed is seen below. Without it, a reference committed
    -- between the check and the MERGE would be cascaded or set null.
    EXECUTE format(
        'SELECT FROM %1$s t WHERE NOT EXISTS (SELECT FROM jsonb_populate_recordset(NULL::%1$s, $1) s WHERE %2$s) FOR UPDATE',
        v_table,
        (SELECT string_agg(format('s.%1$I = t.%1$I', k), ' AND ') FROM unnest(v_keys) k))
    USING p_rows;
    GET DIAGNOSTICS v_doomed = ROW_COUNT;

    -- Forced row-level security hides tenant rows from the owner, but not
    -- from an ON DELETE CASCADE, SET NULL or SET DEFAULT action: such a check
    -- runs with FORCE lifted on the referencing table (ACCESS EXCLUSIVE until
    -- commit), and only when the sync deletes something. A NO ACTION or
    -- RESTRICT key needs no lift: its own check sees every row and refuses.
    FOR v_fk IN
        SELECT c.conname, c.conrelid = p_table AS self, c.conrelid, c.confdeltype IN ('c', 'n', 'd') AS acts,
               (SELECT format('%I.%I', n.nspname, r.relname) FROM pg_class r JOIN pg_namespace n ON n.oid = r.relnamespace
                WHERE r.oid = c.conrelid) AS referencing_name,
               (SELECT string_agg(format('r.%I', a.attname), ', ' ORDER BY k.ord)
                FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
                JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum) AS referencing_cols,
               (SELECT string_agg(format('d.%I', a.attname), ', ' ORDER BY k.ord)
                FROM unnest(c.confkey) WITH ORDINALITY AS k(attnum, ord)
                JOIN pg_attribute a ON a.attrelid = c.confrelid AND a.attnum = k.attnum) AS referenced_cols
        FROM pg_constraint c
        WHERE c.contype = 'f' AND c.confrelid = p_table AND v_doomed > 0
        ORDER BY 5, c.conname
    LOOP
        v_forced := v_fk.acts AND (SELECT relforcerowsecurity FROM pg_class WHERE oid = v_fk.conrelid);
        IF v_forced THEN
            EXECUTE format('ALTER TABLE %s NO FORCE ROW LEVEL SECURITY', v_fk.conrelid::regclass);
        END IF;
        EXECUTE format(
            $sql$
            WITH src AS (SELECT * FROM jsonb_populate_recordset(NULL::%1$s, $1)),
            doomed AS (SELECT t.* FROM %1$s t WHERE NOT EXISTS (SELECT FROM src s WHERE %2$s))
            SELECT string_agg(DISTINCT '(' || concat_ws(', ', %3$s) || ')', ', ')
            FROM %4$s r JOIN doomed d ON (%5$s) = (%3$s)
            %6$s
            $sql$,
            v_table,
            (SELECT string_agg(format('s.%1$I = t.%1$I', k), ' AND ') FROM unnest(v_keys) k),
            v_fk.referenced_cols,
            v_fk.referencing_name,
            v_fk.referencing_cols,
            CASE WHEN v_fk.self THEN format('WHERE EXISTS (SELECT FROM src s WHERE %s)',
                (SELECT string_agg(format('s.%1$I = r.%1$I', k), ' AND ') FROM unnest(v_keys) k)) END)
        INTO v_refs
        USING p_rows;
        IF v_forced THEN
            EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', v_fk.conrelid::regclass);
        END IF;

        IF v_refs IS NOT NULL THEN
            v_problem := concat_ws(E'\n  ', v_problem,
                format('%s %s, still referenced by %s (%s)', v_table, v_refs, v_fk.referencing_name, v_fk.conname));
        END IF;
    END LOOP;
    IF v_problem IS NOT NULL THEN
        RAISE EXCEPTION E'Catalog % drops rows that are still in use:\n  %', v_source, v_problem
            USING ERRCODE = 'foreign_key_violation',
                  HINT = 'Keep the rows in the catalog, or first change the rows that reference them in a run-once file sorted before the sync.';
    END IF;

    v_set := ARRAY(SELECT c FROM unnest(v_cols) c WHERE c <> ALL (v_keys));

    EXECUTE format(
        $sql$
        MERGE INTO %1$s AS t
        USING jsonb_populate_recordset(NULL::%1$s, $1) AS s ON %2$s
        %3$s
        WHEN NOT MATCHED BY TARGET THEN %4$s
        WHEN NOT MATCHED BY SOURCE THEN DELETE
        $sql$,
        v_table,
        (SELECT string_agg(format('t.%1$I = s.%1$I', k), ' AND ') FROM unnest(v_keys) k),
        CASE WHEN cardinality(v_set) > 0 THEN format(
            'WHEN MATCHED AND (%s) IS DISTINCT FROM (%s) THEN UPDATE SET %s',
            (SELECT string_agg(format('t.%I', c), ', ') FROM unnest(v_set) c),
            (SELECT string_agg(format('s.%I', c), ', ') FROM unnest(v_set) c),
            (SELECT string_agg(format('%1$I = s.%1$I', c), ', ') FROM unnest(v_set) c)) END,
        CASE WHEN cardinality(v_cols) > 0 THEN format('INSERT (%s) VALUES (%s)',
            (SELECT string_agg(format('%I', c), ', ') FROM unnest(v_cols) c),
            (SELECT string_agg(format('s.%I', c), ', ') FROM unnest(v_cols) c))
        ELSE 'INSERT DEFAULT VALUES' END)
    USING p_rows;
    GET DIAGNOSTICS v_changed = ROW_COUNT;

    IF v_changed > 0 THEN
        RAISE NOTICE '[pgmi] Catalog %: % row(s) inserted, updated or deleted', v_source, v_changed;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION pg_temp.sync_catalog(p_table regclass)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_path    text;
    v_content text;
    v_rows    jsonb;
BEGIN
    SELECT format('./data/catalogs/%s.%s.json', n.nspname, c.relname) INTO v_path
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.oid = p_table;

    SELECT s.content INTO v_content FROM pg_temp.pgmi_source_view s WHERE s.path = v_path;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Catalog file % not found for table %', v_path, p_table::text;
    END IF;

    BEGIN
        v_rows := v_content::jsonb;
    EXCEPTION WHEN invalid_text_representation THEN
        RAISE EXCEPTION 'Catalog %: not valid JSON: %', v_path, SQLERRM;
    END;

    PERFORM pg_temp.sync_catalog(p_table, v_rows, v_path);
    INSERT INTO pg_temp.catalog_synced VALUES (v_path) ON CONFLICT DO NOTHING;
END $$;
