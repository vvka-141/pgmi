-- ============================================================================
-- Test: querySchema is enforced before the handler and published
-- ============================================================================
-- Query parameters are declared as a JSON Schema object, the dialect of
-- inputSchema. The gateway answers 400 pgmi.invalid_query with invalid-params
-- before the handler runs, and the OpenAPI document lists every property as
-- an in: query parameter.

DO $$
DECLARE
    v_response api.http_response;
    v_body     jsonb;
    v_params   jsonb;
    v_case     record;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-0304-4000-8000-000000000001',
            'path', '/report',
            'httpMethod', '^GET$',
            'name', 'query_schema_report',
            'access', 'public',
            'outputSchema', jsonb_build_object('type', 'object'),
            'querySchema', jsonb_build_object(
                'type', 'object',
                'required', jsonb_build_array('format'),
                'properties', jsonb_build_object(
                    'format', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('json', 'csv'),
                                                 'description', 'Output format'),
                    'size', jsonb_build_object('type', 'integer', 'enum', jsonb_build_array(10, 20, 50)),
                    'archived', jsonb_build_object('type', 'boolean', 'enum', jsonb_build_array(true, false)),
                    'page', jsonb_build_object('type', 'integer', 'minimum', 1),
                    'tag', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'string')),
                    'q', jsonb_build_object('type', 'string')))),
        $body$ BEGIN PERFORM set_config('test.query_ran', 'yes', true); RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$);

    FOR v_case IN
        SELECT * FROM (VALUES
            ('/report?a=1&format=csv',                          200, NULL),
            ('/report?format=json&size=20&archived=true&page=2', 200, NULL),
            ('/report?format=csv&tag=a&tag=b&q=',                200, NULL),
            ('/report?format=csv&q=a%3Db%3F',                    200, NULL),
            ('/report?a=1',                                      400, '[{"name": "format", "reason": "required"}]'),
            ('/report?format=',                                  400, '[{"name": "format", "reason": "required"}]'),
            ('/report?format=xml&size=30&archived=yes&page=0',   400,
                '[{"name": "archived", "reason": "invalid"}, {"name": "format", "reason": "invalid"},
                  {"name": "page", "reason": "invalid"}, {"name": "size", "reason": "invalid"}]'),
            ('/report?format=csv&format=json',                   400, '[{"name": "format", "reason": "invalid"}]')
        ) AS t(url, status, invalid)
    LOOP
        PERFORM set_config('test.query_ran', 'no', true);
        v_response := api.rest_invoke('GET', v_case.url, ''::extensions.hstore, NULL::bytea);
        v_body := api.content_json((v_response).content);
        IF (v_response).status_code IS DISTINCT FROM v_case.status THEN
            RAISE EXCEPTION 'TEST FAILED: GET % should answer %, got % %', v_case.url, v_case.status, (v_response).status_code, v_body;
        END IF;
        CONTINUE WHEN v_case.status = 200;
        IF v_body->>'code' IS DISTINCT FROM 'pgmi.invalid_query'
           OR v_body->'invalid-params' IS DISTINCT FROM v_case.invalid::jsonb
           OR current_setting('test.query_ran') IS DISTINCT FROM 'no'
           OR (v_response).headers->'x-pgmi-catalog-version' IS NULL THEN
            RAISE EXCEPTION 'TEST FAILED: GET % should be refused before the handler with %, got %', v_case.url, v_case.invalid, v_body;
        END IF;
    END LOOP;

    SELECT op->'parameters' INTO v_params
    FROM jsonb_each(api.openapi_document()->'paths') p, jsonb_each(p.value) m(method, op)
    WHERE p.key = '/report' AND m.method = 'get';
    IF (coalesce(v_params @> jsonb_build_array(jsonb_build_object(
            'name', 'format', 'in', 'query', 'required', true, 'description', 'Output format',
            'schema', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('json', 'csv')))), false)) IS NOT TRUE
       OR (coalesce(v_params @> jsonb_build_array(jsonb_build_object(
            'name', 'tag', 'in', 'query', 'required', false,
            'schema', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'string')))), false)) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: /openapi.json must list the query parameters, got %', v_params;
    END IF;
END $$;

-- What the gateway could not enforce is refused at registration, and so is the
-- retired route-level query array.
DO $$
DECLARE
    v_case record;
    v_refused boolean;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('query array',      jsonb_build_object('query', jsonb_build_array(jsonb_build_object('name', 'q')))),
            ('pattern keyword',  jsonb_build_object('querySchema', '{"type": "object", "properties": {"q": {"type": "string", "pattern": "^a"}}}'::jsonb)),
            ('object parameter', jsonb_build_object('querySchema', '{"type": "object", "properties": {"q": {"type": "object"}}}'::jsonb)),
            ('undeclared required', jsonb_build_object('querySchema', '{"type": "object", "required": ["q"], "properties": {}}'::jsonb))
        ) AS t(label, extra)
    LOOP
        v_refused := false;
        BEGIN
            PERFORM api.create_or_replace_rest_handler(
                jsonb_build_object('id', 'ffffffff-0304-4000-8000-000000000002', 'path', '/report-bad',
                    'httpMethod', '^GET$', 'name', 'query_schema_bad', 'access', 'public') || v_case.extra,
                $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
        EXCEPTION WHEN invalid_parameter_value THEN
            v_refused := true;
        END;
        IF v_refused IS DISTINCT FROM true THEN
            RAISE EXCEPTION 'TEST FAILED: a % must be refused at registration', v_case.label;
        END IF;
    END LOOP;
END $$;

-- The query schema is part of the published contract, so changing only it
-- changes the catalog version (the OpenAPI ETag).
DO $$
DECLARE
    v_before text;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0304-4000-8000-000000000003', 'path', '/report-version',
            'httpMethod', '^GET$', 'name', 'query_schema_version', 'access', 'public',
            'querySchema', '{"type": "object", "properties": {"q": {"type": "string"}}}'::jsonb),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
    v_before := api.catalog_version();

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0304-4000-8000-000000000003', 'path', '/report-version',
            'httpMethod', '^GET$', 'name', 'query_schema_version', 'access', 'public',
            'querySchema', '{"type": "object", "required": ["q"], "properties": {"q": {"type": "string"}}}'::jsonb),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
    IF api.catalog_version() IS NOT DISTINCT FROM v_before THEN
        RAISE EXCEPTION 'TEST FAILED: making a query parameter required must change api.catalog_version()';
    END IF;
END $$;
