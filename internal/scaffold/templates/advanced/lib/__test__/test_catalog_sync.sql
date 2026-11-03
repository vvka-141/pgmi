-- pg_temp.sync_catalog: a table made to hold exactly the given rows, matched
-- by primary key; every refusal leaves the table as it was.

DO $$
DECLARE
    v_rows jsonb;
    v_msg  text;
BEGIN
    CREATE TABLE core.catalog_probe (
        code  text PRIMARY KEY,
        label text NOT NULL,
        rank  int NOT NULL DEFAULT 9
    );
    CREATE TABLE core.catalog_probe_use (
        id   int PRIMARY KEY,
        code text REFERENCES core.catalog_probe (code) ON DELETE CASCADE
    );
    INSERT INTO core.catalog_probe VALUES ('kept', 'Kept', 1), ('changed', 'Old', 2), ('gone', 'Gone', 3);

    PERFORM pg_temp.sync_catalog('core.catalog_probe', $json$[
        {"code": "kept",    "label": "Kept"},
        {"code": "changed", "label": "New"},
        {"code": "added",   "label": "Added"}
    ]$json$);

    SELECT jsonb_object_agg(code, jsonb_build_array(label, rank)) INTO v_rows FROM core.catalog_probe;
    IF v_rows IS DISTINCT FROM '{"kept": ["Kept", 1], "changed": ["New", 2], "added": ["Added", 9]}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: sync should insert, update and delete, and leave unnamed columns alone; got %', v_rows;
    END IF;

    BEGIN
        PERFORM pg_temp.sync_catalog('core.catalog_probe', '[{"code": "kept", "label": "Kept", "colour": "red"}]');
    EXCEPTION WHEN raise_exception THEN
        v_msg := SQLERRM;
    END;
    IF coalesce(v_msg, '') NOT LIKE '%has no column colour%' THEN
        RAISE EXCEPTION 'TEST FAILED: an unknown column must be refused, got %', v_msg;
    END IF;

    v_msg := NULL;
    BEGIN
        PERFORM pg_temp.sync_catalog('core.catalog_probe', '[{"code": "kept", "label": "Kept"}, {"label": "No key"}]');
    EXCEPTION WHEN raise_exception THEN
        v_msg := SQLERRM;
    END;
    IF coalesce(v_msg, '') NOT LIKE '%row(s) 2 lack a value for the primary key (code)%' THEN
        RAISE EXCEPTION 'TEST FAILED: a row without its key must be refused, got %', v_msg;
    END IF;

    v_msg := NULL;
    BEGIN
        PERFORM pg_temp.sync_catalog('core.catalog_probe', '[{"code": "kept", "label": "A"}, {"code": "kept", "label": "B"}]');
    EXCEPTION WHEN raise_exception THEN
        v_msg := SQLERRM;
    END;
    IF coalesce(v_msg, '') NOT LIKE '%more than one row with key {"code": "kept"}%' THEN
        RAISE EXCEPTION 'TEST FAILED: two rows with one key must be refused, got %', v_msg;
    END IF;

    v_msg := NULL;
    BEGIN
        PERFORM pg_temp.sync_catalog('core.catalog_probe', '{"code": "kept"}');
    EXCEPTION WHEN raise_exception THEN
        v_msg := SQLERRM;
    END;
    IF coalesce(v_msg, '') NOT LIKE '%expected a JSON array of objects%' THEN
        RAISE EXCEPTION 'TEST FAILED: a JSON object must be refused, got %', v_msg;
    END IF;

    -- ON DELETE CASCADE would let the delete through and take the referencing row with it.
    INSERT INTO core.catalog_probe_use VALUES (1, 'added');
    v_msg := NULL;
    BEGIN
        PERFORM pg_temp.sync_catalog('core.catalog_probe', '[{"code": "kept", "label": "Kept"}]', './data/catalogs/core.catalog_probe.json');
    EXCEPTION WHEN foreign_key_violation THEN
        v_msg := SQLERRM;
    END;
    IF coalesce(v_msg, '') NOT LIKE '%./data/catalogs/core.catalog_probe.json%'
       OR coalesce(v_msg, '') NOT LIKE '%(added), still referenced by core.catalog_probe_use (catalog_probe_use_code_fkey)%' THEN
        RAISE EXCEPTION 'TEST FAILED: deleting a referenced row must be refused, naming the catalog and the reference; got %', v_msg;
    END IF;

    SELECT jsonb_object_agg(code, jsonb_build_array(label, rank)) INTO v_rows FROM core.catalog_probe;
    IF v_rows IS DISTINCT FROM '{"kept": ["Kept", 1], "changed": ["New", 2], "added": ["Added", 9]}'::jsonb
       OR NOT EXISTS (SELECT FROM core.catalog_probe_use) THEN
        RAISE EXCEPTION 'TEST FAILED: a refused sync must change nothing; got %', v_rows;
    END IF;
END $$;
