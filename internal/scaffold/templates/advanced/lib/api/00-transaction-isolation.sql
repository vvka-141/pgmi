/*
<pgmi-meta
    id="a7f01000-0000-4000-8000-000000000001"
    idempotent="true">
  <description>
    Transaction policy contract: canonical isolation levels, normalizer, ordering,
    read-only guard, and the resolve-then-open policy resolver
  </description>
  <sortKeys>
    <key>004/000</key>
  </sortKeys>
</pgmi-meta>
*/

-- ============================================================================
-- Transaction Isolation Contract
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
-- Resolve-then-open: the client gateway (tools/mcp-gateway.py, or the reverse
-- proxy a deployment supplies) resolves the route's policy BEFORE opening the
-- transaction — api.rest_route_policy / api.rpc_route_policy /
-- api.mcp_request_policy (lib/api/09-gateways.sql) return the effective
-- characteristics — and opens at max(route floor, client-requested level),
-- READ ONLY when the route declares it, DEFERRABLE when that combination is
-- SERIALIZABLE READ ONLY. X-PGMI-Transaction-Isolation is therefore
-- escalation, not obligation: a client may ask for more than the floor, never
-- less. The SQL gateways cannot SET the level — transaction control is
-- forbidden inside functions (.claude/rules/postgres-transaction.md); they
-- only READ current_setting('transaction_isolation') /
-- current_setting('transaction_read_only') and reject a shortfall. That
-- DB-side check is the fail-closed invariant for proxies that skip the
-- lookup; the resolver is the path correct callers travel.
--
-- Normalization tolerates case, and hyphen/underscore/whitespace separators, so
-- 'READ COMMITTED', 'read-committed', and 'Read_Committed' all map to
-- 'read committed'. PostgreSQL collapses 'read uncommitted' onto 'read committed'
-- (it implements no dirty-read level), so the normalizer does the same rather
-- than rejecting it — matching server behavior. NULL passes through (absent
-- floor); anything else RAISEs.

DO $$ BEGIN RAISE NOTICE '→ Installing transaction isolation contract'; END $$;

CREATE OR REPLACE FUNCTION internal.normalize_transaction_isolation(p_value text, p_raise boolean)
RETURNS text
LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
    v_result text;
BEGIN
    IF p_value IS NULL THEN
        RETURN NULL;  -- NULL floor means 'no requirement': pass through, never raise
    END IF;
    v_result := CASE regexp_replace(lower(btrim(p_value)), '[\s_-]+', ' ', 'g')
        WHEN 'read committed'   THEN 'read committed'
        WHEN 'read uncommitted' THEN 'read committed'
        WHEN 'repeatable read'  THEN 'repeatable read'
        WHEN 'serializable'     THEN 'serializable'
        ELSE NULL
    END;
    IF v_result IS NULL AND p_raise THEN
        RAISE EXCEPTION 'unsupported transaction isolation level: %', p_value
            USING HINT = 'Supported: read committed, repeatable read, serializable (case/separator-insensitive; read uncommitted maps to read committed).';
    END IF;
    RETURN v_result;
END;
$$;

COMMENT ON FUNCTION internal.normalize_transaction_isolation(text, boolean) IS
    'Normalizes an isolation level to canonical PostgreSQL form (read committed | repeatable read | serializable). Tolerates case and hyphen/underscore/space separators; folds read uncommitted onto read committed. NULL passes through (absent floor). p_raise=true RAISEs on unsupported input; p_raise=false returns NULL.';

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
    'Ordinal rank of an isolation level (read committed=1 < repeatable read=2 < serializable=3). RAISEs on unsupported input; NULL yields NULL. Compare two ranks to decide whether an actual level satisfies a required floor.';

-- Gateway validation primitive: returns the current transaction's
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

CREATE OR REPLACE FUNCTION internal.transaction_is_read_only()
RETURNS boolean
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT current_setting('transaction_read_only') = 'on';
$$;

COMMENT ON FUNCTION internal.transaction_is_read_only() IS
    'True when the current transaction is READ ONLY. Gateways use it to reject a read-only route dispatched in a read-write transaction (fail closed) and to skip their own writes (exchange logging, JIT provisioning) when the transaction cannot write.';

-- Resolve-then-open policy resolver. Given a route's declared
-- policy and the client-requested level, returns the characteristics the
-- client gateway should open the transaction with:
--   * isolation  = max(floor, requested) — escalation possible, downgrade
--     structurally impossible; NULL when neither side states a level (open at
--     the connection default)
--   * read-only  = the route's declaration
--   * deferrable = SERIALIZABLE READ ONLY only. Such a transaction waits for a
--     conflict-free snapshot at BEGIN and then can never abort with 40001, so
--     the route needs no retry logic at all (transaction-iso docs).
CREATE OR REPLACE FUNCTION internal.resolve_transaction_policy(
    p_floor text,
    p_read_only boolean,
    p_requested text
) RETURNS TABLE(
    transaction_isolation text,
    transaction_read_only boolean,
    transaction_deferrable boolean
)
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    WITH effective AS (
        SELECT CASE
            WHEN f.lvl IS NULL THEN q.lvl
            WHEN q.lvl IS NULL THEN f.lvl
            WHEN internal.transaction_isolation_rank(q.lvl)
                 >= internal.transaction_isolation_rank(f.lvl) THEN q.lvl
            ELSE f.lvl
        END AS lvl
        FROM internal.normalize_transaction_isolation(p_floor, true) AS f(lvl),
             internal.normalize_transaction_isolation(p_requested, true) AS q(lvl)
    )
    -- COALESCE the comparison: a NULL effective level (open at the connection
    -- default) must yield deferrable = false, not NULL.
    SELECT e.lvl,
           COALESCE(p_read_only, false),
           COALESCE(p_read_only, false) AND COALESCE(e.lvl = 'serializable', false)
    FROM effective e;
$$;

COMMENT ON FUNCTION internal.resolve_transaction_policy(text, boolean, text) IS
    'Resolves the transaction characteristics a client gateway should open with: isolation = max(route floor, client-requested), read-only from the route, DEFERRABLE exactly for SERIALIZABLE READ ONLY. NULL isolation means "open at the connection default". RAISEs on an unsupported level.';

-- Replica-routing hint. readOnly alone is NOT sufficient to say "safe on a
-- read replica": a hot standby caps at repeatable read — SERIALIZABLE is not
-- supported there (mvcc-caveats docs) — so a read-only route with a
-- serializable floor is primary-only. The hint is a function of both policies.
CREATE OR REPLACE FUNCTION internal.transaction_policy_replica_safe(
    p_floor text,
    p_read_only boolean
) RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE(p_read_only, false)
       AND internal.transaction_isolation_rank(
               COALESCE(internal.normalize_transaction_isolation(p_floor, true), 'read committed'))
           <= internal.transaction_isolation_rank('repeatable read');
$$;

COMMENT ON FUNCTION internal.transaction_policy_replica_safe(text, boolean) IS
    'True when a route may be served by a read replica: declared read-only AND its isolation floor is at most repeatable read (a hot standby cannot run SERIALIZABLE). Emitted as x-pgmi-replica-safe in the OpenAPI document.';

-- ============================================================================
-- Inline tests (pure functions)
-- ============================================================================

DO $$
BEGIN
    -- Canonicalization across case and separators
    IF internal.normalize_transaction_isolation('READ COMMITTED', true) IS DISTINCT FROM 'read committed' THEN
        RAISE EXCEPTION 'normalize: uppercase failed';
    END IF;
    IF internal.normalize_transaction_isolation('read-committed', true) IS DISTINCT FROM 'read committed' THEN
        RAISE EXCEPTION 'normalize: hyphen failed';
    END IF;
    IF internal.normalize_transaction_isolation('Read_Committed', true) IS DISTINCT FROM 'read committed' THEN
        RAISE EXCEPTION 'normalize: underscore failed';
    END IF;
    IF internal.normalize_transaction_isolation('  repeatable   read  ', true) IS DISTINCT FROM 'repeatable read' THEN
        RAISE EXCEPTION 'normalize: whitespace collapse failed';
    END IF;
    IF internal.normalize_transaction_isolation('SERIALIZABLE', true) IS DISTINCT FROM 'serializable' THEN
        RAISE EXCEPTION 'normalize: serializable failed';
    END IF;

    -- read uncommitted folds onto read committed (matches PostgreSQL)
    IF internal.normalize_transaction_isolation('read uncommitted', true) IS DISTINCT FROM 'read committed' THEN
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

    -- NULL passes through even with p_raise=true (absent floor is not an error)
    IF internal.normalize_transaction_isolation(NULL, true) IS NOT NULL THEN
        RAISE EXCEPTION 'normalize: NULL input should return NULL, not raise';
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
               IS DISTINCT FROM (internal.transaction_isolation_rank(v_actual) >= internal.transaction_isolation_rank(v_lvl)) THEN
                RAISE EXCEPTION 'shortfall: NULL-ness disagrees with rank comparison for floor %', v_lvl;
            END IF;
            IF internal.transaction_isolation_shortfall(v_lvl) IS NOT NULL
               AND internal.transaction_isolation_shortfall(v_lvl) IS DISTINCT FROM v_actual THEN
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

DO $$
DECLARE
    v_policy record;
BEGIN
    -- max(floor, requested): downgrade impossible, escalation works
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy('repeatable read', false, 'read committed');
    IF v_policy.transaction_isolation IS DISTINCT FROM 'repeatable read' THEN
        RAISE EXCEPTION 'resolve: requesting weaker than the floor must yield the floor, got %', v_policy.transaction_isolation;
    END IF;
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy('repeatable read', false, 'SERIALIZABLE');
    IF v_policy.transaction_isolation IS DISTINCT FROM 'serializable' THEN
        RAISE EXCEPTION 'resolve: requesting stronger than the floor must escalate, got %', v_policy.transaction_isolation;
    END IF;

    -- Absent sides: floor alone, request alone, neither (connection default)
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy('serializable', false, NULL);
    IF v_policy.transaction_isolation IS DISTINCT FROM 'serializable' THEN
        RAISE EXCEPTION 'resolve: floor with no request must yield the floor';
    END IF;
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy(NULL, false, 'repeatable-read');
    IF v_policy.transaction_isolation IS DISTINCT FROM 'repeatable read' THEN
        RAISE EXCEPTION 'resolve: request with no floor must yield the request (normalized)';
    END IF;
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy(NULL, NULL, NULL);
    IF v_policy.transaction_isolation IS NOT NULL
       OR v_policy.transaction_read_only IS DISTINCT FROM false
       OR v_policy.transaction_deferrable IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'resolve: no policy and no request must yield all-default characteristics';
    END IF;
    -- The boolean characteristics are a contract: never NULL, even when the
    -- effective level is NULL (read-only route with no floor and no request).
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy(NULL, true, NULL);
    IF v_policy.transaction_isolation IS NOT NULL
       OR v_policy.transaction_read_only IS DISTINCT FROM true
       OR v_policy.transaction_deferrable IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'resolve: read-only with no level must be (NULL, true, false), got (%, %, %)',
            v_policy.transaction_isolation, v_policy.transaction_read_only, v_policy.transaction_deferrable;
    END IF;

    -- DEFERRABLE exactly for SERIALIZABLE READ ONLY
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy('serializable', true, NULL);
    IF NOT (v_policy.transaction_read_only AND v_policy.transaction_deferrable) THEN
        RAISE EXCEPTION 'resolve: serializable read-only must be deferrable';
    END IF;
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy('repeatable read', true, NULL);
    IF v_policy.transaction_deferrable IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'resolve: repeatable read read-only must NOT be deferrable';
    END IF;
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy('serializable', false, NULL);
    IF v_policy.transaction_deferrable IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'resolve: serializable read-write must NOT be deferrable';
    END IF;
    -- Escalation combines: read-only route, floor rr, client requests serializable
    SELECT * INTO v_policy FROM internal.resolve_transaction_policy('repeatable read', true, 'serializable');
    IF v_policy.transaction_deferrable IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'resolve: escalated serializable read-only must be deferrable';
    END IF;

    -- Replica hint: read-only is necessary but not sufficient
    IF internal.transaction_policy_replica_safe(NULL, false) THEN
        RAISE EXCEPTION 'replica_safe: a read-write route is never replica-safe';
    END IF;
    IF NOT internal.transaction_policy_replica_safe(NULL, true) THEN
        RAISE EXCEPTION 'replica_safe: read-only with no floor is replica-safe';
    END IF;
    IF NOT internal.transaction_policy_replica_safe('repeatable read', true) THEN
        RAISE EXCEPTION 'replica_safe: read-only at repeatable read is replica-safe';
    END IF;
    IF internal.transaction_policy_replica_safe('serializable', true) THEN
        RAISE EXCEPTION 'replica_safe: serializable is not available on a hot standby — primary only';
    END IF;

    -- The deploy transaction is read-write, so the probe answers deterministically here.
    IF internal.transaction_is_read_only() THEN
        RAISE EXCEPTION 'transaction_is_read_only: deploy transaction must be read-write';
    END IF;
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ internal.normalize_transaction_isolation(text, boolean) - canonical level normalizer';
    RAISE NOTICE '  ✓ internal.transaction_isolation_rank(text) - level ordering (rc<rr<s)';
    RAISE NOTICE '  ✓ internal.transaction_isolation_shortfall(text) - gateway floor validation';
    RAISE NOTICE '  ✓ internal.transaction_is_read_only() - gateway read-only probe';
    RAISE NOTICE '  ✓ internal.resolve_transaction_policy(text, boolean, text) - resolve-then-open characteristics';
    RAISE NOTICE '  ✓ internal.transaction_policy_replica_safe(text, boolean) - replica-routing hint';
END $$;
