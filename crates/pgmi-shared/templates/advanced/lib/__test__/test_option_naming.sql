-- ============================================================================
-- Option naming: views, constraints and indexes follow one naming rule set
-- ============================================================================
-- Off by default; switched on in pg_temp.guard_enabled() in deploy/settings.sql.
-- A name that says what an object is reads in an error, a plan and a catalog
-- query without a lookup. pg_temp.naming_rule() is the rule set, one regular
-- expression per kind of object; change it to the project's own. An object
-- PostgreSQL names for a partition is not checked.
-- ============================================================================

CREATE FUNCTION pg_temp.naming_rule()
RETURNS TABLE (kind text, pattern text)
LANGUAGE sql IMMUTABLE AS $$
    VALUES ('view',               '^vw_'),
           ('primary key',        '_pkey$'),
           ('unique constraint',  '_key$'),
           ('foreign key',        '_fkey$'),
           ('check constraint',   '_check$'),
           ('exclusion',          '_excl$'),
           ('unique index',       '^uq_'),
           ('index',              '^ix_')
$$;

CREATE FUNCTION pg_temp.naming_violation()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    WITH named AS (
        SELECT 'view' AS kind, format('%s.%I', s.name, c.relname) AS object, c.relname::text AS name
        FROM pg_class c JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
        WHERE c.relkind IN ('v', 'm')
        UNION ALL
        SELECT CASE k.contype WHEN 'p' THEN 'primary key' WHEN 'u' THEN 'unique constraint' WHEN 'f' THEN 'foreign key'
                              WHEN 'c' THEN 'check constraint' WHEN 'x' THEN 'exclusion' END,
               format('%s.%I on %I', s.name, k.conname, c.relname), k.conname::text
        FROM pg_constraint k
        JOIN pg_class c ON c.oid = k.conrelid
        JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
        WHERE k.contype IN ('p', 'u', 'f', 'c', 'x') AND k.conparentid = 0 AND NOT c.relispartition
        UNION ALL
        SELECT CASE WHEN i.indisunique THEN 'unique index' ELSE 'index' END,
               format('%s.%I', s.name, c.relname), c.relname::text
        FROM pg_index i
        JOIN pg_class c ON c.oid = i.indexrelid
        JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
        WHERE NOT c.relispartition
          AND NOT EXISTS (SELECT FROM pg_constraint k WHERE k.conindid = i.indexrelid)
    )
    SELECT format('%s %s (want %s)', n.kind, n.object, r.pattern)
    FROM named n
    JOIN pg_temp.naming_rule() r ON r.kind = n.kind
    WHERE n.name !~ r.pattern
$$;

DO $$
BEGIN
    IF pg_temp.guard_enabled('naming') THEN
        PERFORM pg_temp.enforce_guard('naming', ARRAY(SELECT pg_temp.naming_violation()));
    END IF;
END $$;

DO $$
DECLARE
    v_found text;
BEGIN
    CREATE TABLE core.naming_probe (id int CONSTRAINT naming_probe_pk PRIMARY KEY, code text CONSTRAINT naming_probe_code_check CHECK (code ~ '\S'));
    CREATE INDEX naming_probe_code_idx ON core.naming_probe (code);
    CREATE UNIQUE INDEX ix_naming_probe_code ON core.naming_probe (lower(code));
    CREATE VIEW core.naming_probe_view WITH (security_invoker = true) AS SELECT id FROM core.naming_probe;
    SELECT string_agg(v, '; ' ORDER BY v COLLATE "C") INTO v_found FROM pg_temp.naming_violation() v WHERE v LIKE '%naming_probe%';
    IF v_found IS DISTINCT FROM 'index core.naming_probe_code_idx (want ^ix_); primary key core.naming_probe_pk on naming_probe (want _pkey$); '
                                'unique index core.ix_naming_probe_code (want ^uq_); view core.naming_probe_view (want ^vw_)' THEN
        RAISE EXCEPTION 'TEST FAILED: naming must name each misnamed object and pass the check constraint, named: %', v_found;
    END IF;
END $$;
