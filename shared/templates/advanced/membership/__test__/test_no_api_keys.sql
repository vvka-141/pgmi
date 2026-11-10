-- ============================================================================
-- Test: no API keys
-- ============================================================================
-- Machine access goes through approved connections and service principals.
-- A table, type or routine whose name says api_key is the old credential model
-- coming back.
-- ============================================================================

DO $$
DECLARE
    v_found text;
BEGIN
    SELECT string_agg(o.kind || ' ' || o.name, ', ' ORDER BY o.name)
    INTO v_found
    FROM (
        SELECT 'relation', format('%I.%I', n.nspname, c.relname)
        FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relname ~* 'api_?key'
        UNION ALL
        SELECT 'type', format('%I.%I', n.nspname, t.typname)
        FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
        WHERE t.typname ~* 'api_?key' AND t.typtype IN ('e', 'd', 'b', 'r', 'm')
        UNION ALL
        SELECT 'routine', format('%I.%I', n.nspname, p.proname)
        FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE p.proname ~* 'api_?key'
    ) AS o(kind, name);

    IF v_found IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: API-key objects remain: %', v_found;
    END IF;
END $$;
