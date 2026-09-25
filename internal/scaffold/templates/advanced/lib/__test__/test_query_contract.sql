-- ============================================================================
-- Test: declared query parameters are enforced and published
-- ============================================================================
-- A handler that reads a query parameter used to enforce it alone, so the
-- OpenAPI document never mentioned it and a generated client failed at
-- runtime against a spec that said it was fine.

DO $$
DECLARE
    v_response api.http_response;
    v_detail   text;
    v_params   jsonb;
BEGIN
    RAISE NOTICE '-> Testing the declared query contract';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0304-4000-8000-000000000001',
            'path', '/report',
            'httpMethod', '^GET$',
            'name', 'query_contract_report',
            'requiresAuth', false,
            'outputSchema', jsonb_build_object('type', 'object'),
            'query', jsonb_build_array(
                jsonb_build_object('name', 'format', 'required', true,
                    'schema', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('json', 'csv'))),
                jsonb_build_object('name', 'limit', 'required', false,
                    'schema', jsonb_build_object('type', 'integer')),
                jsonb_build_object('name', 'q', 'required', false, 'allowEmptyValue', true))),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$);

    -- Present in either order: the query is parsed, not matched.
    v_response := api.rest_invoke('GET', '/report?a=1&format=csv', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: format given after another parameter must pass, got %', (v_response).status_code;
    END IF;
    v_response := api.rest_invoke('GET', '/report?format=csv&a=1', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: format given first must pass, got %', (v_response).status_code;
    END IF;

    -- Repeated: satisfied whichever accessor the handler uses later, since the
    -- check runs before the handler and cannot know which it will call.
    v_response := api.rest_invoke('GET', '/report?format=csv&format=json', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: a repeated required parameter must pass, got %', (v_response).status_code;
    END IF;

    -- Absent: 400 naming the parameter, finalized like every gateway error.
    v_response := api.rest_invoke('GET', '/report?a=1', ''::extensions.hstore, NULL::bytea);
    v_detail := api.content_json((v_response).content)->>'detail';
    IF (v_response).status_code IS DISTINCT FROM 400 OR coalesce(v_detail, '') NOT LIKE '%"format"%' THEN
        RAISE EXCEPTION 'TEST FAILED: a missing required parameter must be a 400 naming it, got % %',
            (v_response).status_code, v_detail;
    END IF;
    IF (v_response).headers->'x-pgmi-catalog-version' IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the 400 must go through finalize_error';
    END IF;

    -- Present but empty: rejected unless allowEmptyValue says otherwise.
    v_response := api.rest_invoke('GET', '/report?format=', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 400 THEN
        RAISE EXCEPTION 'TEST FAILED: ?format= must be a 400 (allowEmptyValue not set), got %', (v_response).status_code;
    END IF;
    v_response := api.rest_invoke('GET', '/report?format=json&q=', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: ?q= must pass (allowEmptyValue true), got %', (v_response).status_code;
    END IF;

    -- Published as in: query, with the declared schema.
    SELECT op->'parameters' INTO v_params
    FROM jsonb_each(api.openapi_document()->'paths') p, jsonb_each(p.value) m(method, op)
    WHERE p.key = '/report' AND m.method = 'get';
    IF NOT coalesce(v_params @> jsonb_build_array(jsonb_build_object(
            'name', 'format', 'in', 'query', 'required', true,
            'schema', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('json', 'csv')))), false)
       OR NOT coalesce(v_params @> jsonb_build_array(jsonb_build_object(
            'name', 'q', 'in', 'query', 'allowEmptyValue', true)), false)
       OR NOT coalesce(v_params @> jsonb_build_array(jsonb_build_object(
            'name', 'limit', 'in', 'query', 'required', false)), false) THEN
        RAISE EXCEPTION 'TEST FAILED: /openapi.json must list the query parameters, got %', v_params;
    END IF;

    -- A malformed declaration is refused at registration.
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object('id', 'ffffffff-0304-4000-8000-000000000002', 'path', '/report-bad',
                'httpMethod', '^GET$', 'name', 'query_contract_bad', 'requiresAuth', false,
                'outputSchema', jsonb_build_object('type', 'object'),
                'query', jsonb_build_array(jsonb_build_object('required', true))),
            $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
        RAISE EXCEPTION 'TEST FAILED: a query entry without a name must be refused';
    EXCEPTION WHEN invalid_parameter_value THEN
        NULL;
    END;

    RAISE NOTICE '  + required, empty and ordering cases enforced; parameters published as in: query';
    RAISE NOTICE '✓ Query contract tests passed';
END $$;
