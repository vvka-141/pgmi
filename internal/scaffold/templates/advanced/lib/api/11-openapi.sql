/*
<pgmi-meta
    id="a7f02000-0004-4000-8000-000000000011"
    idempotent="true">
  <description>
    OpenAPI 3.1 generator: introspects the handler registry and emits
    a spec document served at GET /openapi.json.
  </description>
  <sortKeys>
    <key>004/011</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION api.openapi_path(p_address_regexp text)
RETURNS text
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE
AS $openapi_path$
DECLARE
    v_path text := p_address_regexp;
BEGIN
    v_path := regexp_replace(v_path, '^\^', '');
    v_path := regexp_replace(v_path, '\(\\\\?\?\.\*\)\?\$?$', '');
    v_path := regexp_replace(v_path, '\$$', '');
    v_path := replace(v_path, '\.', '.');
    v_path := regexp_replace(v_path, '\([^)]+\)', '{param}', 'g');
    RETURN v_path;
END;
$openapi_path$;

CREATE OR REPLACE FUNCTION api.openapi_methods(p_method_regexp text)
RETURNS text[]
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE
AS $sql$
DECLARE
    v_bare text;
BEGIN
    IF p_method_regexp ~ E'^\\^[A-Z]+\\$$' THEN
        v_bare := regexp_replace(regexp_replace(p_method_regexp, E'^\\^', ''), E'\\$$', '');
        RETURN ARRAY[lower(v_bare)];
    ELSIF p_method_regexp ~ E'^\\^\\(' THEN
        RETURN ARRAY(SELECT lower(m[1]) FROM regexp_matches(p_method_regexp, '[A-Z]+', 'g') AS m);
    ELSE
        RETURN ARRAY['get','post','put','delete','patch'];
    END IF;
END;
$sql$;

CREATE OR REPLACE FUNCTION api.openapi_document()
RETURNS jsonb
LANGUAGE plpgsql STABLE
AS $fn$
DECLARE
    v_paths jsonb := '{}'::jsonb;
    v_route RECORD;
    v_methods text[];
    v_method text;
    v_path text;
    v_operation jsonb;
    v_responses jsonb;
    v_request_body jsonb;
    v_security jsonb;
    v_path_item jsonb;
BEGIN
    FOR v_route IN
        SELECT
            r.address_regexp,
            r.method_regexp,
            r.route_name,
            h.handler_function_name,
            h.title,
            h.description,
            h.accepts,
            h.produces,
            h.requires_auth,
            h.input_json_schema,
            h.output_json_schema
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id
        WHERE h.deleted_at IS NULL
        ORDER BY r.sequence_number
    LOOP
        v_path := api.openapi_path(v_route.address_regexp);
        v_methods := api.openapi_methods(v_route.method_regexp);
        v_path_item := COALESCE(v_paths->v_path, '{}'::jsonb);

        FOREACH v_method IN ARRAY v_methods
        LOOP
            v_responses := jsonb_build_object(
                '200', jsonb_build_object(
                    'description', 'Success',
                    'content', jsonb_build_object(
                        COALESCE(v_route.produces[1], 'application/json'),
                        jsonb_build_object(
                            'schema', COALESCE(
                                v_route.output_json_schema::jsonb,
                                '{"type":"object"}'::jsonb
                            )
                        )
                    )
                )
            );

            v_operation := jsonb_build_object(
                'operationId', v_route.handler_function_name,
                'responses', v_responses
            );

            IF v_route.title IS NOT NULL THEN
                v_operation := v_operation || jsonb_build_object('summary', v_route.title);
            END IF;

            IF v_route.description IS NOT NULL THEN
                v_operation := v_operation || jsonb_build_object('description', v_route.description);
            END IF;

            IF v_method IN ('post', 'put', 'patch') THEN
                v_request_body := jsonb_build_object(
                    'content', jsonb_build_object(
                        COALESCE(v_route.accepts[1], 'application/json'),
                        jsonb_build_object(
                            'schema', COALESCE(
                                v_route.input_json_schema::jsonb,
                                '{"type":"object"}'::jsonb
                            )
                        )
                    )
                );
                v_operation := v_operation || jsonb_build_object('requestBody', v_request_body);
            END IF;

            IF v_route.requires_auth THEN
                v_security := jsonb_build_array(jsonb_build_object('bearerAuth', '[]'::jsonb));
                v_operation := v_operation || jsonb_build_object('security', v_security);
            END IF;

            v_path_item := v_path_item || jsonb_build_object(v_method, v_operation);
        END LOOP;

        v_paths := v_paths || jsonb_build_object(v_path, v_path_item);
    END LOOP;

    RETURN jsonb_build_object(
        'openapi', '3.1.0',
        'info', jsonb_build_object(
            'title', current_database() || ' API',
            'version', '1.0.0'
        ),
        'paths', v_paths,
        'components', jsonb_build_object(
            'securitySchemes', jsonb_build_object(
                'bearerAuth', jsonb_build_object(
                    'type', 'http',
                    'scheme', 'bearer'
                )
            )
        )
    );
END;
$fn$;

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0004-4000-8000-00000000a001',
        'uri', '^/openapi\.json(\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'openapi_spec',
        'title', 'OpenAPI 3.1 Specification',
        'description', 'Returns the OpenAPI 3.1 specification document generated from the handler registry.',
        'requiresAuth', false,
        'autoLog', false,
        'produces', jsonb_build_array('application/json')
    ),
    $body$
BEGIN
    RETURN api.json_response(200, api.openapi_document());
END;
    $body$
);

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.openapi_document() - OpenAPI 3.1 generator';
    RAISE NOTICE '  ✓ GET /openapi.json - self-describing REST contract';
END $$;
