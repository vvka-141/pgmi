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

    -- JSON-RPC 2.0 spec: the response envelope MUST NOT carry extra top-level
    -- keys. The $schema injection goes into result (where result is "Any" per
    -- spec, so extra keys inside result are allowed).
    IF v_response_body ? '$schema' THEN
        RAISE EXCEPTION 'RPC response $schema must not be at envelope top level (JSON-RPC 2.0 violation): %', v_response_body;
    END IF;
    IF NOT (v_response_body->'result') ? '$schema' THEN
        RAISE EXCEPTION 'RPC response.result missing $schema key (x-include-schema=true): %', v_response_body;
    END IF;

    RAISE NOTICE '  + RPC response.result contains $schema merged from output_json_schema';

    -- ========================================================================
    -- MCP: outputSchema appears in mcp_list_tools; tags filter works
    -- ========================================================================

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', v_mcp_handler_a,
            'type', 'tool',
            'name', 'schema_tool_alpha',
            'description', 'Alpha schema test tool',
            'requiresAuth', false,
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
            'requiresAuth', false,
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

    -- Tags MUST be under _meta.tags (MCP extension slot), NOT at top level
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'tools') t
        WHERE t->>'name' = 'schema_tool_alpha' AND t ? 'tags'
    ) THEN
        RAISE EXCEPTION 'mcp_list_tools must NOT put tags at top-level tool object (spec violation): %', v_list;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'tools') t
        WHERE t->>'name' = 'schema_tool_alpha'
          AND t->'_meta'->'tags' @> '["alpha"]'::jsonb
    ) THEN
        RAISE EXCEPTION 'mcp_list_tools missing _meta.tags for schema_tool_alpha: %', v_list;
    END IF;

    RAISE NOTICE '  + mcp_list_tools includes outputSchema and _meta.tags';

    -- Tag filter: specific tag returns one tool
    v_list := api.mcp_list_tools(ARRAY['alpha']);
    IF jsonb_array_length(v_list->'tools') <> 1
       OR (v_list->'tools'->0->>'name') <> 'schema_tool_alpha' THEN
        RAISE EXCEPTION 'mcp_list_tools(ARRAY[''alpha'']) did not filter correctly: %', v_list;
    END IF;

    -- Tag filter: shared tag returns both
    v_list := api.mcp_list_tools(ARRAY['schema-test']);
    IF jsonb_array_length(v_list->'tools') < 2 THEN
        RAISE EXCEPTION 'mcp_list_tools(ARRAY[''schema-test'']) should match both tools: %', v_list;
    END IF;

    -- Tag filter: empty array MUST be treated as "no filter" (returns all tools)
    DECLARE
        v_all_count int;
        v_empty_count int;
    BEGIN
        v_list := api.mcp_list_tools();
        v_all_count := jsonb_array_length(v_list->'tools');
        v_list := api.mcp_list_tools(ARRAY[]::text[]);
        v_empty_count := jsonb_array_length(v_list->'tools');
        IF v_empty_count <> v_all_count THEN
            RAISE EXCEPTION 'mcp_list_tools(empty array) must behave identically to mcp_list_tools() — got % vs %',
                v_empty_count, v_all_count;
        END IF;
    END;

    RAISE NOTICE '  + mcp_list_tools(p_tags) filters by tag overlap; empty array = no filter';

    RAISE NOTICE '✓ Schema and tags tests passed';
END $$;

-- ============================================================================
-- Test: x-include-schema directive is matched case-insensitively (PGMI-10A)
-- A handler registering a mixed-case key (X-Include-Schema) or value (TRUE)
-- must still trigger $schema injection — the directive lookup must not depend
-- on the exact case stored in response_headers.
-- ============================================================================

DO $$
DECLARE
    v_handler_id uuid := 'ffffffff-0002-4000-8000-00000000000a';
    v_response   api.http_response;
    v_body       jsonb;
BEGIN
    RAISE NOTICE '-> Testing x-include-schema case-insensitivity';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_handler_id,
            'uri', '^/schema-case(\?.*)?$',
            'httpMethod', '^GET$',
            'name', 'schema_case_test',
            'requiresAuth', false,
            'outputSchema', jsonb_build_object(
                'type', 'object',
                'properties', jsonb_build_object('ok', jsonb_build_object('type', 'boolean'))
            ),
            'responseHeaders', jsonb_build_object('X-Include-Schema', 'TRUE')
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('ok', true));
END;
        $body$
    );

    v_response := api.rest_invoke('GET', '/schema-case', ''::extensions.hstore, NULL::bytea);
    v_body := api.content_json((v_response).content);

    IF NOT v_body ? '$schema' THEN
        RAISE EXCEPTION 'mixed-case X-Include-Schema:TRUE must trigger $schema injection: %', v_body;
    END IF;

    -- directive must still be stripped from the wire regardless of case
    IF (v_response).headers ? 'x-include-schema' THEN
        RAISE EXCEPTION 'x-include-schema directive must not leak to the wire: %', (v_response).headers;
    END IF;

    RAISE NOTICE '  + x-include-schema matched case-insensitively (key + value)';
    RAISE NOTICE '✓ x-include-schema case-insensitivity test passed';
END $$;

-- ============================================================================
-- Test: auth-required tools are hidden from mcp_list_tools when auth.user_id
-- is unset; static resources emit `uri`, templated ones emit `uriTemplate`
-- ============================================================================

DO $$
DECLARE
    v_auth_tool_id uuid := 'e3000010-0001-4000-8000-000000000001';
    v_static_res_id uuid := 'e3000010-0002-4000-8000-000000000001';
    v_template_res_id uuid := 'e3000010-0003-4000-8000-000000000001';
    v_list jsonb;
BEGIN
    RAISE NOTICE '-> Testing tools/list auth filter + resources/list uri vs uriTemplate';

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', v_auth_tool_id,
            'type', 'tool',
            'name', 'auth_only_probe',
            'description', 'Requires auth — should be hidden when unauthenticated',
            'inputSchema', jsonb_build_object('type', 'object', 'properties', jsonb_build_object()),
            'requiresAuth', true
        ),
        $body$
BEGIN
    RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id);
END;
        $body$
    );

    -- Ensure no session auth is carried over from earlier tests
    PERFORM set_config('auth.user_id', '', true);

    v_list := api.mcp_list_tools();
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'tools') t
        WHERE t->>'name' = 'auth_only_probe'
    ) THEN
        RAISE EXCEPTION 'auth_only_probe must be hidden when auth.user_id is unset: %', v_list;
    END IF;

    PERFORM set_config('auth.user_id', 'test|alice', true);

    v_list := api.mcp_list_tools();
    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'tools') t
        WHERE t->>'name' = 'auth_only_probe'
    ) THEN
        RAISE EXCEPTION 'auth_only_probe must appear when auth.user_id is set: %', v_list;
    END IF;

    PERFORM set_config('auth.user_id', '', true);

    RAISE NOTICE '  + mcp_list_tools hides auth-required tools when unauthenticated';

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', v_static_res_id,
            'type', 'resource',
            'name', 'static_catalog',
            'description', 'Static resource (no placeholders)',
            'uriTemplate', 'postgres:///catalog',
            'mimeType', 'application/json',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.mcp_resource_result(
        jsonb_build_array(jsonb_build_object(
            'uri', (request).uri,
            'mimeType', 'application/json',
            'text', '{}'
        )),
        (request).request_id
    );
END;
        $body$
    );

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object(
            'id', v_template_res_id,
            'type', 'resource',
            'name', 'templated_doc',
            'description', 'Templated resource (has {placeholder})',
            'uriTemplate', 'postgres:///docs/{doc_id}',
            'mimeType', 'application/json',
            'requiresAuth', false
        ),
        $body$
BEGIN
    RETURN api.mcp_resource_result(
        jsonb_build_array(jsonb_build_object(
            'uri', (request).uri,
            'mimeType', 'application/json',
            'text', '{}'
        )),
        (request).request_id
    );
END;
        $body$
    );

    v_list := api.mcp_list_resources();

    -- Static resource: in resources/list, MUST emit `uri`, never `uriTemplate`.
    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'resources') r
        WHERE r->>'name' = 'static_catalog'
          AND r->>'uri' = 'postgres:///catalog'
          AND NOT (r ? 'uriTemplate')
    ) THEN
        RAISE EXCEPTION 'Static resource must emit `uri` in resources/list: %', v_list;
    END IF;

    -- Templated resource MUST NOT appear in resources/list.
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'resources') r
        WHERE r->>'name' = 'templated_doc'
    ) THEN
        RAISE EXCEPTION 'Templated resource must not appear in resources/list: %', v_list;
    END IF;

    v_list := api.mcp_list_resource_templates();

    -- Templated resource: in resources/templates/list, MUST emit `uriTemplate`.
    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'resourceTemplates') r
        WHERE r->>'name' = 'templated_doc'
          AND r->>'uriTemplate' = 'postgres:///docs/{doc_id}'
          AND NOT (r ? 'uri')
    ) THEN
        RAISE EXCEPTION 'Templated resource must emit `uriTemplate` in resources/templates/list: %', v_list;
    END IF;

    -- Static resource MUST NOT appear in resources/templates/list.
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_list->'resourceTemplates') r
        WHERE r->>'name' = 'static_catalog'
    ) THEN
        RAISE EXCEPTION 'Static resource must not appear in resources/templates/list: %', v_list;
    END IF;

    RAISE NOTICE '  + resources/list and resources/templates/list split static uri from uriTemplate';

    RAISE NOTICE '✓ MCP list auth/uri tests passed';
END $$;

-- ============================================================================
-- Test: registered response_headers propagate to REST responses
-- (except the x-include-schema directive, which stays internal).
-- ============================================================================

DO $$
DECLARE
    v_handler_id uuid := 'e3000011-0001-4000-8000-000000000001';
    v_response api.http_response;
BEGIN
    RAISE NOTICE '-> Testing REST registered response_headers propagation';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_handler_id,
            'uri', '^/headers-probe(\?.*)?$',
            'httpMethod', '^GET$',
            'name', 'headers_probe',
            'requiresAuth', false,
            'responseHeaders', jsonb_build_object(
                'X-Content-Type-Options', 'nosniff',
                'X-Frame-Options', 'DENY',
                'x-include-schema', 'false'
            )
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('ok', true));
END;
        $body$
    );

    v_response := api.rest_invoke('GET', '/headers-probe', ''::extensions.hstore, NULL::bytea);

    IF (v_response).headers->'x-content-type-options' <> 'nosniff' THEN
        RAISE EXCEPTION 'X-Content-Type-Options not propagated (got %): %',
            (v_response).headers->'x-content-type-options', (v_response).headers;
    END IF;
    IF (v_response).headers->'x-frame-options' <> 'DENY' THEN
        RAISE EXCEPTION 'X-Frame-Options not propagated: %', (v_response).headers;
    END IF;
    -- x-include-schema is a directive — MUST NOT leak to the wire
    IF (v_response).headers ? 'x-include-schema' THEN
        RAISE EXCEPTION 'x-include-schema directive must not appear on the wire: %', (v_response).headers;
    END IF;

    RAISE NOTICE '  + registered response_headers propagate (case-insensitive) and x-include-schema is stripped';

    RAISE NOTICE '✓ response_headers propagation tests passed';
END $$;
