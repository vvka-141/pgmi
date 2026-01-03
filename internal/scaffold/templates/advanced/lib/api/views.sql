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
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.vw_handler_info TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_stats TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_summary TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_info TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_stats TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT ON api.vw_handler_summary TO %I', v_admin_role);
END $$;
