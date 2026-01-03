/*
<pgmi-meta
    id="a7f01000-0002-4000-8000-000000000002"
    idempotent="true">
  <description>
    API schema views: analysis, statistics, and summary views for handlers and routes
  </description>
  <sortKeys>
    <key>004/010</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing API views'; END $$;

-- ============================================================================
-- Handler Analysis View
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_handler_info AS
SELECT
    h.object_id,
    h.handler_type,
    h.handler_function_name,
    h.description,

    h.created_at,
    now() - h.created_at AS age,

    EXISTS (SELECT 1 FROM pg_proc WHERE oid = h.handler_func::oid) AS function_exists,

    CASE
        WHEN NOT EXISTS (SELECT 1 FROM pg_proc WHERE oid = h.handler_func::oid) THEN NULL
        ELSE h.def_hash != extensions.digest(
            convert_to(pg_get_functiondef(h.handler_func::oid), 'UTF8'), 'sha256'
        )
    END AS definition_drifted,

    EXISTS (SELECT 1 FROM api.rest_route r WHERE r.handler_object_id = h.object_id) AS has_rest_route,
    EXISTS (SELECT 1 FROM api.rpc_route r WHERE r.handler_object_id = h.object_id) AS has_rpc_route,
    EXISTS (SELECT 1 FROM api.mcp_route r WHERE r.handler_object_id = h.object_id) AS has_mcp_route,

    (SELECT address_regexp FROM api.rest_route WHERE handler_object_id = h.object_id) AS rest_pattern,
    (SELECT method_name FROM api.rpc_route WHERE handler_object_id = h.object_id) AS rpc_method,
    (SELECT mcp_name FROM api.mcp_route WHERE handler_object_id = h.object_id) AS mcp_name,
    (SELECT mcp_type FROM api.mcp_route WHERE handler_object_id = h.object_id) AS mcp_type,

    array_to_string(h.accepts, ', ') AS accepts_formatted,
    array_to_string(h.produces, ', ') AS produces_formatted,
    h.requires_auth,

    h.returns_type::text AS returns_type_name,
    h.returns_set,
    h.volatility,
    h.parallel,
    h.leakproof,
    h.security,
    h.language_name,
    h.owner_name,

    EXISTS (
        SELECT 1 FROM core.attached_text
        WHERE weakref_object_id = h.object_id
    ) AS has_attached_properties

FROM api.handler h;

COMMENT ON VIEW api.vw_handler_info IS
    'Power-user analysis view for handlers. Includes lifecycle age, health checks (function_exists, definition_drifted), route bindings, and attached properties indicator.';

-- ============================================================================
-- Handler Statistics View (GROUPING SETS)
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_handler_stats AS
SELECT
    handler_type,
    volatility,
    leakproof,
    security,
    language_name,
    requires_auth,
    count(*) AS handler_count,

    GROUPING(handler_type) AS _grp_type,
    GROUPING(volatility) AS _grp_volatility,
    GROUPING(leakproof) AS _grp_leakproof,
    GROUPING(security) AS _grp_security,
    GROUPING(language_name) AS _grp_language,
    GROUPING(requires_auth) AS _grp_auth
FROM api.handler
GROUP BY GROUPING SETS (
    (),
    (handler_type),
    (volatility),
    (language_name),
    (security),
    (requires_auth),
    (leakproof),
    (handler_type, volatility),
    (handler_type, language_name),
    (handler_type, requires_auth)
);

COMMENT ON VIEW api.vw_handler_stats IS
    'Multi-dimensional handler statistics using GROUPING SETS. Use _grp_* columns to identify aggregation level (1=aggregated, 0=specific value).';

-- ============================================================================
-- Handler Summary Dashboard View (single row)
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_handler_summary AS
SELECT
    count(*) AS total_handlers,
    count(*) FILTER (WHERE handler_type = 'rest') AS rest_handlers,
    count(*) FILTER (WHERE handler_type = 'rpc') AS rpc_handlers,
    count(*) FILTER (WHERE handler_type::text LIKE 'mcp_%') AS mcp_handlers,
    count(*) FILTER (WHERE volatility = 'immutable') AS immutable_count,
    count(*) FILTER (WHERE volatility = 'stable') AS stable_count,
    count(*) FILTER (WHERE volatility = 'volatile') AS volatile_count,
    count(*) FILTER (WHERE leakproof) AS leakproof_count,
    count(*) FILTER (WHERE security = 'definer') AS security_definer_count,
    count(*) FILTER (WHERE security = 'invoker') AS security_invoker_count,
    count(*) FILTER (WHERE requires_auth) AS requires_auth_count,
    count(DISTINCT language_name) AS language_count,
    count(DISTINCT owner_name) AS owner_count,
    min(created_at) AS oldest_handler_at,
    max(created_at) AS newest_handler_at
FROM api.handler;

COMMENT ON VIEW api.vw_handler_summary IS
    'Single-row dashboard showing handler counts by type, volatility, security model, and other characteristics.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.vw_handler_info - power-user analysis view';
    RAISE NOTICE '  ✓ api.vw_handler_stats - multi-dimensional statistics';
    RAISE NOTICE '  ✓ api.vw_handler_summary - dashboard summary';
END $$;

-- ============================================================================
-- Route Views
-- ============================================================================

DO $$ BEGIN RAISE NOTICE '→ Installing route views'; END $$;

-- ============================================================================
-- Unified Route Analysis View
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_route_info AS
-- REST routes
SELECT
    r.handler_object_id AS object_id,
    'rest'::text AS route_type,
    r.address_regexp AS pattern,
    r.method_regexp,
    NULL::text AS mcp_type,
    h.handler_function_name,
    h.requires_auth,
    r.auto_log,
    r.sequence_number,
    h.volatility,
    h.created_at,
    now() - h.created_at AS age
FROM api.rest_route r
JOIN api.handler h ON h.object_id = r.handler_object_id

UNION ALL

-- RPC routes
SELECT
    r.handler_object_id,
    'rpc'::text,
    r.method_name,
    NULL,
    NULL,
    h.handler_function_name,
    h.requires_auth,
    r.auto_log,
    NULL,
    h.volatility,
    h.created_at,
    now() - h.created_at
FROM api.rpc_route r
JOIN api.handler h ON h.object_id = r.handler_object_id

UNION ALL

-- MCP routes
SELECT
    r.handler_object_id,
    'mcp_' || r.mcp_type,
    COALESCE(r.mcp_name, r.uri_template),
    NULL,
    r.mcp_type,
    h.handler_function_name,
    h.requires_auth,
    true,
    NULL,
    h.volatility,
    h.created_at,
    now() - h.created_at
FROM api.mcp_route r
JOIN api.handler h ON h.object_id = r.handler_object_id;

COMMENT ON VIEW api.vw_route_info IS
    'Unified route view across all protocols. Shows pattern, handler, auth requirements, and age.';

-- ============================================================================
-- Route Statistics View (GROUPING SETS)
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_route_stats AS
WITH route_base AS (
    SELECT route_type, requires_auth, auto_log, volatility
    FROM api.vw_route_info
)
SELECT
    route_type,
    requires_auth,
    auto_log,
    volatility,
    count(*) AS route_count,
    GROUPING(route_type) AS _grp_type,
    GROUPING(requires_auth) AS _grp_auth,
    GROUPING(auto_log) AS _grp_log,
    GROUPING(volatility) AS _grp_volatility
FROM route_base
GROUP BY GROUPING SETS (
    (),
    (route_type),
    (requires_auth),
    (auto_log),
    (volatility),
    (route_type, requires_auth),
    (route_type, volatility)
);

COMMENT ON VIEW api.vw_route_stats IS
    'Multi-dimensional route statistics using GROUPING SETS. Use _grp_* columns to identify aggregation level.';

-- ============================================================================
-- Route Summary Dashboard View (single row)
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_route_summary AS
SELECT
    count(*) AS total_routes,
    count(*) FILTER (WHERE route_type = 'rest') AS rest_routes,
    count(*) FILTER (WHERE route_type = 'rpc') AS rpc_routes,
    count(*) FILTER (WHERE route_type LIKE 'mcp_%') AS mcp_routes,
    count(*) FILTER (WHERE requires_auth) AS auth_required_count,
    count(*) FILTER (WHERE auto_log) AS auto_log_count,
    min(created_at) AS oldest_route_at,
    max(created_at) AS newest_route_at
FROM api.vw_route_info;

COMMENT ON VIEW api.vw_route_summary IS
    'Single-row dashboard showing route counts by protocol type and configuration.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.vw_route_info - unified route analysis view';
    RAISE NOTICE '  ✓ api.vw_route_stats - multi-dimensional statistics';
    RAISE NOTICE '  ✓ api.vw_route_summary - dashboard summary';
END $$;

-- ============================================================================
-- Exchange Views (Request/Response Logging with Replay SQL)
-- ============================================================================

DO $$ BEGIN RAISE NOTICE '→ Installing exchange views'; END $$;

-- ============================================================================
-- REST Exchange Analysis View
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_rest_exchange_info AS
SELECT
    e.sequence_number,
    e.object_id AS exchange_id,
    e.enqueued_at,
    now() - e.enqueued_at AS age,
    e.completed_at,
    e.completed_at - e.enqueued_at AS duration,
    e.response IS NULL AS is_pending,

    (e.request).method AS request_method,
    (e.request).url AS request_url,

    (e.response).status_code AS response_status,
    COALESCE((e.response).status_code >= 400, false) AS is_error,

    e.handler_object_id,
    h.handler_function_name,

    format(
        $f$SELECT api.rest_invoke(%L, %L, %L::extensions.hstore, %s);$f$,
        (e.request).method,
        (e.request).url,
        COALESCE((e.request).headers::text, ''),
        CASE WHEN (e.request).content IS NULL THEN 'NULL'
             ELSE format('decode(%L, ''hex'')', encode((e.request).content, 'hex'))
        END
    ) AS replay_sql

FROM api.rest_exchange e
LEFT JOIN api.handler h ON h.object_id = e.handler_object_id;

COMMENT ON VIEW api.vw_rest_exchange_info IS
    'REST exchange analysis with age, duration, error status, and replay_sql for troubleshooting.';

-- ============================================================================
-- RPC Exchange Analysis View
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_rpc_exchange_info AS
SELECT
    e.sequence_number,
    e.object_id AS exchange_id,
    e.enqueued_at,
    now() - e.enqueued_at AS age,
    e.completed_at,
    e.completed_at - e.enqueued_at AS duration,
    e.response IS NULL AS is_pending,

    r.method_name AS rpc_method,

    (e.response).status_code AS response_status,
    COALESCE((e.response).status_code >= 400, false) AS is_error,

    e.handler_object_id,
    h.handler_function_name,

    format(
        $f$SELECT api.rpc_invoke(%L::uuid, %L::extensions.hstore, %s);$f$,
        e.handler_object_id,
        COALESCE((e.request).headers::text, ''),
        CASE WHEN (e.request).content IS NULL THEN 'NULL'
             ELSE format('decode(%L, ''hex'')', encode((e.request).content, 'hex'))
        END
    ) AS replay_sql

FROM api.rpc_exchange e
LEFT JOIN api.handler h ON h.object_id = e.handler_object_id
LEFT JOIN api.rpc_route r ON r.handler_object_id = e.handler_object_id;

COMMENT ON VIEW api.vw_rpc_exchange_info IS
    'RPC exchange analysis with age, duration, error status, and replay_sql for troubleshooting.';

-- ============================================================================
-- MCP Exchange Analysis View
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_mcp_exchange_info AS
SELECT
    e.sequence_number,
    e.object_id AS exchange_id,
    e.enqueued_at,
    now() - e.enqueued_at AS age,
    e.completed_at,
    e.completed_at - e.enqueued_at AS duration,

    e.mcp_type,
    e.mcp_name,

    ((e.response).envelope->>'error') IS NOT NULL AS is_error,

    e.handler_object_id,
    h.handler_function_name,

    CASE e.mcp_type
        WHEN 'tool' THEN format(
            $f$SELECT api.mcp_call_tool(%L, %L::jsonb, %L::jsonb, %L);$f$,
            e.mcp_name,
            COALESCE((e.request).arguments, '{}'::jsonb),
            (e.request).context,
            (e.request).request_id
        )
        WHEN 'resource' THEN format(
            $f$SELECT api.mcp_read_resource(%L, %L::jsonb, %L);$f$,
            (e.request).uri,
            (e.request).context,
            (e.request).request_id
        )
        WHEN 'prompt' THEN format(
            $f$SELECT api.mcp_get_prompt(%L, %L::jsonb, %L::jsonb, %L);$f$,
            e.mcp_name,
            COALESCE((e.request).arguments, '{}'::jsonb),
            (e.request).context,
            (e.request).request_id
        )
    END AS replay_sql

FROM api.mcp_exchange e
LEFT JOIN api.handler h ON h.object_id = e.handler_object_id;

COMMENT ON VIEW api.vw_mcp_exchange_info IS
    'MCP exchange analysis with age, duration, error status, and replay_sql for troubleshooting.';

-- ============================================================================
-- Exchange Statistics View (GROUPING SETS)
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_exchange_stats AS
WITH exchange_base AS (
    SELECT 'rest'::text AS protocol,
           (response).status_code,
           COALESCE((response).status_code >= 400, false) AS is_error,
           response IS NULL AS is_pending
    FROM api.rest_exchange
    UNION ALL
    SELECT 'rpc', (response).status_code,
           COALESCE((response).status_code >= 400, false),
           response IS NULL
    FROM api.rpc_exchange
    UNION ALL
    SELECT 'mcp_' || mcp_type, NULL,
           ((response).envelope->>'error') IS NOT NULL,
           false
    FROM api.mcp_exchange
)
SELECT
    protocol,
    status_code,
    is_error,
    is_pending,
    count(*) AS exchange_count,
    GROUPING(protocol) AS _grp_protocol,
    GROUPING(status_code) AS _grp_status,
    GROUPING(is_error) AS _grp_error,
    GROUPING(is_pending) AS _grp_pending
FROM exchange_base
GROUP BY GROUPING SETS (
    (),
    (protocol),
    (status_code),
    (is_error),
    (is_pending),
    (protocol, is_error),
    (protocol, is_pending),
    (protocol, status_code)
);

COMMENT ON VIEW api.vw_exchange_stats IS
    'Multi-dimensional exchange statistics using GROUPING SETS. Use _grp_* columns to identify aggregation level.';

-- ============================================================================
-- Exchange Summary Dashboard View (single row)
-- ============================================================================

CREATE OR REPLACE VIEW api.vw_exchange_summary AS
SELECT
    (SELECT count(*) FROM api.rest_exchange) +
    (SELECT count(*) FROM api.rpc_exchange) +
    (SELECT count(*) FROM api.mcp_exchange) AS total_exchanges,

    (SELECT count(*) FROM api.rest_exchange) AS rest_exchanges,
    (SELECT count(*) FROM api.rpc_exchange) AS rpc_exchanges,
    (SELECT count(*) FROM api.mcp_exchange) AS mcp_exchanges,

    (SELECT count(*) FROM api.rest_exchange WHERE response IS NULL) +
    (SELECT count(*) FROM api.rpc_exchange WHERE response IS NULL) AS pending_count,

    (SELECT count(*) FROM api.rest_exchange WHERE (response).status_code >= 400) +
    (SELECT count(*) FROM api.rpc_exchange WHERE (response).status_code >= 400) +
    (SELECT count(*) FROM api.mcp_exchange WHERE (response).envelope->>'error' IS NOT NULL) AS error_count,

    (SELECT avg(completed_at - enqueued_at) FROM api.rest_exchange WHERE completed_at IS NOT NULL) AS rest_avg_duration,
    (SELECT avg(completed_at - enqueued_at) FROM api.rpc_exchange WHERE completed_at IS NOT NULL) AS rpc_avg_duration,
    (SELECT avg(completed_at - enqueued_at) FROM api.mcp_exchange) AS mcp_avg_duration,

    LEAST(
        (SELECT min(enqueued_at) FROM api.rest_exchange WHERE response IS NULL),
        (SELECT min(enqueued_at) FROM api.rpc_exchange WHERE response IS NULL)
    ) AS oldest_pending_at;

COMMENT ON VIEW api.vw_exchange_summary IS
    'Single-row dashboard showing exchange counts, pending/error counts, and average durations by protocol.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.vw_rest_exchange_info - REST exchanges with replay_sql';
    RAISE NOTICE '  ✓ api.vw_rpc_exchange_info - RPC exchanges with replay_sql';
    RAISE NOTICE '  ✓ api.vw_mcp_exchange_info - MCP exchanges with replay_sql';
    RAISE NOTICE '  ✓ api.vw_exchange_stats - multi-dimensional statistics';
    RAISE NOTICE '  ✓ api.vw_exchange_summary - dashboard summary';
END $$;

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    -- Handler views
    EXECUTE format('GRANT SELECT ON api.vw_handler_info TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_stats TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_summary TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_info TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_stats TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_summary TO %I', v_admin_role);

    -- Route views
    EXECUTE format('GRANT SELECT ON api.vw_route_info TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_route_stats TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_route_summary TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_route_info TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_route_stats TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_route_summary TO %I', v_admin_role);

    -- Exchange views
    EXECUTE format('GRANT SELECT ON api.vw_rest_exchange_info TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_rpc_exchange_info TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_mcp_exchange_info TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_exchange_stats TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_exchange_summary TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_rest_exchange_info TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_rpc_exchange_info TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_mcp_exchange_info TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_exchange_stats TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_exchange_summary TO %I', v_admin_role);
END $$;
