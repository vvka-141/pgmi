/*
<pgmi-meta
    id="a7f01000-0010-4000-8000-000000000003"
    idempotent="true">
  <description>
    Admin analytics REST API: surfaces handler, route, and exchange views
    as JSON endpoints under the /admin/ namespace.
  </description>
  <sortKeys>
    <key>005/002</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE DEBUG '-> Installing admin analytics handlers'; END $$;

-- ============================================================================
-- GET /admin/dashboard
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000001',
        'uri', '^/admin/dashboard(\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'admin_dashboard',
        'description', 'Combined dashboard: handler, route, and exchange summaries',
        'requiresAuth', true,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'handlers', jsonb_build_object('type', 'object'),
                'routes', jsonb_build_object('type', 'object'),
                'exchanges', jsonb_build_object('type', 'object')
            ),
            'required', jsonb_build_array('handlers', 'routes', 'exchanges')
        )
    ),
    $body$
DECLARE
    v_handlers jsonb;
    v_routes jsonb;
    v_exchanges jsonb;
BEGIN
    SELECT jsonb_build_object(
        'totalHandlers', total_handlers,
        'restHandlers', rest_handlers,
        'rpcHandlers', rpc_handlers,
        'mcpHandlers', mcp_handlers,
        'requiresAuthCount', requires_auth_count,
        'oldestHandlerAt', oldest_handler_at,
        'newestHandlerAt', newest_handler_at
    ) INTO v_handlers FROM api.vw_handler_summary;

    SELECT jsonb_build_object(
        'totalRoutes', total_routes,
        'restRoutes', rest_routes,
        'rpcRoutes', rpc_routes,
        'mcpRoutes', mcp_routes,
        'authRequiredCount', auth_required_count,
        'autoLogCount', auto_log_count,
        'oldestRouteAt', oldest_route_at,
        'newestRouteAt', newest_route_at
    ) INTO v_routes FROM api.vw_route_summary;

    SELECT jsonb_build_object(
        'totalExchanges', total_exchanges,
        'restExchanges', rest_exchanges,
        'rpcExchanges', rpc_exchanges,
        'mcpExchanges', mcp_exchanges,
        'pendingCount', pending_count,
        'errorCount', error_count,
        'restAvgDuration', COALESCE(rest_avg_duration::text, null),
        'rpcAvgDuration', COALESCE(rpc_avg_duration::text, null),
        'mcpAvgDuration', COALESCE(mcp_avg_duration::text, null),
        'oldestPendingAt', oldest_pending_at
    ) INTO v_exchanges FROM api.vw_exchange_summary;

    RETURN api.json_response(200, jsonb_build_object(
        'handlers', v_handlers,
        'routes', v_routes,
        'exchanges', v_exchanges
    ));
END;
    $body$
);

-- ============================================================================
-- GET /admin/handlers
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000002',
        'uri', '^/admin/handlers(\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'admin_handlers',
        'description', 'List all handlers with health and binding info',
        'requiresAuth', true,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'handlers', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'object')),
                'total', jsonb_build_object('type', 'integer')
            ),
            'required', jsonb_build_array('handlers', 'total')
        )
    ),
    $body$
DECLARE
    v_q extensions.hstore;
    v_page record;
    v_items jsonb;
    v_total int;
BEGIN
    v_q := api.query_params((request).url);
    v_page := api.pagination_params(v_q);
    IF (v_page.o_error).status_code IS NOT NULL THEN
        RETURN v_page.o_error;
    END IF;

    SELECT COUNT(*) INTO v_total FROM api.vw_handler_info;

    SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'objectId', object_id,
        'handlerType', handler_type,
        'functionName', handler_function_name,
        'description', description,
        'createdAt', created_at,
        'age', age::text,
        'functionExists', function_exists,
        'definitionDrifted', definition_drifted,
        'restPattern', rest_pattern,
        'rpcMethod', rpc_method,
        'mcpName', mcp_name,
        'requiresAuth', requires_auth,
        'hasInputSchema', has_input_schema,
        'hasOutputSchema', has_output_schema,
        'schemaComplete', schema_complete
    )), '[]'::jsonb) INTO v_items
    FROM (
        SELECT * FROM api.vw_handler_info
        ORDER BY created_at DESC
        LIMIT v_page.o_limit OFFSET v_page.o_offset
    ) sub;

    RETURN api.json_response(200, jsonb_build_object(
        'handlers', v_items,
        'total', v_total,
        'limit', v_page.o_limit,
        'offset', v_page.o_offset
    ));
END;
    $body$
);

-- ============================================================================
-- GET /admin/handlers/stats
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000003',
        'uri', '^/admin/handlers/stats(\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'admin_handler_stats',
        'description', 'Handler statistics grouped by type, volatility, security, etc.',
        'requiresAuth', true,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'stats', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'object'))
            ),
            'required', jsonb_build_array('stats')
        )
    ),
    $body$
DECLARE
    v_items jsonb;
BEGIN
    SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'handlerType', handler_type,
        'volatility', volatility,
        'leakproof', leakproof,
        'security', security,
        'languageName', language_name,
        'requiresAuth', requires_auth,
        'handlerCount', handler_count
    )), '[]'::jsonb) INTO v_items
    FROM api.vw_handler_stats;

    RETURN api.json_response(200, jsonb_build_object('stats', v_items));
END;
    $body$
);

-- ============================================================================
-- GET /admin/routes
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000004',
        'uri', '^/admin/routes(\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'admin_routes',
        'description', 'Unified route listing across REST, RPC, and MCP',
        'requiresAuth', true,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'routes', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'object')),
                'total', jsonb_build_object('type', 'integer')
            ),
            'required', jsonb_build_array('routes', 'total')
        )
    ),
    $body$
DECLARE
    v_q extensions.hstore;
    v_page record;
    v_items jsonb;
    v_total int;
BEGIN
    v_q := api.query_params((request).url);
    v_page := api.pagination_params(v_q);
    IF (v_page.o_error).status_code IS NOT NULL THEN
        RETURN v_page.o_error;
    END IF;

    SELECT COUNT(*) INTO v_total FROM api.vw_route_info;

    SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'objectId', object_id,
        'routeType', route_type,
        'pattern', pattern,
        'methodRegexp', method_regexp,
        'mcpType', mcp_type,
        'functionName', handler_function_name,
        'requiresAuth', requires_auth,
        'autoLog', auto_log,
        'sequenceNumber', sequence_number,
        'volatility', volatility,
        'createdAt', created_at,
        'age', age::text
    )), '[]'::jsonb) INTO v_items
    FROM (
        SELECT * FROM api.vw_route_info
        ORDER BY sequence_number
        LIMIT v_page.o_limit OFFSET v_page.o_offset
    ) sub;

    RETURN api.json_response(200, jsonb_build_object(
        'routes', v_items,
        'total', v_total,
        'limit', v_page.o_limit,
        'offset', v_page.o_offset
    ));
END;
    $body$
);

-- ============================================================================
-- GET /admin/exchanges
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000005',
        'uri', '^/admin/exchanges(\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'admin_exchanges',
        'description', 'Paginated exchange list across REST, RPC, MCP with filters',
        'requiresAuth', true,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'exchanges', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'object')),
                'total', jsonb_build_object('type', 'integer')
            ),
            'required', jsonb_build_array('exchanges', 'total')
        )
    ),
    $body$
DECLARE
    v_q extensions.hstore;
    v_page record;
    v_protocol text;
    v_items jsonb;
    v_total int;
BEGIN
    v_q := api.query_params((request).url);
    v_page := api.pagination_params(v_q);
    IF (v_page.o_error).status_code IS NOT NULL THEN
        RETURN v_page.o_error;
    END IF;

    v_protocol := v_q -> 'protocol';

    WITH unified AS (
        SELECT 'rest' AS protocol, exchange_id, enqueued_at, duration,
               handler_function_name, response_status AS status_code,
               is_error, is_pending,
               request_method || ' ' || request_url AS summary
        FROM api.vw_rest_exchange_info
        WHERE v_protocol IS NULL OR v_protocol = 'rest'
        UNION ALL
        SELECT 'rpc', exchange_id, enqueued_at, duration,
               handler_function_name, response_status,
               is_error, is_pending,
               rpc_method
        FROM api.vw_rpc_exchange_info
        WHERE v_protocol IS NULL OR v_protocol = 'rpc'
        UNION ALL
        SELECT 'mcp', exchange_id, enqueued_at, duration,
               handler_function_name, NULL::int,
               is_error, false,
               mcp_type || ':' || mcp_name
        FROM api.vw_mcp_exchange_info
        WHERE v_protocol IS NULL OR v_protocol = 'mcp'
    )
    SELECT COUNT(*) INTO v_total FROM unified;

    WITH unified AS (
        SELECT 'rest' AS protocol, exchange_id, enqueued_at, duration,
               handler_function_name, response_status AS status_code,
               is_error, is_pending,
               request_method || ' ' || request_url AS summary
        FROM api.vw_rest_exchange_info
        WHERE v_protocol IS NULL OR v_protocol = 'rest'
        UNION ALL
        SELECT 'rpc', exchange_id, enqueued_at, duration,
               handler_function_name, response_status,
               is_error, is_pending,
               rpc_method
        FROM api.vw_rpc_exchange_info
        WHERE v_protocol IS NULL OR v_protocol = 'rpc'
        UNION ALL
        SELECT 'mcp', exchange_id, enqueued_at, duration,
               handler_function_name, NULL::int,
               is_error, false,
               mcp_type || ':' || mcp_name
        FROM api.vw_mcp_exchange_info
        WHERE v_protocol IS NULL OR v_protocol = 'mcp'
    )
    SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'protocol', protocol,
        'exchangeId', exchange_id,
        'enqueuedAt', enqueued_at,
        'duration', duration::text,
        'functionName', handler_function_name,
        'statusCode', status_code,
        'isError', is_error,
        'isPending', is_pending,
        'summary', summary
    ) ORDER BY enqueued_at DESC), '[]'::jsonb) INTO v_items
    FROM (SELECT * FROM unified ORDER BY enqueued_at DESC LIMIT v_page.o_limit OFFSET v_page.o_offset) sub;

    RETURN api.json_response(200, jsonb_build_object(
        'exchanges', v_items,
        'total', v_total,
        'limit', v_page.o_limit,
        'offset', v_page.o_offset
    ));
END;
    $body$
);

-- ============================================================================
-- GET /admin/exchanges/stats
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000006',
        'uri', '^/admin/exchanges/stats(\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'admin_exchange_stats',
        'description', 'Exchange statistics grouped by protocol, status, error, pending',
        'requiresAuth', true,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'stats', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'object'))
            ),
            'required', jsonb_build_array('stats')
        )
    ),
    $body$
DECLARE
    v_items jsonb;
BEGIN
    SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'protocol', protocol,
        'statusCode', status_code,
        'isError', is_error,
        'isPending', is_pending,
        'exchangeCount', exchange_count
    )), '[]'::jsonb) INTO v_items
    FROM api.vw_exchange_stats;

    RETURN api.json_response(200, jsonb_build_object('stats', v_items));
END;
    $body$
);

-- ============================================================================
-- GET /admin/exchanges/:id/replay
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000007',
        'uri', '^/admin/exchanges/([^/]+)/replay(\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'admin_exchange_replay',
        'description', 'Get replay SQL for a specific exchange by ID',
        'requiresAuth', true,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'exchangeId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'protocol', jsonb_build_object('type', 'string'),
                'replaySql', jsonb_build_object('type', 'string')
            ),
            'required', jsonb_build_array('exchangeId', 'protocol', 'replaySql')
        )
    ),
    $body$
DECLARE
    v_path text;
    v_parts text[];
    v_exchange_id uuid;
    v_result jsonb;
BEGIN
    v_path := split_part((request).url, '?', 1);
    v_parts := string_to_array(v_path, '/');
    v_exchange_id := common.try_cast(v_parts[4], null::uuid);

    IF v_exchange_id IS NULL THEN
        RETURN api.problem_response(400, 'Bad Request', 'Invalid exchange ID');
    END IF;

    SELECT jsonb_build_object(
        'exchangeId', exchange_id, 'protocol', 'rest',
        'replaySql', replay_sql
    ) INTO v_result
    FROM api.vw_rest_exchange_info WHERE exchange_id = v_exchange_id;

    IF v_result IS NULL THEN
        SELECT jsonb_build_object(
            'exchangeId', exchange_id, 'protocol', 'rpc',
            'replaySql', replay_sql
        ) INTO v_result
        FROM api.vw_rpc_exchange_info WHERE exchange_id = v_exchange_id;
    END IF;

    IF v_result IS NULL THEN
        SELECT jsonb_build_object(
            'exchangeId', exchange_id, 'protocol', 'mcp',
            'replaySql', replay_sql
        ) INTO v_result
        FROM api.vw_mcp_exchange_info WHERE exchange_id = v_exchange_id;
    END IF;

    IF v_result IS NULL THEN
        RETURN api.problem_response(404, 'Not Found', format('Exchange %s not found', v_exchange_id));
    END IF;

    RETURN api.json_response(200, v_result);
END;
    $body$
);

DO $$ BEGIN
    RAISE DEBUG '  + GET /admin/dashboard - combined summaries';
    RAISE DEBUG '  + GET /admin/handlers - handler list with health';
    RAISE DEBUG '  + GET /admin/handlers/stats - handler statistics';
    RAISE DEBUG '  + GET /admin/routes - unified route listing';
    RAISE DEBUG '  + GET /admin/exchanges - paginated exchange list';
    RAISE DEBUG '  + GET /admin/exchanges/stats - exchange statistics';
    RAISE DEBUG '  + GET /admin/exchanges/:id/replay - exchange replay SQL';
END $$;
