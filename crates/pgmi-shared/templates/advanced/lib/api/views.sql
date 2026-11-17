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

-- ============================================================================
-- Handler Analysis View
-- ============================================================================

SELECT core.ensure_view('api.vw_handler_info', $view$
CREATE OR REPLACE VIEW api.vw_handler_info WITH (security_invoker = true) AS
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

    rr.handler_object_id IS NOT NULL AS has_rest_route,
    pr.handler_object_id IS NOT NULL AS has_rpc_route,
    mr.handler_object_id IS NOT NULL AS has_mcp_route,
    rr.handler_object_id IS NULL AND pr.handler_object_id IS NULL AND mr.handler_object_id IS NULL AS is_unrouted,

    rr.canonical_path AS rest_path,
    rr.method_regexp AS rest_method_regexp,
    rr.address_regexp AS rest_pattern,
    pr.method_name AS rpc_method,
    mr.mcp_name,
    mr.mcp_type,

    array_to_string(h.accepts, ', ') AS accepts_formatted,
    array_to_string(h.produces, ', ') AS produces_formatted,
    h.access,

    h.input_json_schema IS NOT NULL AS has_input_schema,
    h.output_json_schema IS NOT NULL AS has_output_schema,
    h.input_json_schema IS NOT NULL AND h.output_json_schema IS NOT NULL AS schema_complete,

    h.returns_type::text AS returns_type_name,
    h.returns_set,
    h.volatility,
    h.parallel,
    h.leakproof,
    h.security,
    h.language_name,
    h.owner_name,
    h.min_transaction_isolation,
    h.read_only

FROM api.handler h
LEFT JOIN api.rest_route rr ON rr.handler_object_id = h.object_id
LEFT JOIN api.rpc_route pr ON pr.handler_object_id = h.object_id
LEFT JOIN api.mcp_route mr ON mr.handler_object_id = h.object_id
$view$);

COMMENT ON VIEW api.vw_handler_info IS
    'One row per registered handler: lifecycle age, health checks (function_exists, definition_drifted) and its route as values, each NULL when the handler has no route of that kind.';
COMMENT ON COLUMN api.vw_handler_info.rest_path IS
    'The REST route''s path template without parameter types (api.rest_route.canonical_path); NULL without a REST route.';
COMMENT ON COLUMN api.vw_handler_info.rest_method_regexp IS
    'The REST route''s method pattern, e.g. ^GET$; NULL without a REST route.';
COMMENT ON COLUMN api.vw_handler_info.is_unrouted IS
    'True when the handler has no REST, RPC or MCP route, so no request reaches it. It may be deliberate; a human decides.';

-- ============================================================================
-- Handler Statistics View (GROUPING SETS)
-- ============================================================================

SELECT core.ensure_view('api.vw_handler_stats', $view$
CREATE OR REPLACE VIEW api.vw_handler_stats WITH (security_invoker = true) AS
SELECT
    handler_type,
    volatility,
    leakproof,
    security,
    language_name,
    access,
    count(*) AS handler_count,

    GROUPING(handler_type) AS _grp_type,
    GROUPING(volatility) AS _grp_volatility,
    GROUPING(leakproof) AS _grp_leakproof,
    GROUPING(security) AS _grp_security,
    GROUPING(language_name) AS _grp_language,
    GROUPING(access) AS _grp_access
FROM api.handler
GROUP BY GROUPING SETS (
    (),
    (handler_type),
    (volatility),
    (language_name),
    (security),
    (access),
    (leakproof),
    (handler_type, volatility),
    (handler_type, language_name),
    (handler_type, access)
)
$view$);

COMMENT ON VIEW api.vw_handler_stats IS
    'Multi-dimensional handler statistics using GROUPING SETS. Use _grp_* columns to identify aggregation level (1=aggregated, 0=specific value).';

-- ============================================================================
-- Handler Summary Dashboard View (single row)
-- ============================================================================

SELECT core.ensure_view('api.vw_handler_summary', $view$
CREATE OR REPLACE VIEW api.vw_handler_summary WITH (security_invoker = true) AS
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
    count(*) FILTER (WHERE access = 'public') AS public_count,
    count(*) FILTER (WHERE access = 'authenticated') AS authenticated_count,
    count(*) FILTER (WHERE access = 'permission') AS permission_count,
    count(DISTINCT language_name) AS language_count,
    count(DISTINCT owner_name) AS owner_count,
    min(created_at) AS oldest_handler_at,
    max(created_at) AS newest_handler_at,
    count(*) FILTER (WHERE access = 'organization') AS organization_count
FROM api.handler
$view$);

COMMENT ON VIEW api.vw_handler_summary IS
    'Single-row dashboard showing handler counts by type, volatility, security model, and other characteristics.';

-- ============================================================================
-- Route Views
-- ============================================================================

-- ============================================================================
-- Unified Route Analysis View
-- ============================================================================

SELECT core.ensure_view('api.vw_route_info', $view$
CREATE OR REPLACE VIEW api.vw_route_info WITH (security_invoker = true) AS
-- REST routes
SELECT
    r.handler_object_id AS object_id,
    'rest'::text AS route_type,
    r.canonical_path AS pattern,
    r.method_regexp,
    NULL::text AS mcp_type,
    h.handler_function_name,
    h.access,
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
    h.access,
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
    h.access,
    true,
    NULL,
    h.volatility,
    h.created_at,
    now() - h.created_at
FROM api.mcp_route r
JOIN api.handler h ON h.object_id = r.handler_object_id
$view$);

COMMENT ON VIEW api.vw_route_info IS
    'Unified route view across all protocols. Shows pattern, handler, auth requirements, and age.';

-- ============================================================================
-- Route Statistics View (GROUPING SETS)
-- ============================================================================

SELECT core.ensure_view('api.vw_route_stats', $view$
CREATE OR REPLACE VIEW api.vw_route_stats WITH (security_invoker = true) AS
WITH route_base AS (
    SELECT route_type, access, auto_log, volatility
    FROM api.vw_route_info
)
SELECT
    route_type,
    access,
    auto_log,
    volatility,
    count(*) AS route_count,
    GROUPING(route_type) AS _grp_type,
    GROUPING(access) AS _grp_access,
    GROUPING(auto_log) AS _grp_log,
    GROUPING(volatility) AS _grp_volatility
FROM route_base
GROUP BY GROUPING SETS (
    (),
    (route_type),
    (access),
    (auto_log),
    (volatility),
    (route_type, access),
    (route_type, volatility)
)
$view$);

COMMENT ON VIEW api.vw_route_stats IS
    'Multi-dimensional route statistics using GROUPING SETS. Use _grp_* columns to identify aggregation level.';

-- ============================================================================
-- Route Summary Dashboard View (single row)
-- ============================================================================

SELECT core.ensure_view('api.vw_route_summary', $view$
CREATE OR REPLACE VIEW api.vw_route_summary WITH (security_invoker = true) AS
SELECT
    count(*) AS total_routes,
    count(*) FILTER (WHERE route_type = 'rest') AS rest_routes,
    count(*) FILTER (WHERE route_type = 'rpc') AS rpc_routes,
    count(*) FILTER (WHERE route_type LIKE 'mcp_%') AS mcp_routes,
    count(*) FILTER (WHERE access <> 'public') AS auth_required_count,
    count(*) FILTER (WHERE auto_log) AS auto_log_count,
    min(created_at) AS oldest_route_at,
    max(created_at) AS newest_route_at
FROM api.vw_route_info
$view$);

COMMENT ON VIEW api.vw_route_summary IS
    'Single-row dashboard showing route counts by protocol type and configuration.';

-- ============================================================================
-- Exchange Views (Request/Response Logging with Replay SQL)
-- ============================================================================

-- ============================================================================
-- Exchange View
-- ============================================================================

SELECT core.ensure_view('api.vw_exchange', $view$
CREATE OR REPLACE VIEW api.vw_exchange WITH (security_invoker = true) AS
SELECT 'rest' AS protocol, e.sequence_number, e.object_id AS exchange_id, e.enqueued_at, e.completed_at,
       e.handler_object_id, e.context, (e.request).headers OPERATOR(extensions.->) 'x-request-id' AS request_id,
       (e.response).status_code AS response_status, e.actual_principal_id
FROM api.rest_exchange e
UNION ALL
SELECT 'rpc', e.sequence_number, e.object_id, e.enqueued_at, e.completed_at,
       e.handler_object_id, e.context, (e.request).headers OPERATOR(extensions.->) 'x-request-id',
       (e.response).status_code, e.actual_principal_id
FROM api.rpc_exchange e
UNION ALL
SELECT 'mcp', e.sequence_number, e.object_id, e.enqueued_at, e.completed_at,
       e.handler_object_id, e.context, NULL,
       NULL, e.actual_principal_id
FROM api.mcp_exchange e
$view$);

COMMENT ON VIEW api.vw_exchange IS
    'Every logged exchange across protocols: the logged context, the transport''s x-request-id (REST, RPC) and the HTTP status. Find the row a caller quotes with WHERE request_id = ...';

-- ============================================================================
-- REST Exchange Analysis View
-- ============================================================================

SELECT core.ensure_view('api.vw_rest_exchange_info', $view$
CREATE OR REPLACE VIEW api.vw_rest_exchange_info WITH (security_invoker = true) AS
SELECT
    e.sequence_number,
    e.object_id AS exchange_id,
    e.enqueued_at,
    now() - e.enqueued_at AS age,
    e.completed_at,
    e.completed_at - e.enqueued_at AS duration,

    (e.request).method AS request_method,
    (e.request).url AS request_url,

    (e.response).status_code AS response_status,
    COALESCE((e.response).status_code >= 400, false) AS is_error,

    e.handler_object_id,
    h.handler_function_name,

    format($f$SELECT * FROM api.replay_exchange(%L);$f$, e.object_id) AS replay_sql

FROM api.rest_exchange e
LEFT JOIN api.handler h ON h.object_id = e.handler_object_id
$view$);

COMMENT ON VIEW api.vw_rest_exchange_info IS
    'REST exchange analysis with age, duration, error status, and replay_sql: api.replay_exchange re-runs the request in its logged context and rolls it back.';

-- ============================================================================
-- RPC Exchange Analysis View
-- ============================================================================

SELECT core.ensure_view('api.vw_rpc_exchange_info', $view$
CREATE OR REPLACE VIEW api.vw_rpc_exchange_info WITH (security_invoker = true) AS
SELECT
    e.sequence_number,
    e.object_id AS exchange_id,
    e.enqueued_at,
    now() - e.enqueued_at AS age,
    e.completed_at,
    e.completed_at - e.enqueued_at AS duration,

    r.method_name AS rpc_method,

    (e.response).status_code AS response_status,
    COALESCE((e.response).status_code >= 400, false) AS is_error,

    e.handler_object_id,
    h.handler_function_name,

    format($f$SELECT * FROM api.replay_exchange(%L);$f$, e.object_id) AS replay_sql

FROM api.rpc_exchange e
LEFT JOIN api.handler h ON h.object_id = e.handler_object_id
LEFT JOIN api.rpc_route r ON r.handler_object_id = e.handler_object_id
$view$);

COMMENT ON VIEW api.vw_rpc_exchange_info IS
    'RPC exchange analysis with age, duration, error status, and replay_sql: api.replay_exchange re-runs the request in its logged context and rolls it back.';

-- ============================================================================
-- MCP Exchange Analysis View
-- ============================================================================

SELECT core.ensure_view('api.vw_mcp_exchange_info', $view$
CREATE OR REPLACE VIEW api.vw_mcp_exchange_info WITH (security_invoker = true) AS
SELECT
    e.sequence_number,
    e.object_id AS exchange_id,
    e.enqueued_at,
    now() - e.enqueued_at AS age,
    e.completed_at,
    e.completed_at - e.enqueued_at AS duration,

    e.mcp_type,
    e.mcp_name,

    internal.mcp_is_error((e.response).envelope) AS is_error,

    e.handler_object_id,
    h.handler_function_name,

    format($f$SELECT * FROM api.replay_exchange(%L);$f$, e.object_id) AS replay_sql

FROM api.mcp_exchange e
LEFT JOIN api.handler h ON h.object_id = e.handler_object_id
$view$);

COMMENT ON VIEW api.vw_mcp_exchange_info IS
    'MCP exchange analysis with age, duration, error status (a JSON-RPC error or a tool result with isError), and replay_sql: api.replay_exchange re-runs the call in its logged context and rolls it back.';

-- ============================================================================
-- Exchange Statistics View (GROUPING SETS)
-- ============================================================================

SELECT core.ensure_view('api.vw_exchange_stats', $view$
CREATE OR REPLACE VIEW api.vw_exchange_stats WITH (security_invoker = true) AS
WITH exchange_base AS (
    SELECT 'rest'::text AS protocol,
           (response).status_code,
           COALESCE((response).status_code >= 400, false) AS is_error
    FROM api.rest_exchange
    UNION ALL
    SELECT 'rpc', (response).status_code,
           COALESCE((response).status_code >= 400, false)
    FROM api.rpc_exchange
    UNION ALL
    SELECT 'mcp_' || mcp_type, NULL,
           internal.mcp_is_error((response).envelope)
    FROM api.mcp_exchange
)
SELECT
    protocol,
    status_code,
    is_error,
    count(*) AS exchange_count,
    GROUPING(protocol) AS _grp_protocol,
    GROUPING(status_code) AS _grp_status,
    GROUPING(is_error) AS _grp_error
FROM exchange_base
GROUP BY GROUPING SETS (
    (),
    (protocol),
    (status_code),
    (is_error),
    (protocol, is_error),
    (protocol, status_code)
)
$view$);

COMMENT ON VIEW api.vw_exchange_stats IS
    'Multi-dimensional exchange statistics using GROUPING SETS. Use _grp_* columns to identify aggregation level.';

-- ============================================================================
-- Exchange Summary Dashboard View (single row)
-- ============================================================================

SELECT core.ensure_view('api.vw_exchange_summary', $view$
CREATE OR REPLACE VIEW api.vw_exchange_summary WITH (security_invoker = true) AS
WITH
    rest_stats AS (
        SELECT
            count(*) AS total,
            count(*) FILTER (WHERE (response).status_code >= 400) AS errors,
            avg(completed_at - enqueued_at) FILTER (WHERE completed_at IS NOT NULL) AS avg_duration
        FROM api.rest_exchange
    ),
    rpc_stats AS (
        SELECT
            count(*) AS total,
            count(*) FILTER (WHERE (response).status_code >= 400) AS errors,
            avg(completed_at - enqueued_at) FILTER (WHERE completed_at IS NOT NULL) AS avg_duration
        FROM api.rpc_exchange
    ),
    mcp_stats AS (
        SELECT
            count(*) AS total,
            count(*) FILTER (WHERE internal.mcp_is_error((response).envelope)) AS errors,
            avg(completed_at - enqueued_at) AS avg_duration
        FROM api.mcp_exchange
    )
SELECT
    (SELECT total FROM rest_stats) + (SELECT total FROM rpc_stats) + (SELECT total FROM mcp_stats) AS total_exchanges,
    (SELECT total FROM rest_stats) AS rest_exchanges,
    (SELECT total FROM rpc_stats) AS rpc_exchanges,
    (SELECT total FROM mcp_stats) AS mcp_exchanges,
    (SELECT errors FROM rest_stats) + (SELECT errors FROM rpc_stats) + (SELECT errors FROM mcp_stats) AS error_count,
    (SELECT avg_duration FROM rest_stats) AS rest_avg_duration,
    (SELECT avg_duration FROM rpc_stats) AS rpc_avg_duration,
    (SELECT avg_duration FROM mcp_stats) AS mcp_avg_duration
$view$);

COMMENT ON VIEW api.vw_exchange_summary IS
    'Single-row dashboard showing exchange counts, error counts, and average durations by protocol.';

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_admin_role text := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.vw_handler_info, api.vw_handler_stats, api.vw_handler_summary, '
                   'api.vw_route_info, api.vw_route_stats, api.vw_route_summary, '
                   'api.vw_exchange, api.vw_rest_exchange_info, api.vw_rpc_exchange_info, api.vw_mcp_exchange_info, '
                   'api.vw_exchange_stats, api.vw_exchange_summary TO %I', v_admin_role);
END $$;
