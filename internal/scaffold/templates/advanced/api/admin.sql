/*
<pgmi-meta
    id="a7f01000-0010-4000-8000-000000000003"
    idempotent="true">
  <description>
    Admin analytics REST API: surfaces handler, route, and exchange views
    as JSON endpoints under the /admin/ namespace, and the invitation-only
    path for organizations. Every endpoint declares the platform permission
    platform.operate, which only a platform operator in a session that used a
    second factor holds; the gateway refuses everyone else before the body
    runs. These surfaces expose cross-tenant operational data and a
    destructive purge.
  </description>
  <sortKeys>
    <key>005/002</key>
  </sortKeys>
</pgmi-meta>
*/


SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000001',
        'path', '/admin/dashboard',
        'httpMethod', '^GET$',
        'name', 'admin_dashboard',
        'description', 'Combined dashboard: handler, route, and exchange summaries',
        'permissions', jsonb_build_array('platform.operate'),
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
        'publicCount', public_count,
        'authenticatedCount', authenticated_count,
        'organizationCount', organization_count,
        'permissionCount', permission_count,
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
        'errorCount', error_count,
        'restAvgDuration', rest_avg_duration::text,
        'rpcAvgDuration', rpc_avg_duration::text,
        'mcpAvgDuration', mcp_avg_duration::text
    ) INTO v_exchanges FROM api.vw_exchange_summary;

    RETURN api.json_response(200, jsonb_build_object(
        'handlers', v_handlers,
        'routes', v_routes,
        'exchanges', v_exchanges
    ));
END;
    $body$
);


SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000002',
        'path', '/admin/handlers',
        'httpMethod', '^GET$',
        'name', 'admin_handlers',
        'description', 'List all handlers with health and binding info',
        'permissions', jsonb_build_array('platform.operate'),
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
    v_items jsonb;
    v_total int;
BEGIN
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
        'restPath', rest_path,
        'restMethod', rest_method_regexp,
        'unrouted', is_unrouted,
        'rpcMethod', rpc_method,
        'mcpName', mcp_name,
        'access', access,
        'hasInputSchema', has_input_schema,
        'hasOutputSchema', has_output_schema,
        'schemaComplete', schema_complete
    )), '[]'::jsonb) INTO v_items
    FROM (
        SELECT * FROM api.vw_handler_info
        ORDER BY created_at DESC
    ) sub;

    RETURN api.json_response(200, jsonb_build_object(
        'handlers', v_items,
        'total', v_total
    ));
END;
    $body$
);


SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000003',
        'path', '/admin/handlers/stats',
        'httpMethod', '^GET$',
        'name', 'admin_handler_stats',
        'description', 'Handler statistics grouped by type, volatility, security, etc.',
        'permissions', jsonb_build_array('platform.operate'),
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
        'access', access,
        'handlerCount', handler_count
    )), '[]'::jsonb) INTO v_items
    FROM api.vw_handler_stats;

    RETURN api.json_response(200, jsonb_build_object('stats', v_items));
END;
    $body$
);


SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000004',
        'path', '/admin/routes',
        'httpMethod', '^GET$',
        'name', 'admin_routes',
        'description', 'Unified route listing across REST, RPC, and MCP',
        'permissions', jsonb_build_array('platform.operate'),
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
    v_items jsonb;
    v_total int;
BEGIN
    SELECT COUNT(*) INTO v_total FROM api.vw_route_info;

    SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'objectId', object_id,
        'routeType', route_type,
        'pattern', pattern,
        'methodRegexp', method_regexp,
        'mcpType', mcp_type,
        'functionName', handler_function_name,
        'access', access,
        'autoLog', auto_log,
        'sequenceNumber', sequence_number,
        'volatility', volatility,
        'createdAt', created_at,
        'age', age::text
    )), '[]'::jsonb) INTO v_items
    FROM (
        SELECT * FROM api.vw_route_info
        ORDER BY sequence_number
    ) sub;

    RETURN api.json_response(200, jsonb_build_object(
        'routes', v_items,
        'total', v_total
    ));
END;
    $body$
);


SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000005',
        'path', '/admin/exchanges',
        'httpMethod', '^GET$',
        'name', 'admin_exchanges',
        'description', 'Paginated exchange list across REST, RPC, MCP, newest first; actualPrincipalId narrows it to the exchanges one principal made',
        'permissions', jsonb_build_array('platform.operate'),
        'querySchema', api.page_query_schema(jsonb_build_object(
            'protocol', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('rest', 'rpc', 'mcp')),
            'actualPrincipalId', jsonb_build_object('type', 'string', 'format', 'uuid'))),
        'outputSchema', api.page_output_schema(jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'protocol', jsonb_build_object('type', 'string'),
                'exchangeId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'enqueuedAt', jsonb_build_object('type', 'string', 'format', 'date-time'),
                'statusCode', jsonb_build_object('type', jsonb_build_array('integer', 'null')),
                'isError', jsonb_build_object('type', 'boolean'),
                'summary', jsonb_build_object('type', jsonb_build_array('string', 'null'))),
            'required', jsonb_build_array('protocol', 'exchangeId', 'enqueuedAt')))
    ),
    $body$
DECLARE
    v_q        extensions.hstore := api.query_params((request).url);
    v_page     record := api.pagination_params(v_q);
    v_protocol text := v_q -> 'protocol';
    v_actual   text := common.try_cast(v_q -> 'actualPrincipalId', NULL::uuid)::text;
    v_keys     jsonb;
    v_items    jsonb;
    v_total    bigint;
BEGIN
    IF v_actual IS NULL THEN
        SELECT jsonb_agg(k),
               (SELECT count(*) FROM api.exchange e
                WHERE v_protocol IS NULL OR e.tableoid = ('api.' || v_protocol || '_exchange')::regclass)
        INTO v_keys, v_total
        FROM (SELECT protocol, exchange_id, enqueued_at
              FROM api.vw_exchange
              WHERE v_protocol IS NULL OR protocol = v_protocol
              ORDER BY enqueued_at DESC
              LIMIT v_page.o_limit OFFSET v_page.o_offset) k;
    ELSE
        SELECT jsonb_agg(k),
               (SELECT count(*) FROM api.vw_exchange
                WHERE (v_protocol IS NULL OR protocol = v_protocol) AND context->>'actualPrincipalId' = v_actual)
        INTO v_keys, v_total
        FROM (SELECT protocol, exchange_id, enqueued_at
              FROM api.vw_exchange
              WHERE (v_protocol IS NULL OR protocol = v_protocol) AND context->>'actualPrincipalId' = v_actual
              ORDER BY enqueued_at DESC
              LIMIT v_page.o_limit OFFSET v_page.o_offset) k;
    END IF;

    SELECT jsonb_agg(jsonb_build_object(
               'protocol', p.protocol,
               'exchangeId', p.exchange_id,
               'enqueuedAt', p.enqueued_at,
               'duration', COALESCE(r.duration, c.duration, m.duration)::text,
               'functionName', COALESCE(r.handler_function_name, c.handler_function_name, m.handler_function_name),
               'statusCode', COALESCE(r.response_status, c.response_status),
               'isError', COALESCE(r.is_error, c.is_error, m.is_error),
               'summary', CASE p.protocol
                              WHEN 'rest' THEN r.request_method || ' ' || r.request_url
                              WHEN 'rpc' THEN c.rpc_method
                              ELSE m.mcp_type || ':' || m.mcp_name
                          END
           ) ORDER BY p.enqueued_at DESC)
    INTO v_items
    FROM jsonb_to_recordset(v_keys) AS p(protocol text, exchange_id uuid, enqueued_at timestamptz)
    LEFT JOIN api.vw_rest_exchange_info r ON p.protocol = 'rest' AND r.exchange_id = p.exchange_id
    LEFT JOIN api.vw_rpc_exchange_info c ON p.protocol = 'rpc' AND c.exchange_id = p.exchange_id
    LEFT JOIN api.vw_mcp_exchange_info m ON p.protocol = 'mcp' AND m.exchange_id = p.exchange_id;

    RETURN api.page_response(v_items, v_total, v_page.o_limit, v_page.o_offset);
END;
    $body$
);


SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000006',
        'path', '/admin/exchanges/stats',
        'httpMethod', '^GET$',
        'name', 'admin_exchange_stats',
        'description', 'Exchange statistics grouped by protocol, status and error',
        'permissions', jsonb_build_array('platform.operate'),
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
        'exchangeCount', exchange_count
    )), '[]'::jsonb) INTO v_items
    FROM api.vw_exchange_stats;

    RETURN api.json_response(200, jsonb_build_object('stats', v_items));
END;
    $body$
);


SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000007',
        'path', '/admin/exchanges/{exchangeId:uuid}/replay',
        'httpMethod', '^GET$',
        'name', 'admin_exchange_replay',
        'description', 'Get replay SQL for a specific exchange by ID',
        'permissions', jsonb_build_array('platform.operate'),
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
    v_exchange_id uuid := ((request).path_params->>'exchangeId')::uuid;
    v_result jsonb;
BEGIN
    SELECT jsonb_build_object('exchangeId', exchange_id, 'protocol', protocol, 'replaySql', replay_sql)
    INTO v_result
    FROM (SELECT 1, 'rest', exchange_id, replay_sql FROM api.vw_rest_exchange_info WHERE exchange_id = v_exchange_id
          UNION ALL
          SELECT 2, 'rpc', exchange_id, replay_sql FROM api.vw_rpc_exchange_info WHERE exchange_id = v_exchange_id
          UNION ALL
          SELECT 3, 'mcp', exchange_id, replay_sql FROM api.vw_mcp_exchange_info WHERE exchange_id = v_exchange_id
         ) e(rank, protocol, exchange_id, replay_sql)
    ORDER BY rank
    LIMIT 1;

    IF v_result IS NULL THEN
        RETURN api.problem_response(404, 'Not Found', format('Exchange %s not found', v_exchange_id), code => 'api.exchange.not_found');
    END IF;

    RETURN api.json_response(200, v_result);
END;
    $body$
);


SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000008',
        'path', '/admin/maintenance/purge-exchanges',
        'httpMethod', '^POST$',
        'name', 'admin_purge_exchanges',
        'description', 'Single-batch exchange purge (admin only); POST to avoid CSRF/prefetch on a destructive operation',
        'permissions', jsonb_build_array('platform.operate'),
        'querySchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object(
            'retention_days', jsonb_build_object('type', 'integer', 'minimum', 1,
                'description', 'Keep this many days; defaults to the deploy parameter exchange_retention_days'),
            'batch_size', jsonb_build_object('type', 'integer', 'minimum', 1, 'maximum', 50000, 'default', 5000))),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'deleted', jsonb_build_object('type', 'object',
                    'properties', jsonb_build_object(
                        'rest', jsonb_build_object('type', 'integer'),
                        'rpc', jsonb_build_object('type', 'integer'),
                        'mcp', jsonb_build_object('type', 'integer')
                    )),
                'totalDeleted', jsonb_build_object('type', 'integer'),
                'cutoff', jsonb_build_object('type', 'string', 'format', 'date-time'),
                'hasMore', jsonb_build_object('type', 'boolean')
            ),
            'required', jsonb_build_array('deleted', 'totalDeleted', 'cutoff', 'hasMore')
        )
    ),
    $body$
DECLARE
    v_q      extensions.hstore := api.query_params((request).url);
    v_cutoff timestamptz := now() - COALESCE(make_interval(days => (v_q->'retention_days')::int), api.exchange_retention());
    v_batch  record;
BEGIN
    SELECT * INTO v_batch FROM api.purge_exchanges_batch(v_cutoff, COALESCE((v_q->'batch_size')::int, 5000));

    RETURN api.json_response(200, jsonb_build_object(
        'deleted', jsonb_build_object(
            'rest', v_batch.rest_deleted,
            'rpc', v_batch.rpc_deleted,
            'mcp', v_batch.mcp_deleted
        ),
        'totalDeleted', v_batch.rest_deleted + v_batch.rpc_deleted + v_batch.mcp_deleted,
        'cutoff', v_cutoff,
        'hasMore', EXISTS (SELECT FROM api.exchange WHERE enqueued_at < v_cutoff)
    ));
END;
    $body$
);

-- Runs the logged request again as its caller and rolls it back
-- (api.replay_exchange): what would it answer now?

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-00000000000a',
        'path', '/admin/exchanges/{exchangeId:uuid}/replay',
        'httpMethod', '^POST$',
        'name', 'admin_replay_exchange',
        'description', 'Replay a logged REST, RPC or MCP exchange in its logged context, rolled back; answers the replayed response',
        'permissions', jsonb_build_array('platform.operate'),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'exchangeId', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'status', jsonb_build_object('type', 'integer'),
                'headers', jsonb_build_object('type', 'object'),
                'body', jsonb_build_object('type', 'string')
            ),
            'required', jsonb_build_array('exchangeId', 'status', 'headers')
        )
    ),
    $body$
DECLARE
    v_exchange_id uuid := ((request).path_params->>'exchangeId')::uuid;
    v_response api.http_response;
BEGIN
    IF NOT EXISTS (SELECT FROM api.rest_exchange WHERE object_id = v_exchange_id
                   UNION ALL
                   SELECT FROM api.rpc_exchange WHERE object_id = v_exchange_id
                   UNION ALL
                   SELECT FROM api.mcp_exchange WHERE object_id = v_exchange_id) THEN
        RETURN api.problem_response(404, 'Not Found', format('No exchange %s', v_exchange_id), code => 'api.exchange.not_found');
    END IF;

    v_response := api.replay_exchange(v_exchange_id);
    RETURN api.json_response(200, jsonb_strip_nulls(jsonb_build_object(
        'exchangeId', v_exchange_id,
        'status', (v_response).status_code,
        'headers', extensions.hstore_to_jsonb((v_response).headers),
        'body', convert_from((v_response).content, 'UTF8'))));
END;
    $body$
);

-- The default, invitation-only path: an operator creates a pending
-- organization and invites its owner; it becomes active when that person
-- signs in with the verified address and accepts.

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0005-4000-8000-000000000009',
        'path', '/admin/organizations',
        'httpMethod', '^POST$',
        'name', 'admin_create_organization',
        'description', 'Create a pending organization and invite its owner by email',
        'permissions', jsonb_build_array('platform.operate'),
        'successStatus', 201,
        'noLocation', 'GET /organizations/{id} shows only the caller''s own organizations',
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'name', jsonb_build_object('type', 'string'),
                'slug', jsonb_build_object('type', 'string'),
                'ownerEmail', jsonb_build_object('type', 'string', 'format', 'email')
            ),
            'required', jsonb_build_array('name', 'slug', 'ownerEmail')
        ),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'id', jsonb_build_object('type', 'string', 'format', 'uuid'),
                'name', jsonb_build_object('type', 'string'),
                'slug', jsonb_build_object('type', 'string'),
                'status', jsonb_build_object('type', 'string', 'const', 'pending')
            ),
            'required', jsonb_build_array('id', 'name', 'slug', 'status')
        )
    ),
    $body$
DECLARE
    v_b       jsonb := api.content_json((request).content);
    v_name    text := v_b->>'name';
    v_slug    text := v_b->>'slug';
    v_email   text := v_b->>'ownerEmail';
    v_problem text;
    v_org     membership.organization;
BEGIN
    IF NOT coalesce(membership.fn_is_well_formed_email(membership.fn_normalize_email(v_email)), false) THEN
        RETURN api.problem_response(422, 'Unprocessable Content', 'ownerEmail is not a valid email address',
                                    code => 'membership.invitation.email_invalid',
                                    invalid_params => jsonb_build_array(api.invalid_param('ownerEmail', 'invalid')));
    END IF;
    v_problem := membership.fn_organization_problem(v_name, v_slug);
    IF v_problem IS NULL THEN
        v_org := membership.fn_create_organization(v_name, v_slug, v_email);
        IF v_org.object_id IS NULL THEN
            -- Another request took the slug between the probe and the insert.
            v_problem := coalesce(membership.fn_organization_problem(v_name, v_slug), 'membership.organization.slug_taken');
        END IF;
    END IF;
    IF v_problem IS NOT NULL THEN
        RETURN api.problem_response(422, 'Unprocessable Content', 'The name or slug is not valid', code => v_problem,
            invalid_params => jsonb_build_array(CASE v_problem
                WHEN 'membership.organization.name_invalid' THEN api.invalid_param('name', 'invalid')
                WHEN 'membership.organization.slug_taken' THEN api.invalid_param('slug', 'duplicate')
                WHEN 'membership.organization.slug_reserved' THEN api.invalid_param('slug', 'reserved')
                ELSE api.invalid_param('slug', 'invalid') END));
    END IF;
    RETURN api.json_response(201, jsonb_build_object(
        'id', v_org.object_id, 'name', v_org.name, 'slug', v_org.slug, 'status', v_org.status));
END;
    $body$
);
