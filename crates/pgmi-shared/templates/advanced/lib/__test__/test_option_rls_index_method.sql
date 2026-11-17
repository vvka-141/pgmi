-- ============================================================================
-- Option rls_index_method: no lookup index but btree on a forced table
-- ============================================================================
-- Off by default; switched on in pg_temp.guard_enabled() in deploy/settings.sql.
-- Under row-level security PostgreSQL runs a query's own conditions after the
-- policy unless their operators are leakproof, and the GIN, GiST and trigram
-- operators are not: such an index cannot serve the query on a forced table,
-- and the query scans. An index that backs a constraint (an exclusion or a
-- WITHOUT OVERLAPS key) is not a lookup index and passes.
-- ============================================================================

CREATE FUNCTION pg_temp.rls_index_method_violation()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT format('%s.%I (%s on %s.%I)', s.name, ic.relname, am.amname, t.relnamespace::regnamespace, t.relname)
    FROM pg_index ix
    JOIN pg_class ic ON ic.oid = ix.indexrelid
    JOIN pg_am am ON am.oid = ic.relam
    JOIN pg_class t ON t.oid = ix.indrelid
    JOIN pg_temp.application_schema() s ON s.oid = ic.relnamespace
    WHERE t.relrowsecurity AND t.relforcerowsecurity
      AND am.amname IS DISTINCT FROM 'btree'
      AND NOT EXISTS (SELECT FROM pg_constraint k WHERE k.conindid = ix.indexrelid)
$$;

DO $$
BEGIN
    IF pg_temp.guard_enabled('rls_index_method') THEN
        PERFORM pg_temp.enforce_guard('rls_index_method', ARRAY(SELECT pg_temp.rls_index_method_violation()));
    END IF;
END $$;

DO $$
DECLARE
    v_found text;
BEGIN
    CREATE TABLE core.rls_index_probe (organization_id uuid, tags text[], during tstzrange,
                                       EXCLUDE USING gist (during WITH &&));
    PERFORM core.apply_org_rls('core.rls_index_probe', p_has_created_by => false);
    CREATE INDEX ix_rls_index_probe_tags ON core.rls_index_probe USING gin (tags);
    SELECT string_agg(v, ', ') INTO v_found FROM pg_temp.rls_index_method_violation() v WHERE v LIKE 'core.%rls_index_probe%';
    IF v_found IS DISTINCT FROM 'core.ix_rls_index_probe_tags (gin on core.rls_index_probe)' THEN
        RAISE EXCEPTION 'TEST FAILED: rls_index_method must name a GIN lookup index on a forced table and pass the exclusion constraint, named: %', v_found;
    END IF;
END $$;
