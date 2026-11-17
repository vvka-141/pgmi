-- ============================================================================
-- Test: a foreign key between two organizations' tables carries the organization
-- ============================================================================
-- A key on the id alone accepts a row of another organization. Between two
-- tables that hold an organization's rows (an organization column and forced
-- row-level security), the key pairs the organization columns too:
-- FOREIGN KEY (organization_id, x_id) REFERENCES t (organization_id, object_id).
-- ============================================================================

CREATE FUNCTION pg_temp.tenant_fk_gap()
RETURNS TABLE (constraint_name text, child text, parent text)
LANGUAGE sql STABLE AS $$
    WITH scoped AS (
        SELECT c.oid, a.attnum AS tenant_attnum
        FROM pg_class c
        JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
        JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
                           AND a.attname IN (SELECT pg_temp.tenant_column())
        WHERE c.relkind IN ('r', 'p') AND c.relforcerowsecurity
    )
    SELECT k.conname::text, k.conrelid::regclass::text, k.confrelid::regclass::text
    FROM pg_constraint k
    JOIN scoped ch ON ch.oid = k.conrelid
    JOIN scoped pa ON pa.oid = k.confrelid
    WHERE k.contype = 'f'
      AND NOT EXISTS (SELECT FROM generate_subscripts(k.conkey, 1) i
                      WHERE k.conkey[i] = ch.tenant_attnum AND k.confkey[i] = pa.tenant_attnum)
$$;

DO $$
DECLARE
    v_gap text;
BEGIN
    SELECT string_agg(format('%s (%s -> %s)', constraint_name, child, parent), ', ' ORDER BY constraint_name)
    INTO v_gap FROM pg_temp.tenant_fk_gap();
    IF v_gap IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: foreign key(s) between organizations'' tables without the organization column: %', v_gap
            USING HINT = 'FOREIGN KEY (organization_id, x_id) REFERENCES <table> (organization_id, object_id), with UNIQUE (organization_id, object_id) on the referenced table';
    END IF;

    CREATE TABLE core.tenant_fk_parent_probe (
        object_id uuid PRIMARY KEY, organization_id uuid NOT NULL, UNIQUE (organization_id, object_id));
    PERFORM core.apply_org_rls('core.tenant_fk_parent_probe', p_has_created_by => false);
    CREATE TABLE core.tenant_fk_child_probe (
        object_id uuid PRIMARY KEY, organization_id uuid NOT NULL, parent_id uuid,
        CONSTRAINT tenant_fk_single_probe FOREIGN KEY (parent_id) REFERENCES core.tenant_fk_parent_probe (object_id),
        CONSTRAINT tenant_fk_composite_probe FOREIGN KEY (organization_id, parent_id)
            REFERENCES core.tenant_fk_parent_probe (organization_id, object_id));
    PERFORM core.apply_org_rls('core.tenant_fk_child_probe', p_has_created_by => false);

    SELECT string_agg(constraint_name, ',' ORDER BY constraint_name) INTO v_gap FROM pg_temp.tenant_fk_gap();
    IF v_gap IS DISTINCT FROM 'tenant_fk_single_probe' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name the single-column key and only it, named: %', v_gap;
    END IF;
END $$;
