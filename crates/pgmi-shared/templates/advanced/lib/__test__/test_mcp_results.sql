-- ============================================================================
-- Test: MCP results are checked before they leave the database
-- ============================================================================
-- A tool's structuredContent must conform to its outputSchema; a resource that
-- does not exist is -32602, never an empty contents array.
-- ============================================================================

DO $$
DECLARE
    v_envelope jsonb;
BEGIN
    RAISE NOTICE '-> Testing MCP result checks';

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-d001-4000-8000-000000000001', 'type', 'tool', 'effect', 'read',
            'name', 'test_output_shape', 'description', 'Answers the shape its argument asks for',
            'access', 'public',
            'inputSchema', jsonb_build_object('type', 'object',
                'properties', jsonb_build_object('shape', jsonb_build_object('type', 'string'))),
            'outputSchema', jsonb_build_object('type', 'object', 'required', jsonb_build_array('n'),
                'properties', jsonb_build_object('n', jsonb_build_object('type', 'integer')))),
        $body$
DECLARE
    v_out jsonb := CASE (request).arguments->>'shape'
        WHEN 'right' THEN '{"n": 1}'
        WHEN 'wrong' THEN '{"wrong": "shape"}'
        WHEN 'mistyped' THEN '{"n": "one"}'
    END;
BEGIN
    IF (request).arguments->>'shape' = 'refused' THEN
        RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('refused')), (request).request_id, true);
    END IF;
    RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text(COALESCE(v_out::text, ''))), (request).request_id,
                               false, v_out);
END;
        $body$);

    v_envelope := (api.mcp_call_tool('test_output_shape', '{"shape": "right"}', NULL, '"o1"')).envelope;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'false'::jsonb
       OR v_envelope->'result'->'structuredContent' IS DISTINCT FROM '{"n": 1}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a conforming result must pass unchanged, got %', v_envelope;
    END IF;

    FOREACH v_envelope IN ARRAY ARRAY[
        (api.mcp_call_tool('test_output_shape', '{"shape": "wrong"}', NULL, '"o2"')).envelope,
        (api.mcp_call_tool('test_output_shape', '{"shape": "mistyped"}', NULL, '"o2"')).envelope,
        (api.mcp_call_tool('test_output_shape', '{"shape": "none"}', NULL, '"o2"')).envelope]
    LOOP
        IF v_envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb
           OR v_envelope->'result'->'content'->0->>'text' IS DISTINCT FROM 'Tool execution failed'
           OR v_envelope->'result' ? 'structuredContent'
           OR v_envelope->'id' IS DISTINCT FROM '"o2"'::jsonb THEN
            RAISE EXCEPTION 'TEST FAILED: a result that breaks outputSchema must be a sanitized isError, got %', v_envelope;
        END IF;
    END LOOP;

    IF NOT EXISTS (SELECT FROM api.mcp_exchange
                   WHERE mcp_name = 'test_output_shape'
                     AND (response).envelope->'result'->'content'->0->>'text' LIKE 'outputSchema: %n is required%') THEN
        RAISE EXCEPTION 'TEST FAILED: the exchange log must keep why the output was refused';
    END IF;

    v_envelope := (api.mcp_call_tool('test_output_shape', '{"shape": "refused"}', NULL, '"o3"')).envelope;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb
       OR v_envelope->'result'->'content'->0->>'text' IS DISTINCT FROM 'refused' THEN
        RAISE EXCEPTION 'TEST FAILED: a refusal is not checked against outputSchema, got %', v_envelope;
    END IF;
    RAISE DEBUG '  + tool results are checked against outputSchema';

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-d002-4000-8000-000000000001', 'type', 'resource',
            'name', 'test_maybe_item', 'description', 'Item 1 exists, nothing else',
            'uriTemplate', 'resultcheck://item/{id}', 'mimeType', 'application/json', 'access', 'public'),
        $body$
BEGIN
    IF (request).uri = 'resultcheck://item/1' THEN
        RETURN api.mcp_resource_result(jsonb_build_array(jsonb_build_object(
            'uri', (request).uri, 'mimeType', 'application/json', 'text', '{}')), (request).request_id);
    ELSIF (request).uri = 'resultcheck://item/2' THEN
        RETURN api.mcp_resource_not_found((request).uri, (request).request_id);
    END IF;
    RETURN api.mcp_resource_result('[]', (request).request_id);
END;
        $body$);

    v_envelope := (api.mcp_read_resource('resultcheck://item/1', NULL, '"r1"')).envelope;
    IF jsonb_array_length(v_envelope->'result'->'contents') IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'TEST FAILED: an existing resource must be read, got %', v_envelope;
    END IF;

    FOREACH v_envelope IN ARRAY ARRAY[
        (api.mcp_read_resource('resultcheck://item/2', NULL, '"r2"')).envelope,
        (api.mcp_read_resource('resultcheck://item/3', NULL, '"r2"')).envelope]
    LOOP
        IF v_envelope->'error'->'code' IS DISTINCT FROM '-32602'::jsonb
           OR v_envelope->'error'->>'message' IS DISTINCT FROM 'Resource not found'
           OR v_envelope->'error'->'data'->>'uri' NOT IN ('resultcheck://item/2', 'resultcheck://item/3')
           OR v_envelope->'error'->'data'->>'uri' IS NULL
           OR v_envelope->'id' IS DISTINCT FROM '"r2"'::jsonb THEN
            RAISE EXCEPTION 'TEST FAILED: a missing resource must be -32602 with data.uri, got %', v_envelope;
        END IF;
    END LOOP;
    RAISE DEBUG '  + a missing resource is -32602, never an empty contents';
END $$;

-- A complete resources/read result carries caching hints however the handler
-- built it; the handler's own hints win.
DO $$
DECLARE
    v_result jsonb;
BEGIN
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0724-4000-8000-000000000001', 'type', 'resource', 'name', 'cache_plain',
            'description', 'Built with api.mcp_success', 'uriTemplate', 'cachecheck://plain', 'access', 'public'),
        $b$BEGIN
            RETURN api.mcp_success(jsonb_build_object('contents', jsonb_build_array(
                jsonb_build_object('uri', 'cachecheck://plain', 'text', 'x'))), (request).request_id);
        END;$b$);
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0724-4000-8000-000000000002', 'type', 'resource', 'name', 'cache_own',
            'description', 'Sets its own ttlMs', 'uriTemplate', 'cachecheck://own', 'access', 'public'),
        $b$BEGIN
            RETURN api.mcp_success(jsonb_build_object('ttlMs', 5, 'contents', jsonb_build_array(
                jsonb_build_object('uri', 'cachecheck://own', 'text', 'x'))), (request).request_id);
        END;$b$);

    v_result := (api.mcp_read_resource('cachecheck://plain', NULL, '"c1"')).envelope->'result';
    IF (v_result @> api.mcp_cache_policy()) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: a resource built with api.mcp_success must carry %, got %', api.mcp_cache_policy(), v_result;
    END IF;
    v_result := (api.mcp_read_resource('cachecheck://own', NULL, '"c2"')).envelope->'result';
    IF v_result->'ttlMs' IS DISTINCT FROM '5'::jsonb OR (v_result ? 'cacheScope') IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: the handler''s own ttlMs must win, the rest come from the policy, got %', v_result;
    END IF;
END $$;
