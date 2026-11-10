-- ============================================================================
-- Option strict_fk_index: a key with a referential action has an exact index
-- ============================================================================
-- Off by default; switched on in pg_temp.guard_enabled() in deploy.sql.
-- ON DELETE / ON UPDATE CASCADE, SET NULL and SET DEFAULT look up the child
-- rows by the key's columns alone, bypassing row-level security. An index led
-- by the organization and continuing elsewhere, or one partial on deleted_at,
-- cannot serve that lookup: it scans every organization's rows. So for such a
-- key some index has exactly the key's columns first, in any order, and no
-- predicate but "<key column> IS NOT NULL". A key declared with
-- pg_temp.exempt_from_fk_index is skipped, as by test_fk_index_coverage.sql.
-- ============================================================================

CREATE FUNCTION pg_temp.strict_fk_index_violation()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT format('%s.%I (%s) -> %s', s.name, rel.relname,
                  (SELECT string_agg(a.attname, ', ' ORDER BY k.o)
                   FROM unnest(con.conkey) WITH ORDINALITY k(attnum, o)
                   JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum),
                  (SELECT format('%s.%I', c.relnamespace::regnamespace, c.relname) FROM pg_class c WHERE c.oid = con.confrelid))
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    JOIN pg_temp.application_schema() s ON s.oid = rel.relnamespace
    WHERE con.contype = 'f' AND con.conparentid = 0 AND NOT rel.relispartition
      AND (con.confdeltype IN ('c', 'n', 'd') OR con.confupdtype IN ('c', 'n', 'd'))
      AND NOT EXISTS (SELECT FROM internal.fk_index_exemption e
                      JOIN pg_attribute a ON a.attrelid = e.relation AND a.attname = e.column_name
                      WHERE e.relation = con.conrelid AND a.attnum = ANY (con.conkey))
      AND NOT EXISTS (
          SELECT FROM pg_index ix
          WHERE ix.indrelid = con.conrelid
            AND ix.indnkeyatts >= cardinality(con.conkey)
            AND (SELECT array_agg(k.attnum) FROM unnest(ix.indkey) WITH ORDINALITY k(attnum, o)
                 WHERE k.o <= cardinality(con.conkey)) @> con.conkey
            AND (ix.indpred IS NULL
                 OR pg_get_expr(ix.indpred, ix.indrelid) IN (
                        SELECT format('(%I IS NOT NULL)', a.attname)
                        FROM pg_attribute a
                        WHERE a.attrelid = con.conrelid AND a.attnum = ANY (con.conkey))))
$$;

DO $$
BEGIN
    IF pg_temp.guard_enabled('strict_fk_index') THEN
        PERFORM pg_temp.enforce_guard('strict_fk_index', ARRAY(SELECT pg_temp.strict_fk_index_violation()));
    END IF;
END $$;

DO $$
DECLARE
    v_found text;
BEGIN
    CREATE TABLE core.strict_fk_parent_probe (object_id uuid PRIMARY KEY);
    CREATE TABLE core.strict_fk_child_probe (
        organization_id uuid,
        parent_id uuid REFERENCES core.strict_fk_parent_probe ON DELETE CASCADE,
        deleted_at timestamptz);
    CREATE INDEX ix_strict_fk_child_probe_org ON core.strict_fk_child_probe (organization_id, parent_id);
    CREATE INDEX ix_strict_fk_child_probe_live ON core.strict_fk_child_probe (parent_id) WHERE deleted_at IS NULL;
    SELECT string_agg(v, ', ') INTO v_found FROM pg_temp.strict_fk_index_violation() v WHERE v LIKE 'core.strict_fk_child_probe %';
    IF v_found IS DISTINCT FROM 'core.strict_fk_child_probe (parent_id) -> core.strict_fk_parent_probe' THEN
        RAISE EXCEPTION 'TEST FAILED: strict_fk_index must name a cascading key served only by an organization-led and a partial index, named: %', v_found;
    END IF;
    CREATE INDEX ix_strict_fk_child_probe_parent ON core.strict_fk_child_probe (parent_id) WHERE parent_id IS NOT NULL;
    IF EXISTS (SELECT FROM pg_temp.strict_fk_index_violation() v WHERE v LIKE 'core.strict_fk_child_probe %') THEN
        RAISE EXCEPTION 'TEST FAILED: strict_fk_index must accept an index on exactly the key, partial on the key IS NOT NULL';
    END IF;
END $$;
