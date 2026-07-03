/*
<pgmi-meta
    id="a7f01000-0000-4000-8000-000000000001"
    idempotent="true">
  <description>
    Transaction isolation contract: canonical levels, header name, normalizer, ordering
  </description>
  <sortKeys>
    <key>004/000</key>
  </sortKeys>
</pgmi-meta>
*/

-- ============================================================================
-- Transaction Isolation Contract (PGMI-107)
-- ============================================================================
-- Source of truth shared by handler registration (api.create_or_replace_*_handler,
-- lib/api/08-registration.sql) and the protocol gateways (lib/api/09-gateways.sql).
--
-- Supported levels (canonical form == PostgreSQL's transaction_isolation GUC):
--     'read committed' < 'repeatable read' < 'serializable'
-- A caller satisfies a route iff the current transaction's actual level ranks
-- >= the route's required floor. A NULL floor means "no requirement" and is
-- treated as 'read committed' (every transaction satisfies it).
--
-- Header: callers request a level via X-PGMI-Transaction-Isolation. The header
-- is read by the client (tools/mcp-gateway.py), which opens the transaction at
-- that level BEFORE the first statement. Gateways cannot SET the level —
-- transaction control is forbidden inside functions (.claude/rules/
-- postgres-transaction.md); they only READ current_setting('transaction_isolation')
-- and reject when it is weaker than the floor.
--
-- Normalization tolerates case, and hyphen/underscore/whitespace separators, so
-- 'READ COMMITTED', 'read-committed', and 'Read_Committed' all map to
-- 'read committed'. PostgreSQL collapses 'read uncommitted' onto 'read committed'
-- (it implements no dirty-read level), so the normalizer does the same rather
-- than rejecting it — matching server behavior. Anything else RAISEs.

DO $$ BEGIN RAISE NOTICE '→ Installing transaction isolation contract'; END $$;

CREATE OR REPLACE FUNCTION internal.normalize_transaction_isolation(p_value text, p_raise boolean)
RETURNS text
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
    v_result text;
BEGIN
    v_result := CASE regexp_replace(lower(btrim(p_value)), '[\s_-]+', ' ', 'g')
        WHEN 'read committed'   THEN 'read committed'
        WHEN 'read uncommitted' THEN 'read committed'
        WHEN 'repeatable read'  THEN 'repeatable read'
        WHEN 'serializable'     THEN 'serializable'
        ELSE NULL
    END;
    IF v_result IS NULL AND p_raise THEN
        RAISE EXCEPTION 'unsupported transaction isolation level: %', COALESCE(p_value, '<null>')
            USING HINT = 'Supported: read committed, repeatable read, serializable (case/separator-insensitive; read uncommitted maps to read committed).';
    END IF;
    RETURN v_result;
END;
$$;

COMMENT ON FUNCTION internal.normalize_transaction_isolation(text, boolean) IS
    'Normalizes an isolation level to canonical PostgreSQL form (read committed | repeatable read | serializable). Tolerates case and hyphen/underscore/space separators; folds read uncommitted onto read committed. p_raise=true RAISEs on unsupported input; p_raise=false returns NULL.';

CREATE OR REPLACE FUNCTION internal.transaction_isolation_rank(p_value text)
RETURNS integer
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE internal.normalize_transaction_isolation(p_value, true)
        WHEN 'read committed'  THEN 1
        WHEN 'repeatable read' THEN 2
        WHEN 'serializable'    THEN 3
    END;
$$;

COMMENT ON FUNCTION internal.transaction_isolation_rank(text) IS
    'Ordinal rank of an isolation level (read committed=1 < repeatable read=2 < serializable=3). RAISEs on unsupported input. Compare two ranks to decide whether an actual level satisfies a required floor.';

-- Gateway validation primitive (PGMI-110): returns the current transaction's
-- isolation level when it is WEAKER than the required floor (i.e. the call must
-- be rejected), else NULL (satisfied). NULL floor is always satisfied. Reads the
-- level via current_setting('transaction_isolation') — a SHOW-class read that is
-- legal inside SECURITY DEFINER gateway functions; the gateway can only read the
-- level, never SET it (that is the client's job before the first statement).
CREATE OR REPLACE FUNCTION internal.transaction_isolation_shortfall(p_required text)
RETURNS text
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN p_required IS NULL THEN NULL
        WHEN internal.transaction_isolation_rank(current_setting('transaction_isolation'))
             < internal.transaction_isolation_rank(p_required)
        THEN current_setting('transaction_isolation')
        ELSE NULL
    END;
$$;

COMMENT ON FUNCTION internal.transaction_isolation_shortfall(text) IS
    'Returns the current transaction isolation level when it is weaker than p_required (reject), else NULL (satisfied). NULL floor is always satisfied. Used by the REST/RPC/MCP gateways to decide whether to dispatch the handler.';

-- ============================================================================
-- Inline tests (pure functions)
-- ============================================================================

DO $$
BEGIN
    -- Canonicalization across case and separators
    IF internal.normalize_transaction_isolation('READ COMMITTED', true) <> 'read committed' THEN
        RAISE EXCEPTION 'normalize: uppercase failed';
    END IF;
    IF internal.normalize_transaction_isolation('read-committed', true) <> 'read committed' THEN
        RAISE EXCEPTION 'normalize: hyphen failed';
    END IF;
    IF internal.normalize_transaction_isolation('Read_Committed', true) <> 'read committed' THEN
        RAISE EXCEPTION 'normalize: underscore failed';
    END IF;
    IF internal.normalize_transaction_isolation('  repeatable   read  ', true) <> 'repeatable read' THEN
        RAISE EXCEPTION 'normalize: whitespace collapse failed';
    END IF;
    IF internal.normalize_transaction_isolation('SERIALIZABLE', true) <> 'serializable' THEN
        RAISE EXCEPTION 'normalize: serializable failed';
    END IF;

    -- read uncommitted folds onto read committed (matches PostgreSQL)
    IF internal.normalize_transaction_isolation('read uncommitted', true) <> 'read committed' THEN
        RAISE EXCEPTION 'normalize: read uncommitted should fold to read committed';
    END IF;

    -- Ordering: read committed < repeatable read < serializable
    IF NOT (internal.transaction_isolation_rank('read committed')
            < internal.transaction_isolation_rank('repeatable read')) THEN
        RAISE EXCEPTION 'rank: read committed should be < repeatable read';
    END IF;
    IF NOT (internal.transaction_isolation_rank('repeatable read')
            < internal.transaction_isolation_rank('serializable')) THEN
        RAISE EXCEPTION 'rank: repeatable read should be < serializable';
    END IF;

    -- Worked examples from the contract (actual >= required ⇒ satisfied)
    IF NOT (internal.transaction_isolation_rank('serializable')
            >= internal.transaction_isolation_rank('repeatable read')) THEN
        RAISE EXCEPTION 'satisfies: serializable should satisfy a repeatable read floor';
    END IF;
    IF internal.transaction_isolation_rank('read committed')
            >= internal.transaction_isolation_rank('repeatable read') THEN
        RAISE EXCEPTION 'satisfies: read committed must NOT satisfy a repeatable read floor';
    END IF;

    -- p_raise=false yields NULL (used to distinguish absent vs invalid at call sites)
    IF internal.normalize_transaction_isolation('bogus', false) IS NOT NULL THEN
        RAISE EXCEPTION 'normalize: non-raising path should return NULL for unsupported';
    END IF;

    -- shortfall semantics, independent of whatever level this deploy runs under:
    --   NULL floor is always satisfied; a floor equal to the current level is
    --   satisfied; shortfall is non-NULL exactly when the current level is too
    --   weak, and then it reports the current level.
    DECLARE
        v_actual text := current_setting('transaction_isolation');
        v_lvl text;
    BEGIN
        IF internal.transaction_isolation_shortfall(NULL) IS NOT NULL THEN
            RAISE EXCEPTION 'shortfall: NULL floor must always be satisfied';
        END IF;
        IF internal.transaction_isolation_shortfall(v_actual) IS NOT NULL THEN
            RAISE EXCEPTION 'shortfall: a floor equal to the current level must be satisfied';
        END IF;
        FOREACH v_lvl IN ARRAY ARRAY['read committed', 'repeatable read', 'serializable'] LOOP
            IF (internal.transaction_isolation_shortfall(v_lvl) IS NULL)
               <> (internal.transaction_isolation_rank(v_actual) >= internal.transaction_isolation_rank(v_lvl)) THEN
                RAISE EXCEPTION 'shortfall: NULL-ness disagrees with rank comparison for floor %', v_lvl;
            END IF;
            IF internal.transaction_isolation_shortfall(v_lvl) IS NOT NULL
               AND internal.transaction_isolation_shortfall(v_lvl) <> v_actual THEN
                RAISE EXCEPTION 'shortfall: reported level must equal the current level for floor %', v_lvl;
            END IF;
        END LOOP;
    END;
END $$;

-- Unsupported input RAISEs under p_raise=true
DO $$
BEGIN
    PERFORM internal.normalize_transaction_isolation('snapshot', true);
    RAISE EXCEPTION 'normalize: expected exception for unsupported level';
EXCEPTION WHEN OTHERS THEN
    IF SQLERRM NOT LIKE 'unsupported transaction isolation level%' THEN
        RAISE EXCEPTION 'normalize: wrong error for unsupported level: %', SQLERRM;
    END IF;
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ internal.normalize_transaction_isolation(text, boolean) - canonical level normalizer';
    RAISE NOTICE '  ✓ internal.transaction_isolation_rank(text) - level ordering (rc<rr<s)';
END $$;
