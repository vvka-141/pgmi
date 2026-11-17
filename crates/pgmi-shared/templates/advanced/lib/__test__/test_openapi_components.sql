-- ============================================================================
-- Test: successStatus, schema components and the catalog version
-- ============================================================================

DO $$
DECLARE
    v_op jsonb;
    v_refused boolean;
    v_before text;
    v_response api.http_response;
BEGIN
    PERFORM api.create_or_replace_schema_component('TaskDraft',
        '{"type": "object", "required": ["title"], "properties": {"title": {"type": "string"}}}');

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0450-4000-8000-000000000001', 'path', '/oa-components/{id:uuid}',
            'httpMethod', '^PUT$', 'name', 'oa_components_put', 'access', 'public', 'autoLog', false,
            'successStatus', 204,
            'inputSchema', '{"$ref": "#/components/schemas/TaskDraft"}'::jsonb),
        $body$ BEGIN RETURN (204, ''::extensions.hstore, NULL)::api.http_response; END; $body$);

    v_op := api.openapi_document()->'paths'->'/oa-components/{id}'->'put';
    IF v_op->'responses'->'204' IS DISTINCT FROM '{"description": "No Content"}'::jsonb
       OR v_op->'responses' ? '200'
       OR v_op->'requestBody'->'required' IS DISTINCT FROM 'true'::jsonb
       OR jsonb_path_query_first(v_op, '$.requestBody.content.*.schema."$ref"') #>> '{}' IS DISTINCT FROM '#/components/schemas/TaskDraft'
       OR ((api.openapi_document()->'components'->'schemas') ?& ARRAY['Problem', 'TaskDraft']) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: successStatus 204, a required body and the component must be published, got %', v_op;
    END IF;

    -- A bare $ref is checked as the component it names.
    v_response := api.rest_invoke('PUT', '/oa-components/00000000-0000-4000-8000-000000000001',
        'content-type=>application/json'::extensions.hstore, convert_to('{}', 'UTF8'));
    IF (v_response).status_code IS DISTINCT FROM 400
       OR api.content_json((v_response).content)->'invalid-params' IS DISTINCT FROM '[{"name": "title", "reason": "required"}]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a body missing a key the referenced component requires must be 400, got % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;

    -- Changing only a component, or only a success status, is a contract change.
    v_before := api.catalog_version();
    IF api.catalog_version() IS DISTINCT FROM v_before THEN
        RAISE EXCEPTION 'TEST FAILED: the catalog version must be stable between registry changes';
    END IF;
    PERFORM api.create_or_replace_schema_component('TaskDraft',
        '{"type": "object", "required": ["title"], "properties": {"title": {"type": "string", "maxLength": 200}}}');
    IF api.catalog_version() IS NOT DISTINCT FROM v_before THEN
        RAISE EXCEPTION 'TEST FAILED: changing a schema component must change api.catalog_version()';
    END IF;
    v_before := api.catalog_version();
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0450-4000-8000-000000000001', 'path', '/oa-components/{id:uuid}',
            'httpMethod', '^PUT$', 'name', 'oa_components_put', 'access', 'public', 'autoLog', false,
            'successStatus', 200,
            'inputSchema', '{"$ref": "#/components/schemas/TaskDraft"}'::jsonb),
        $body$ BEGIN RETURN api.json_response(200, '{}'); END; $body$);
    IF api.catalog_version() IS NOT DISTINCT FROM v_before THEN
        RAISE EXCEPTION 'TEST FAILED: changing only successStatus must change api.catalog_version()';
    END IF;

    -- A $ref to no component, or a status outside 2xx, is refused.
    FOREACH v_op IN ARRAY ARRAY[
        '{"inputSchema": {"$ref": "#/components/schemas/Nowhere"}}'::jsonb,
        '{"outputSchema": {"type": "object", "properties": {"x": {"$ref": "#/components/schemas/Nowhere"}}}}'::jsonb,
        '{"successStatus": 302}'::jsonb,
        '{"successStatus": [201, 302]}'::jsonb,
        '{"successStatus": [201, 201]}'::jsonb,
        '{"successStatus": []}'::jsonb]
    LOOP
        v_refused := false;
        BEGIN
            PERFORM api.create_or_replace_rest_handler(
                jsonb_build_object('id', 'ffffffff-0450-4000-8000-000000000002', 'path', '/oa-components-bad',
                    'httpMethod', '^POST$', 'name', 'oa_components_bad', 'access', 'public') || v_op,
                $body$ BEGIN RETURN api.json_response(200, '{}'); END; $body$);
        EXCEPTION WHEN invalid_parameter_value THEN
            v_refused := true;
        END;
        IF v_refused IS DISTINCT FROM true THEN
            RAISE EXCEPTION 'TEST FAILED: registration must refuse %', v_op;
        END IF;
    END LOOP;
END $$;

-- Every 2xx the body spells out, in a response call or a CASE branch, must be
-- a declared successStatus.
DO $$
DECLARE
    v_case record;
    v_refused boolean;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('{}'::jsonb,                      $b$ BEGIN RETURN api.json_response(201, '{}'); END; $b$, true),
            ('{"successStatus": 201, "noLocation": "a fixture"}'::jsonb, $b$ BEGIN RETURN api.json_response(201, '{}'); END; $b$, false),
            ('{"successStatus": 201, "noLocation": "a fixture"}'::jsonb, $b$ BEGIN RETURN api.json_response(200, '{}'); END; $b$, true),
            ('{}'::jsonb,                      $b$ BEGIN RETURN (204, ''::extensions.hstore, NULL)::api.http_response; END; $b$, true),
            ('{"successStatus": 200}'::jsonb,  $b$ BEGIN IF true THEN RETURN api.json_response(201, '{}'); END IF; RETURN api.json_response(200, '{}'); END; $b$, true),
            ('{}'::jsonb,                      $b$ BEGIN RETURN api.json_response(CASE WHEN true THEN 201 END, '{}'); END; $b$, true),
            ('{"successStatus": 201, "noLocation": "a fixture"}'::jsonb, $b$ BEGIN RETURN api.json_response(CASE WHEN true THEN 201 ELSE 200 END, '{}'); END; $b$, true),
            ('{"successStatus": [201, 200], "noLocation": "a fixture"}'::jsonb, $b$ BEGIN RETURN api.json_response(CASE WHEN true THEN 201 ELSE 200 END, '{}'); END; $b$, false),
            ('{}'::jsonb,                      $b$ DECLARE s int := 201; BEGIN RETURN api.json_response(s, '{}'); END; $b$, false)
        ) AS c(metadata, body, refused)
    LOOP
        v_refused := false;
        BEGIN
            PERFORM api.create_or_replace_rest_handler(
                jsonb_build_object('id', 'ffffffff-0450-4000-8000-000000000003', 'path', '/oa-success-status',
                    'httpMethod', '^POST$', 'name', 'oa_success_status', 'access', 'public') || v_case.metadata,
                v_case.body);
        EXCEPTION WHEN invalid_parameter_value THEN
            IF SQLERRM NOT LIKE '%but successStatus is%' THEN
                RAISE;
            END IF;
            v_refused := true;
        END;
        IF v_refused IS DISTINCT FROM v_case.refused THEN
            RAISE EXCEPTION 'TEST FAILED: % with body % should be %', v_case.metadata, v_case.body,
                CASE WHEN v_case.refused THEN 'refused' ELSE 'registered' END;
        END IF;
    END LOOP;
END $$;
