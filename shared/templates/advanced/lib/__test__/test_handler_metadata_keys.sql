-- ============================================================================
-- Test: a handler metadata key no registration function reads is refused
-- ============================================================================
-- A misspelled key would otherwise deploy green and change nothing: 'readonly'
-- left a write path writable. The hint names the key it was meant to be.
-- ============================================================================

CREATE FUNCTION pg_temp.register_with(p_kind text, p_extra jsonb)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    CASE p_kind
        WHEN 'rest' THEN PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object('id', 'ffffffff-0539-4000-8000-000000000001', 'path', '/metadata-keys',
                               'httpMethod', '^GET$', 'name', 'metadata_keys_rest', 'access', 'public',
                               'outputSchema', '{"type": "object"}'::jsonb) || p_extra,
            $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
        WHEN 'rpc' THEN PERFORM api.create_or_replace_rpc_handler(
            jsonb_build_object('id', 'ffffffff-0539-4000-8000-000000000002', 'methodName', 'metadata_keys.rpc',
                               'access', 'public') || p_extra,
            $body$ BEGIN RETURN api.jsonrpc_success('{}', NULL); END; $body$);
        ELSE PERFORM api.create_or_replace_mcp_handler(
            jsonb_build_object('id', 'ffffffff-0539-4000-8000-000000000003', 'type', 'tool', 'effect', 'read',
                               'name', 'metadata_keys_mcp', 'description', 'Metadata key fixture',
                               'access', 'public') || p_extra,
            $body$ BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END; $body$);
    END CASE;
END $$;

DO $$
DECLARE
    v_kind text;
    v_case record;
    v_message text;
    v_hint text;
BEGIN
    RAISE NOTICE '-> Handler metadata: unknown keys are refused';

    FOREACH v_kind IN ARRAY ARRAY['rest', 'rpc', 'mcp'] LOOP
        PERFORM pg_temp.register_with(v_kind, '{}');

        FOR v_case IN
            SELECT * FROM (VALUES
                ('{"readonly": true}'::jsonb, 'readonly',     'did you mean "readOnly"?'),
                ('{"colour": 1}'::jsonb,      'colour',       'description'),
                ('{"requiresAuth": true}',    'requiresAuth', '"access": "public", "authenticated" or "organization"')
            ) AS c(extra, key, hint)
        LOOP
            v_message := NULL;
            BEGIN
                PERFORM pg_temp.register_with(v_kind, v_case.extra);
            EXCEPTION WHEN invalid_parameter_value THEN
                GET STACKED DIAGNOSTICS v_message = MESSAGE_TEXT, v_hint = PG_EXCEPTION_HINT;
            END;
            IF v_message IS NULL THEN
                RAISE EXCEPTION 'TEST FAILED: % registered with the unknown key %', v_kind, v_case.key;
            END IF;
            IF coalesce(strpos(v_message, v_case.key), 0) IS NOT DISTINCT FROM 0 OR coalesce(strpos(v_hint, v_case.hint), 0) IS NOT DISTINCT FROM 0
               OR v_hint IS NULL THEN
                RAISE EXCEPTION 'TEST FAILED: % refused % as "%" with hint "%", want the key named and a hint containing %',
                    v_kind, v_case.key, v_message, v_hint, v_case.hint;
            END IF;
        END LOOP;
    END LOOP;

    v_message := NULL;
    BEGIN
        PERFORM pg_temp.register_with('rest', '{"version": "1"}');
    EXCEPTION WHEN OTHERS THEN
        v_message := SQLERRM;
    END;
    IF coalesce(strpos(v_message, '"version" key is not supported'), 0) IS NOT DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'TEST FAILED: the retired REST key version lost its own message: %', v_message;
    END IF;

    RAISE NOTICE '  + REST, RPC and MCP refuse unknown keys, naming the near match';
END $$;

DO $$
DECLARE
    v_case record;
    v_message text;
BEGIN
    RAISE NOTICE '-> Handler metadata: a 201 declares locationTemplate or noLocation';

    FOR v_case IN
        SELECT * FROM (VALUES
            ('{"successStatus": [200, 201]}'::jsonb, '%locationTemplate%noLocation%'),
            ('{"successStatus": [200, 201], "noLocation": ""}', '%noLocation%'),
            ('{"successStatus": [200, 201], "noLocation": true}', '%noLocation%'),
            ('{"successStatus": [200, 201], "noLocation": "no GET", "locationTemplate": "/things/{id}"}', '%noLocation%'),
            ('{"noLocation": "no GET"}', '%noLocation%'),
            ('{"successStatus": [200, 201], "noLocation": "no GET reads it"}', NULL)
        ) AS c(extra, refusal)
    LOOP
        v_message := NULL;
        BEGIN
            PERFORM pg_temp.register_with('rest', v_case.extra);
        EXCEPTION WHEN invalid_parameter_value THEN
            v_message := SQLERRM;
        END;
        IF (v_message IS NULL) IS DISTINCT FROM (v_case.refusal IS NULL)
           OR (v_message LIKE v_case.refusal) IS FALSE THEN
            RAISE EXCEPTION 'TEST FAILED: % answered "%", want %', v_case.extra, coalesce(v_message, 'registered'),
                coalesce(v_case.refusal, 'registered');
        END IF;
    END LOOP;

    RAISE NOTICE '  + a 201 without locationTemplate or noLocation is refused at registration';
END $$;
