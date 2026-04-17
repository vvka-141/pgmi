-- ============================================================================
-- Test: JSON Schema persistence, $schema injection, and MCP tags
-- ============================================================================
-- Validates Wave 2 library primitives:
--   • api.json_schema domain stores valid schemas, rejects malformed ones
--   • output_json_schema persists on handler registration
--   • x-include-schema response header triggers $schema merge on REST/RPC
--   • mcp_list_tools returns outputSchema and tags by default
--   • mcp_list_tools(p_tags) filters by tag overlap
-- ============================================================================

DO $$
DECLARE
    v_rest_handler_id uuid := 'ffffffff-0002-4000-8000-000000000001';
    v_rpc_handler_id  uuid := 'ffffffff-0002-4000-8000-000000000002';
    v_mcp_handler_a   uuid := 'ffffffff-0002-4000-8000-000000000003';
    v_mcp_handler_b   uuid := 'ffffffff-0002-4000-8000-000000000004';
    v_http_response   api.http_response;
    v_response_body   jsonb;
    v_list            jsonb;
    v_stored_schema   jsonb;
    v_malformed_ok    boolean;
BEGIN
    RAISE NOTICE '-> Testing JSON Schema persistence, injection, and tags';

    -- ========================================================================
    -- json_schema domain rejects malformed schemas at DDL time
    -- ========================================================================

    BEGIN
        PERFORM '{"type": 42}'::jsonb::api.json_schema;
        v_malformed_ok := true;
    EXCEPTION WHEN check_violation THEN
        v_malformed_ok := false;
    END;

    IF v_malformed_ok THEN
        RAISE EXCEPTION 'api.json_schema accepted malformed type keyword (expected check_violation)';
    END IF;

    RAISE NOTICE '  + api.json_schema rejects malformed top-level keywords';

    -- ========================================================================
    -- REST: output_json_schema persists + x-include-schema merges $schema
    -- ========================================================================

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_rest_handler_id,
            'uri', '^/schema-rest(\?.*)?$',
            'httpMethod', '^GET$',
            'name', 'schema_rest_test',
            'requiresAuth', false,
            'outputSchema', jsonb_build_object(
                'type', 'object',
                'properties', jsonb_build_object(
                    'ok', jsonb_build_object('type', 'boolean', 'description', 'Always true')
                )
            ),
            'responseHeaders', jsonb_build_object('x-include-schema', 'true')
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('ok', true));
END;
        $body$
    );

    SELECT output_json_schema::jsonb INTO v_stored_schema
    FROM api.handler WHERE object_id = v_rest_handler_id;

    IF v_stored_schema IS NULL OR NOT v_stored_schema ? 'properties' THEN
        RAISE EXCEPTION 'REST output_json_schema not persisted: %', v_stored_schema;
    END IF;

    RAISE NOTICE '  + REST handler output_json_schema persisted';

    v_http_response := api.rest_invoke('GET', '/schema-rest', ''::extensions.hstore, NULL::bytea);
    v_response_body := api.content_json((v_http_response).content);

    IF NOT v_response_body ? '$schema' THEN
        RAISE EXCEPTION 'REST response missing $schema key (x-include-schema=true): %', v_response_body;
    END IF;

    IF v_response_body->'$schema'->'properties'->>'ok' IS NULL THEN
        RAISE EXCEPTION 'REST $schema merge corrupted: %', v_response_body;
    END IF;

    RAISE NOTICE '  + REST response body contains $schema merged from output_json_schema';

    -- ========================================================================
    -- RPC: same flow, x-include-schema triggers injection
    -- ========================================================================

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', v_rpc_handler_id,
            'methodName', 'schema.rpc.test',
            'requiresAuth', false,
            'outputSchema', jsonb_build_object(
                'type', 'object',
                'properties', jsonb_build_object(
                    'n', jsonb_build_object('type', 'integer')
                )
            ),
            'responseHeaders', jsonb_build_object('x-include-schema', 'true')
        ),
        $body$
BEGIN
    RETURN api.jsonrpc_success(
        jsonb_build_object('n', 42),
        api.content_json((request).content)->'id'
    );
END;
        $body$
    );

    v_http_response := api.rpc_invoke(
        api.rpc_resolve('schema.rpc.test'),
        ''::extensions.hstore,
        convert_to('{"jsonrpc":"2.0","method":"schema.rpc.test","params":{},"id":1}', 'UTF8')
    );
    v_response_body := api.content_json((v_http_response).content);

    IF NOT v_response_body ? '$schema' THEN
        RAISE EXCEPTION 'RPC response missing $schema key (x-include-schema=true): %', v_response_body;
    END IF;

    RAISE NOTICE '  + RPC response body contains $schema merged from output_json_schema';

    -- ========================================================================
    -- MCP: outputSchema appears in mcp_list_tools; tags filter works
    -- ========================================================================

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', v_mcp_handler_a,
            'type', 'tool',
            'name', 'schema_tool_alpha',
            'description', 'Alpha schema test tool',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object()),
            'outputSchema', jsonb_build_object(
                'type', 'object',
                'properties', jsonb_build_object(
                    'kind', jsonb_build_object('type', 'string')
                )
            ),
            'tags', jsonb_build_array('alpha', 'schema-test')
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('{"kind":"alpha"}'::text)),
        (request).request_id
    );
END;
        $body$
    );

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', v_mcp_handler_b,
            'type', 'tool',
            'name', 'schema_tool_beta',
            'description', 'Beta schema test tool',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object()),
            'tags', jsonb_build_array('beta', 'schema-test')
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('{"kind":"beta"}'::text)),
        (request).request_id
    );
END;
        $body$
    );

    v_list := api.mcp_list_tools();

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'tools') t
        WHERE t->>'name' = 'schema_tool_alpha'
          AND t ? 'outputSchema'
          AND t->'outputSchema'->'properties' ? 'kind'
    ) THEN
        RAISE EXCEPTION 'mcp_list_tools did not expose outputSchema for schema_tool_alpha: %', v_list;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'tools') t
        WHERE t->>'name' = 'schema_tool_alpha'
          AND t->'tags' @> '["alpha"]'::jsonb
    ) THEN
        RAISE EXCEPTION 'mcp_list_tools did not expose tags: %', v_list;
    END IF;

    RAISE NOTICE '  + mcp_list_tools includes outputSchema and tags by default';

    v_list := api.mcp_list_tools(ARRAY['alpha']);

    IF jsonb_array_length(v_list->'tools') <> 1
       OR (v_list->'tools'->0->>'name') <> 'schema_tool_alpha' THEN
        RAISE EXCEPTION 'mcp_list_tools(ARRAY[''alpha'']) did not filter correctly: %', v_list;
    END IF;

    v_list := api.mcp_list_tools(ARRAY['schema-test']);

    IF jsonb_array_length(v_list->'tools') < 2 THEN
        RAISE EXCEPTION 'mcp_list_tools(ARRAY[''schema-test'']) should match both tools: %', v_list;
    END IF;

    RAISE NOTICE '  + mcp_list_tools(p_tags) filters by tag overlap';

    RAISE NOTICE '✓ Schema and tags tests passed';
END $$;
