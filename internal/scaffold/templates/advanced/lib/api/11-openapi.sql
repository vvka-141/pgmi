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

-- Every capture group becomes a distinctly named template variable. Naming them
-- all '{param}' produced a path like /orgs/{param}/users/{param}, which is not
-- valid OpenAPI: two template variables in one path may not share a name, and
-- generators either reject it or bind both segments to one parameter.
DROP FUNCTION IF EXISTS api.openapi_path(text);

CREATE OR REPLACE FUNCTION api.openapi_path(p_address_regexp text, p_param_names text[] DEFAULT '{}')
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $openapi_path$
    SELECT COALESCE(
        replace(
            string_agg(
                CASE
                    WHEN t.group_index IS NULL THEN t.token
                    ELSE '{' || api.route_path_param_name(p_param_names, t.group_index) || '}'
                END,
                '' ORDER BY t.ord
            ),
            '\.', '.'
        ),
        ''
    )
    FROM api.route_path_tokens(p_address_regexp) t;
$openapi_path$;

COMMENT ON FUNCTION api.openapi_path(text, text[]) IS
    'Converts a route regex to an OpenAPI path template. Each capture group becomes a distinct variable: the handler-declared pathParams name, else positional (p1, p2, ...).';

CREATE OR REPLACE FUNCTION api.openapi_path_parameters(p_address_regexp text, p_param_names text[] DEFAULT '{}')
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT COALESCE(
        jsonb_agg(
            jsonb_build_object(
                'name', api.route_path_param_name(p_param_names, t.group_index),
                'in', 'path',
                'required', true,
                'schema', jsonb_build_object('type', 'string')
            )
            ORDER BY t.group_index
        ),
        '[]'::jsonb
    )
    FROM api.route_path_tokens(p_address_regexp) t
    WHERE t.group_index IS NOT NULL;
$$;

COMMENT ON FUNCTION api.openapi_path_parameters(text, text[]) IS
    'OpenAPI parameters array for a route''s path variables (in: path, required: true), named to match api.openapi_path.';

CREATE OR REPLACE FUNCTION api.openapi_methods(p_method_regexp text)
RETURNS text[]
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $openapi_methods$
    SELECT CASE
        WHEN p_method_regexp ~ E'^\\^[A-Z]+\\$$' THEN
            ARRAY[lower(regexp_replace(regexp_replace(p_method_regexp, E'^\\^', ''), E'\\$$', ''))]
        WHEN p_method_regexp ~ E'^\\^\\(' THEN
            ARRAY(SELECT lower(m[1]) FROM regexp_matches(p_method_regexp, '[A-Z]+', 'g') AS m)
        ELSE
            ARRAY['get','post','put','delete','patch']
    END;
$openapi_methods$;

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
    v_parameters jsonb;
BEGIN
    FOR v_route IN
        SELECT
            r.address_regexp,
            r.method_regexp,
            r.route_name,
            r.path_param_names,
            h.handler_function_name,
            h.title,
            h.description,
            h.accepts,
            h.produces,
            h.requires_auth,
            h.required_transaction_isolation,
            h.input_json_schema,
            h.output_json_schema
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id
        WHERE h.deleted_at IS NULL
        ORDER BY r.sequence_number
    LOOP
        v_path := api.openapi_path(v_route.address_regexp, v_route.path_param_names);
        v_parameters := api.openapi_path_parameters(v_route.address_regexp, v_route.path_param_names);
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

            IF jsonb_array_length(v_parameters) > 0 THEN
                v_operation := v_operation || jsonb_build_object('parameters', v_parameters);
            END IF;

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

            IF v_route.required_transaction_isolation IS NOT NULL THEN
                v_operation := v_operation || jsonb_build_object(
                    'x-pgmi-transaction-isolation', v_route.required_transaction_isolation);
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
DECLARE
    v_etag text := '"' || api.catalog_version() || '"';
    v_inm  text := api.header((request).headers, 'If-None-Match');
    v_resp api.http_response;
BEGIN
    -- Conditional GET. Cache-Control: no-cache means "cache it, but revalidate
    -- every time" — exactly right for a contract: a client may hold it forever
    -- and pay one cheap 304 to prove it is still current, but can never serve a
    -- stale route table (a false 404 for a route a later deploy added).
    --
    -- If-None-Match may be a comma-separated list, or '*'. Match on membership,
    -- not equality, or a well-behaved client sending two tags gets a 200.
    IF v_inm IS NOT NULL AND (
           btrim(v_inm) = '*'
           OR EXISTS (
               SELECT 1
               FROM unnest(string_to_array(v_inm, ',')) AS tag
               WHERE btrim(tag) = v_etag
                  OR btrim(tag) = 'W/' || v_etag
           )
       )
    THEN
        v_resp.status_code := 304;
        v_resp.headers := extensions.hstore(ARRAY[
            'etag', v_etag,
            'cache-control', 'no-cache'
        ]);
        v_resp.content := NULL;   -- 304 carries no body
        RETURN v_resp;
    END IF;

    v_resp := api.json_response(200, api.openapi_document());
    v_resp.headers := (v_resp).headers || extensions.hstore(ARRAY[
        'etag', v_etag,
        'cache-control', 'no-cache'
    ]);
    RETURN v_resp;
END;
    $body$
);

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0004-4000-8000-00000000a002',
        'uri', '^/docs(\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'openapi_docs',
        'title', 'Interactive API Explorer',
        'description', 'Renders a Scalar API reference UI pointing at /openapi.json.',
        'requiresAuth', false,
        'autoLog', false,
        'produces', jsonb_build_array('text/html')
    ),
    $body$
DECLARE
    v_html text;
    v_resp api.http_response;
BEGIN
    v_html := '<!doctype html>'
        || '<html>'
        || '<head>'
        || '<title>' || current_database() || ' API Reference</title>'
        || '<meta charset="utf-8"/>'
        || '<meta name="viewport" content="width=device-width,initial-scale=1"/>'
        || '</head>'
        || '<body>'
        || '<script id="api-reference" data-url="/openapi.json"></script>'
        || '<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>'
        || '</body>'
        || '</html>';

    v_resp.status_code := 200;
    v_resp.headers := extensions.hstore(ARRAY['content-type', 'text/html; charset=utf-8']);
    v_resp.content := convert_to(v_html, 'UTF8');
    RETURN v_resp;
END;
    $body$
);

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.openapi_document() - OpenAPI 3.1 generator';
    RAISE NOTICE '  ✓ GET /openapi.json - self-describing REST contract';
    RAISE NOTICE '  ✓ GET /docs - interactive Scalar API explorer';
END $$;
