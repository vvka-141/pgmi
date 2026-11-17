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

-- Tenant rows behind forced row-level security are references too: the sync
-- runs with no organization in context, and still must not cascade into them.
DO $$
DECLARE
    v_msg text;
BEGIN
    CREATE TABLE core.catalog_probe_status (code text PRIMARY KEY);
    CREATE TABLE core.catalog_probe_tenant (
        id              int PRIMARY KEY,
        organization_id uuid NOT NULL,
        status          text REFERENCES core.catalog_probe_status (code) ON DELETE CASCADE,
        previous        text REFERENCES core.catalog_probe_status (code) ON DELETE SET NULL
    );
    INSERT INTO core.catalog_probe_status VALUES ('open'), ('closed'), ('spare');
    INSERT INTO core.catalog_probe_tenant VALUES (1, gen_random_uuid(), 'closed', 'open'), (2, gen_random_uuid(), 'open', 'closed');
    PERFORM core.apply_org_rls('core.catalog_probe_tenant', p_has_created_by => false);

    BEGIN
        PERFORM pg_temp.sync_catalog('core.catalog_probe_status', '[{"code": "open"}]');
    EXCEPTION WHEN foreign_key_violation THEN
        v_msg := SQLERRM;
    END;
    IF coalesce(v_msg, '') NOT LIKE '%(closed), still referenced by core.catalog_probe_tenant%' THEN
        RAISE EXCEPTION 'TEST FAILED: a row referenced only by forced-RLS tenant rows must be refused; got %', v_msg;
    END IF;
    IF (SELECT relforcerowsecurity FROM pg_class WHERE oid = 'core.catalog_probe_tenant'::regclass) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: a refused sync left FORCE ROW LEVEL SECURITY off';
    END IF;

    PERFORM pg_temp.sync_catalog('core.catalog_probe_status', '[{"code": "open"}, {"code": "closed"}]');
    IF EXISTS (SELECT FROM core.catalog_probe_status WHERE code = 'spare')
       OR (SELECT relforcerowsecurity FROM pg_class WHERE oid = 'core.catalog_probe_tenant'::regclass) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: dropping an unreferenced row must succeed and leave FORCE ROW LEVEL SECURITY on';
    END IF;

    INSERT INTO core.catalog_probe_status VALUES ('held');
    CREATE TABLE core.catalog_probe_tenant_plain (
        id              int PRIMARY KEY,
        organization_id uuid NOT NULL,
        status          text REFERENCES core.catalog_probe_status (code)
    );
    INSERT INTO core.catalog_probe_tenant_plain VALUES (1, gen_random_uuid(), 'held');
    PERFORM core.apply_org_rls('core.catalog_probe_tenant_plain', p_has_created_by => false);
    v_msg := NULL;
    BEGIN
        PERFORM pg_temp.sync_catalog('core.catalog_probe_status', '[{"code": "open"}, {"code": "closed"}]');
    EXCEPTION WHEN foreign_key_violation THEN
        v_msg := SQLERRM;
    END;
    IF v_msg IS NULL OR NOT EXISTS (SELECT FROM core.catalog_probe_status WHERE code = 'held') THEN
        RAISE EXCEPTION 'TEST FAILED: a NO ACTION reference from a forced-RLS table must refuse the sync';
    END IF;
END $$;
