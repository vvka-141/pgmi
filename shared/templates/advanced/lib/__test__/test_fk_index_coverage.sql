-- ============================================================================
-- Test: every foreign key has an index leading with it
-- ============================================================================
-- PostgreSQL indexes the referenced side of a foreign key, never the
-- referencing side. Without an index there, a join from the parent and every
-- delete or key update of a parent row scan the child. So some index of the
-- referencing table leads with a column of the key. A key holding an
-- organization column (pg_temp.tenant_column()) and others is judged by its
-- first other column, led by it or by (organization, it): nearly every table
-- has an index led by the organization, which would pass every such key.
--
-- A key left without one on purpose is declared in the run-once file that
-- creates it, with the reason:
--   SELECT pg_temp.exempt_from_fk_index('core.task', '{created_by_user_id}', '<reason>');
-- An exemption that names no unindexed key fails as stale.
-- ============================================================================

CREATE FUNCTION pg_temp.fk_index_exemption()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN c.oid IS NULL THEN e.relation::oid::text || '.' || e.column_name
                ELSE format('%s.%I.%I', c.relnamespace::regnamespace, c.relname, e.column_name) END
    FROM internal.fk_index_exemption e
    LEFT JOIN pg_class c ON c.oid = e.relation
$$;

CREATE FUNCTION pg_temp.unindexed_fk_column()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT DISTINCT format('%s.%I.%I', s.name, rel.relname, att.attname)
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    JOIN pg_temp.application_schema() s ON s.oid = rel.relnamespace
    CROSS JOIN LATERAL (
        SELECT (SELECT a.attnum FROM pg_attribute a
                WHERE a.attrelid = con.conrelid AND a.attnum = ANY (con.conkey)
                  AND a.attname IN (SELECT pg_temp.tenant_column()) LIMIT 1) AS tenant_attnum,
               (SELECT k.attnum FROM unnest(con.conkey) WITH ORDINALITY k(attnum, o)
                JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
                WHERE a.attname NOT IN (SELECT pg_temp.tenant_column())
                ORDER BY k.o LIMIT 1) AS other_attnum
    ) t
    JOIN pg_attribute att ON att.attrelid = con.conrelid
                         AND att.attnum = CASE WHEN t.tenant_attnum IS NOT NULL AND t.other_attnum IS NOT NULL
                                               THEN t.other_attnum ELSE con.conkey[1] END
    WHERE con.contype = 'f'
      AND NOT rel.relispartition
      AND NOT EXISTS (
          SELECT FROM pg_index ix
          WHERE ix.indrelid = con.conrelid
            AND CASE WHEN t.tenant_attnum IS NOT NULL AND t.other_attnum IS NOT NULL
                     THEN ix.indkey[0] = t.other_attnum
                          OR (ix.indkey[0] = t.tenant_attnum AND ix.indnkeyatts >= 2 AND ix.indkey[1] = t.other_attnum)
                     ELSE ix.indkey[0] = ANY (con.conkey) END)
$$;

CREATE FUNCTION pg_temp.stale_fk_index_exemption(p_columns text[])
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT c FROM unnest(p_columns) c WHERE c NOT IN (SELECT pg_temp.unindexed_fk_column())
$$;

DO $$
DECLARE
    v_bad text;
BEGIN
    SELECT string_agg(u, E'\n  ' ORDER BY u) INTO v_bad
    FROM pg_temp.unindexed_fk_column() u
    WHERE u NOT IN (SELECT pg_temp.fk_index_exemption());
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: foreign key column(s) no index leads with:\n  %', v_bad
            USING HINT = 'Create an index leading with the column in the run-once file that creates the table, or declare it there: SELECT pg_temp.exempt_from_fk_index(''<table>'', ''{<column>}'', ''<reason>'');';
    END IF;

    SELECT string_agg(c, ', ' ORDER BY c) INTO v_bad
    FROM pg_temp.stale_fk_index_exemption(ARRAY(SELECT pg_temp.fk_index_exemption())) c;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: stale foreign-key index exemption(s), the key is indexed or gone: %', v_bad;
    END IF;

    IF NOT EXISTS (SELECT FROM pg_constraint k JOIN pg_class c ON c.oid = k.conrelid
                   JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace WHERE k.contype = 'f') THEN
        RAISE EXCEPTION 'TEST FAILED: no foreign key in any application schema; the scan is blind';
    END IF;

    CREATE TABLE core.fk_parent_probe (object_id uuid PRIMARY KEY, organization_id uuid NOT NULL,
                                       UNIQUE (organization_id, object_id));
    CREATE TABLE core.fk_child_probe (
        organization_id uuid NOT NULL,
        parent_id       uuid REFERENCES core.fk_parent_probe,
        tenant_parent_id uuid,
        FOREIGN KEY (organization_id, tenant_parent_id) REFERENCES core.fk_parent_probe (organization_id, object_id));
    CREATE INDEX ix_fk_child_probe_organization ON core.fk_child_probe (organization_id);
    SELECT string_agg(u, ', ' ORDER BY u) INTO v_bad FROM pg_temp.unindexed_fk_column() u WHERE u LIKE 'core.fk_child_probe.%';
    IF v_bad IS DISTINCT FROM 'core.fk_child_probe.parent_id, core.fk_child_probe.tenant_parent_id' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name an unindexed key and a tenant key led only by the organization, named: %', v_bad;
    END IF;
    CREATE INDEX ix_fk_child_probe_parent ON core.fk_child_probe (parent_id);
    CREATE INDEX ix_fk_child_probe_tenant_parent ON core.fk_child_probe (organization_id, tenant_parent_id);
    IF EXISTS (SELECT FROM pg_temp.unindexed_fk_column() u WHERE u LIKE 'core.fk_child_probe.%') THEN
        RAISE EXCEPTION 'TEST FAILED: an index leading with the key, or with (organization, key), serves it';
    END IF;
    SELECT string_agg(c, ', ') INTO v_bad
    FROM pg_temp.stale_fk_index_exemption(ARRAY['core.fk_child_probe.parent_id', 'membership.role_assignment.role_id']) c;
    IF v_bad IS DISTINCT FROM 'core.fk_child_probe.parent_id' THEN
        RAISE EXCEPTION 'TEST FAILED: an exemption for an indexed key must be named stale, named: %', v_bad;
    END IF;
END $$;
