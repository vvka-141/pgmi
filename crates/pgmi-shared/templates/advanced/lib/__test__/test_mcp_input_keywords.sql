-- ============================================================================
-- Test: the size, range and value keywords of a tool's inputSchema hold
-- ============================================================================
-- maxItems, minItems, uniqueItems, maxLength, minLength, minimum, maximum,
-- the exclusive bounds and const are refused before the body runs, naming
-- the argument. format and pattern stay hints for the model and the body.
-- Every keyword the check does not enforce is refused at registration, for
-- REST, RPC and MCP alike; the boolean schema false refuses any value, and
-- #/$defs references are followed through a chain.
-- ============================================================================

DO $$
DECLARE
    v_case     record;
    v_envelope jsonb;
    v_refused  text;
    v_schema   jsonb;
    v_protocol text;
    v_protocols text[];
    v_message  text;
BEGIN
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0707-4000-8000-000000000001', 'type', 'tool', 'effect', 'read',
            'name', 'test_input_keywords', 'description', 'Answers ok when its arguments hold', 'access', 'public',
            'inputSchema', '{"type": "object", "properties": {
                "tags":  {"type": "array", "minItems": 1, "maxItems": 3, "uniqueItems": true,
                          "items": {"type": "string", "minLength": 1, "maxLength": 5, "pattern": "^[a-z]+$"}},
                "count": {"type": "integer", "minimum": 0, "exclusiveMaximum": 10},
                "ratio": {"type": "number", "exclusiveMinimum": 0, "maximum": 1},
                "kind":  {"const": "task"},
                "id":    {"type": "string", "format": "uuid"}}}'::jsonb),
        $b$BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id); END;$b$);

    FOR v_case IN SELECT * FROM (VALUES
        ('{"tags": ["a", "b", "c", "d"]}', 'tags'),
        ('{"tags": []}', 'tags'),
        ('{"tags": ["a", "a"]}', 'tags'),
        ('{"tags": ["abcdef"]}', 'tags[0]'),
        ('{"tags": [""]}', 'tags[0]'),
        ('{"count": -5}', 'count'),
        ('{"count": 10}', 'count'),
        ('{"ratio": 0}', 'ratio'),
        ('{"ratio": 1.5}', 'ratio'),
        ('{"kind": "project"}', 'kind')) c(arguments, argument)
    LOOP
        v_envelope := (api.mcp_call_tool('test_input_keywords', v_case.arguments::jsonb, NULL, '"k"')).envelope;
        IF v_envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb
           OR coalesce(v_envelope->'result'->'content'->0->>'text', '') NOT LIKE '%' || v_case.argument || '%' THEN
            RAISE EXCEPTION 'TEST FAILED: % must be refused naming %, got %', v_case.arguments, v_case.argument, v_envelope;
        END IF;
    END LOOP;

    v_envelope := (api.mcp_call_tool('test_input_keywords',
        '{"tags": ["ABC"], "count": 9, "ratio": 1, "kind": "task", "id": "not-a-uuid"}', NULL, '"ok"')).envelope;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'false'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: arguments within every bound must reach the body (format and pattern are hints), got %', v_envelope;
    END IF;

    PERFORM api.create_or_replace_schema_component('TestUnenforcedStep', '{"type": "integer", "multipleOf": 2}');
    FOR v_refused, v_schema, v_protocols IN SELECT c.keyword, c.schema::jsonb, COALESCE(c.protocols, '{mcp,rest,rpc}') FROM (VALUES
        ('oneOf',                '{"type": "object", "properties": {"a": {"oneOf": [{"type": "string"}]}}}', NULL::text[]),
        ('anyOf',                '{"type": "object", "properties": {"a": {"anyOf": [{"type": "string"}]}}}', NULL),
        ('allOf',                '{"type": "object", "properties": {"a": {"allOf": [{"type": "string"}]}}}', NULL),
        ('not',                  '{"type": "object", "properties": {"a": {"not": {"type": "null"}}}}', NULL),
        ('if',                   '{"type": "object", "if": {"required": ["a"]}, "then": {"required": ["b"]}}', NULL),
        ('dependentRequired',    '{"type": "object", "dependentRequired": {"a": ["b"]}}', NULL),
        ('prefixItems',          '{"type": "object", "properties": {"a": {"type": "array", "prefixItems": [{"type": "string"}]}}}', NULL),
        ('multipleOf',           '{"type": "object", "properties": {"a": {"type": "integer", "multipleOf": 5}}}', NULL),
        ('patternProperties',    '{"type": "object", "patternProperties": {"^x": {"type": "string"}}}', NULL),
        ('additionalProperties', '{"type": "object", "additionalProperties": {"type": "integer", "maximum": 10}}', NULL),
        ('items',                '{"type": "object", "properties": {"a": {"type": "array", "items": [{"type": "string"}]}}}', NULL),
        ('$ref',                 '{"type": "object", "properties": {"a": {"$ref": "#/$defs/missing"}}}', '{mcp}'),
        ('multipleOf',           '{"type": "object", "$defs": {"n": {"type": "integer", "multipleOf": 2}}, "properties": {"a": {"$ref": "#/$defs/n"}}}', '{mcp}'),
        ('multipleOf',           '{"type": "object", "properties": {"a": {"$ref": "#/components/schemas/TestUnenforcedStep"}}}', '{rest,rpc}')
    ) c(keyword, schema, protocols)
    LOOP
        FOREACH v_protocol IN ARRAY v_protocols LOOP
            BEGIN
                CASE v_protocol
                WHEN 'mcp' THEN
                    PERFORM api.create_or_replace_mcp_handler(
                        jsonb_build_object('id', 'ffffffff-0707-4000-8000-000000000002', 'type', 'tool', 'effect', 'read',
                            'name', 'test_unenforced_input', 'description', 'Never registered', 'access', 'public',
                            'inputSchema', v_schema),
                        $b$BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END;$b$);
                WHEN 'rest' THEN
                    PERFORM api.create_or_replace_rest_handler(
                        jsonb_build_object('id', 'ffffffff-0707-4000-8000-000000000003', 'path', '/unenforced-input',
                            'httpMethod', '^POST$', 'name', 'unenforced_input', 'access', 'public',
                            'inputSchema', v_schema, 'outputSchema', '{"type": "object"}'::jsonb),
                        $b$BEGIN RETURN api.json_response(200, '{}'::jsonb); END;$b$);
                ELSE
                    PERFORM api.create_or_replace_rpc_handler(
                        jsonb_build_object('id', 'ffffffff-0707-4000-8000-000000000004', 'methodName', 'test.unenforced_input',
                            'access', 'public', 'inputSchema', v_schema),
                        $b$BEGIN RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id'); END;$b$);
                END CASE;
                RAISE EXCEPTION 'TEST FAILED: a % inputSchema using % must be refused at registration: %', v_protocol, v_refused, v_schema;
            EXCEPTION WHEN invalid_parameter_value THEN
                GET STACKED DIAGNOSTICS v_message = MESSAGE_TEXT;
                IF coalesce(v_message, '') NOT LIKE '%' || v_refused || ' at %' THEN
                    RAISE EXCEPTION 'TEST FAILED: the % refusal must name % and its path, got: %', v_protocol, v_refused, v_message;
                END IF;
            END;
        END LOOP;
    END LOOP;

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0707-4000-8000-000000000005', 'type', 'tool', 'effect', 'read',
            'name', 'test_false_and_chain', 'description', 'Answers ok when its arguments hold', 'access', 'public',
            'inputSchema', '{"type": "object", "additionalProperties": true,
                "$defs": {"id": {"$ref": "#/$defs/code"}, "code": {"type": "string", "maxLength": 3}},
                "properties": {"forbidden": false, "none": {"type": "array", "items": false}, "id": {"$ref": "#/$defs/id"}}}'::jsonb),
        $b$BEGIN RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ok')), (request).request_id); END;$b$);
    FOR v_case IN SELECT * FROM (VALUES
        ('{"forbidden": 1}', 'forbidden is not allowed'),
        ('{"none": ["x"]}', 'none[0] is not allowed'),
        ('{"id": "abcd"}', 'id must have at most 3 characters')) c(arguments, argument)
    LOOP
        v_envelope := (api.mcp_call_tool('test_false_and_chain', v_case.arguments::jsonb, NULL, '"f"')).envelope;
        IF v_envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb
           OR coalesce(v_envelope->'result'->'content'->0->>'text', '') NOT LIKE '%' || v_case.argument || '%' THEN
            RAISE EXCEPTION 'TEST FAILED: % must be refused with "%", got %', v_case.arguments, v_case.argument, v_envelope;
        END IF;
    END LOOP;
    v_envelope := (api.mcp_call_tool('test_false_and_chain', '{"none": [], "id": "abc"}', NULL, '"g"')).envelope;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'false'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: an empty array under items false and a 3-character id must pass, got %', v_envelope;
    END IF;

    RAISE NOTICE '  + tool inputSchema: size, range and value keywords hold; unenforced keywords are refused at registration; false and $defs chains hold';
END $$;
