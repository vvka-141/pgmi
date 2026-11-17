/*
<pgmi-meta
    id="a7f02000-0004-4000-8000-000000000011"
    idempotent="true">
  <description>
    OpenAPI 3.1 generator: introspects the handler registry and emits
    a spec document served at GET /openapi.json, and the /docs page that
    reads it.
  </description>
  <sortKeys>
    <key>004/011</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION api.openapi_path_parameters(p_path_params jsonb)
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT COALESCE(
        jsonb_agg(
            jsonb_build_object(
                'name', p->>'name',
                'in', 'path',
                'required', true,
                'schema', CASE p->>'type'
                    WHEN 'uuid' THEN '{"type": "string", "format": "uuid"}'::jsonb
                    WHEN 'int' THEN '{"type": "integer"}'::jsonb
                    ELSE '{"type": "string"}'::jsonb END)
            ORDER BY ord),
        '[]'::jsonb)
    FROM jsonb_array_elements(p_path_params) WITH ORDINALITY AS t(p, ord);
$$;

COMMENT ON FUNCTION api.openapi_path_parameters(jsonb) IS
    'OpenAPI parameters array for a route''s path variables, from api.rest_route.path_params (uuid → string/uuid, int → integer, text → string).';


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

CREATE OR REPLACE FUNCTION api.openapi_query_parameters(p_query_schema jsonb)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
    SELECT COALESCE(jsonb_agg(
               jsonb_build_object(
                   'name', p.key,
                   'in', 'query',
                   'required', COALESCE(p_query_schema->'required', '[]') ? p.key,
                   'schema', p.value - 'description')
               || CASE WHEN p.value ? 'description'
                       THEN jsonb_build_object('description', p.value->'description') ELSE '{}' END
               ORDER BY p.key), '[]')
    FROM jsonb_each(COALESCE(p_query_schema->'properties', '{}')) AS p;
$$;

COMMENT ON FUNCTION api.openapi_query_parameters(jsonb) IS
    'OpenAPI in: query parameters from a querySchema, one per property.';

-- One RFC 9457 body for every error the gateway or a handler returns.
CREATE OR REPLACE FUNCTION internal.openapi_problem_schema()
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT '{
        "type": "object",
        "description": "RFC 9457 problem details. code is pgmi.<reason> for the framework, <schema>.<entity>.<reason> for the application.",
        "required": ["title", "status"],
        "properties": {
            "type": {"type": "string"},
            "title": {"type": "string"},
            "status": {"type": "integer"},
            "detail": {"type": "string"},
            "instance": {"type": "string"},
            "code": {"type": "string"},
            "invalid-params": {"type": "array", "items": {
                "type": "object", "required": ["name", "reason"],
                "properties": {"name": {"type": "string"}, "reason": {"type": "string"}}}}
        }
    }'::jsonb;
$$;

-- The responses of one operation: its declared success status, and every
-- client error the gateway derives from the route's metadata, each with the
-- shared Problem body.
CREATE OR REPLACE FUNCTION internal.openapi_responses(
    p_method text,
    p_success_statuses integer[],
    p_produces text[],
    p_output_schema jsonb,
    p_access text,
    p_has_typed_path boolean,
    p_has_query_schema boolean,
    p_read_only boolean,
    p_min_isolation text
) RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (SELECT jsonb_object_agg(s::text,
                CASE WHEN s = 204
                     THEN jsonb_build_object('description', 'No Content')
                     ELSE jsonb_build_object('description', 'Success', 'content', jsonb_build_object(
                              COALESCE(p_produces[1], 'application/json'),
                              jsonb_build_object('schema', COALESCE(p_output_schema, '{"type": "object"}'))))
                END)
            FROM unnest(p_success_statuses) s)
        || COALESCE(jsonb_object_agg(e.status, jsonb_build_object(
               'description', e.description,
               'content', jsonb_build_object('application/problem+json',
                   jsonb_build_object('schema', jsonb_build_object('$ref', '#/components/schemas/Problem')))))
             FILTER (WHERE e.applies), '{}')
    FROM (VALUES
        ('400', 'Bad Request: malformed or schema-invalid input',
            p_method IN ('post', 'put', 'patch') OR p_has_typed_path OR p_has_query_schema),
        ('401', 'Unauthorized', p_access <> 'public'),
        ('403', 'Forbidden: no declared permission, or no active organization', p_access IN ('organization', 'permission')),
        ('404', 'Not Found', true),
        ('405', 'Method Not Allowed', true),
        ('406', 'Not Acceptable', true),
        ('409', 'Conflict', true),
        ('415', 'Unsupported Media Type', p_method IN ('post', 'put', 'patch')),
        ('422', 'Unprocessable Entity', true),
        ('428', 'Precondition Required: the transaction does not meet the route''s policy',
            p_read_only OR p_min_isolation IS NOT NULL)
    ) AS e(status, description, applies);
$$;

-- The database does not see the gateway's mount, so the deploy names it.
DO $$
BEGIN
    EXECUTE format($fn$
        CREATE OR REPLACE FUNCTION api.openapi_server_url()
        RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $body$
            SELECT %L::text;
        $body$
    $fn$, pg_temp.deployment_setting('api_base_path'));
END $$;

COMMENT ON FUNCTION api.openapi_server_url() IS
    'The OpenAPI servers url: the deploy parameter api_base_path, the path the gateway serves REST under (/api by default).';

-- The gateways refuse a write that carries the session cookie without the
-- token from the readable __Host-csrf cookie (gateways/PROTOCOL.md).
CREATE OR REPLACE FUNCTION internal.openapi_csrf_parameter(p_required boolean)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object(
        'name', 'X-CSRF-Token',
        'in', 'header',
        'required', p_required,
        'description', 'The value of the __Host-csrf cookie. Required whenever the request carries the '
            || '__Host-session cookie; without it the gateway answers 403 pgmi.csrf_refused.',
        'schema', jsonb_build_object('type', 'string'));
$$;

-- One statement, long because the document is one nested value: each part is
-- a CTE over the registry, and the helpers above build the repeated pieces.
CREATE OR REPLACE FUNCTION api.openapi_document()
RETURNS jsonb
LANGUAGE plpgsql STABLE
AS $fn$
BEGIN
    -- plpgsql, not sql: api.current_principal_id() is the application's and is
    -- defined after this file.
    RETURN (WITH op AS (
        SELECT r.canonical_path AS path,
               m.method,
               jsonb_build_object(
                   'operationId', h.handler_function_name,
                   'responses', (SELECT CASE WHEN r.location_template IS NULL THEN resp
                                             ELSE jsonb_set(resp, '{201,headers}', jsonb_build_object('Location', jsonb_build_object(
                                                      'description', 'The created resource, ' || r.location_template,
                                                      'schema', '{"type": "string", "format": "uri-reference"}'::jsonb))) END
                                 FROM internal.openapi_responses(
                                          m.method, r.success_statuses, h.produces, h.output_json_schema::jsonb, h.access,
                                          EXISTS (SELECT FROM jsonb_array_elements(r.path_params) pp WHERE pp->>'type' IN ('uuid', 'int')),
                                          r.query_schema IS NOT NULL, h.read_only, h.min_transaction_isolation) AS resp))
               || CASE WHEN jsonb_array_length(prm.parameters) > 0
                       THEN jsonb_build_object('parameters', prm.parameters) ELSE '{}' END
               || CASE WHEN h.title IS NOT NULL THEN jsonb_build_object('summary', h.title) ELSE '{}' END
               || CASE WHEN h.description IS NOT NULL THEN jsonb_build_object('description', h.description) ELSE '{}' END
               || CASE WHEN m.method IN ('post', 'put', 'patch') THEN jsonb_build_object('requestBody',
                      jsonb_build_object('content', jsonb_build_object(COALESCE(h.accepts[1], 'application/json'),
                          jsonb_build_object('schema', COALESCE(h.input_json_schema::jsonb, '{"type": "object"}'))))
                      || CASE WHEN h.input_json_schema IS NOT NULL THEN '{"required": true}'::jsonb ELSE '{}' END)
                       ELSE '{}' END
               -- Organization access or an organization permission needs the tab's
               -- organization session beside the login session; a platform
               -- permission or plain authentication needs the login session alone.
               || CASE WHEN h.access <> 'public' THEN jsonb_build_object('security', (
                      SELECT jsonb_agg(alt ORDER BY ord)
                      FROM (VALUES
                          (1, '{"loginSession": [], "organizationSession": []}'::jsonb,
                              h.access = 'organization'
                              OR EXISTS (SELECT FROM unnest(perm.permissions) p WHERE NOT internal.is_platform_permission(p))),
                          (2, '{"loginSession": []}'::jsonb,
                              h.access = 'authenticated'
                              OR EXISTS (SELECT FROM unnest(perm.permissions) p WHERE internal.is_platform_permission(p)))
                      ) AS s(ord, alt, applies)
                      WHERE applies)) ELSE '{}' END
               || CASE WHEN h.access = 'permission' THEN jsonb_build_object('x-pgmi-permissions', to_jsonb(perm.permissions)) ELSE '{}' END
               || CASE WHEN h.min_transaction_isolation IS NOT NULL
                       THEN jsonb_build_object('x-pgmi-min-transaction-isolation', h.min_transaction_isolation) ELSE '{}' END
               -- readOnly alone does not mean replica-safe (a serializable floor
               -- is primary-only), so the hint is explicit on every read-only
               -- operation and absent on the rest.
               || CASE WHEN h.read_only THEN jsonb_build_object('x-pgmi-read-only', true,
                      'x-pgmi-replica-safe', internal.transaction_policy_replica_safe(h.min_transaction_isolation, h.read_only))
                       ELSE '{}' END
               AS operation
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id
        CROSS JOIN LATERAL unnest(api.openapi_methods(r.method_regexp)) AS m(method)
        CROSS JOIN LATERAL (
            SELECT ARRAY(SELECT hp.permission_id FROM api.handler_permission hp
                         WHERE hp.handler_object_id = h.object_id ORDER BY hp.permission_id) AS permissions
        ) perm
        CROSS JOIN LATERAL (
            SELECT api.openapi_path_parameters(r.path_params)
                || api.openapi_query_parameters(r.query_schema::jsonb)
                || CASE WHEN m.method IN ('post', 'put', 'patch', 'delete')
                        THEN jsonb_build_array(internal.openapi_csrf_parameter(h.access <> 'public'))
                        ELSE '[]' END AS parameters
        ) prm
        WHERE h.deleted_at IS NULL
          -- Filtered by caller for readability: an anonymous caller sees the public
          -- operations. Not a security boundary: method sets are public (OPTIONS
          -- and 405 list every method), and the gateway's access decision
          -- protects an operation.
          AND (h.access = 'public' OR (SELECT api.current_principal_id()) IS NOT NULL)
    )
    SELECT jsonb_build_object(
        'openapi', '3.1.0',
        'info', jsonb_build_object('title', current_database() || ' API', 'version', '1.0.0'),
        'servers', jsonb_build_array(jsonb_build_object('url', api.openapi_server_url())),
        'paths', COALESCE((SELECT jsonb_object_agg(p.path, p.operations)
                           FROM (SELECT path, jsonb_object_agg(method, operation) AS operations
                                 FROM op GROUP BY path) p), '{}'),
        'components', jsonb_build_object(
            'schemas', jsonb_build_object('Problem', internal.openapi_problem_schema())
                || COALESCE((SELECT jsonb_object_agg(sc.name, sc.schema::jsonb
                                 || CASE WHEN sc.description IS NOT NULL
                                         THEN jsonb_build_object('description', sc.description) ELSE '{}' END)
                             FROM api.schema_component sc), '{}'),
            'securitySchemes', jsonb_build_object(
                'loginSession', jsonb_build_object(
                    'type', 'apiKey',
                    'in', 'cookie',
                    'name', '__Host-session',
                    'description', 'The login session the gateway sets at sign-in (HttpOnly, Secure, '
                        || 'SameSite=Lax). A browser sends it by itself; a write that carries it also '
                        || 'needs the X-CSRF-Token header.'),
                'organizationSession', jsonb_build_object(
                    'type', 'apiKey',
                    'in', 'header',
                    'name', 'x-organization-session',
                    'description', 'Opaque handle of the browser tab''s organization session (POST '
                        || '/organization-sessions). It fixes the organization of the request and is '
                        || 'valid only with the login session it was opened in.')))));
END;
$fn$;

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'a7f02000-0004-4000-8000-00000000a001',
        'path', '/openapi.json',
        'httpMethod', '^GET$',
        'name', 'openapi_spec',
        'title', 'OpenAPI 3.1 Specification',
        'description', 'Returns the OpenAPI 3.1 specification document generated from the handler registry.',
        'access', 'public',
        'autoLog', false,
        'produces', jsonb_build_array('application/json')
    ),
    $body$
DECLARE
    -- The document depends on the caller only through whether they are
    -- authenticated, so two validator classes suffice. If the filter in
    -- api.openapi_document() ever becomes per-user, this must become per-user
    -- too, or a 304 hands one caller's cached document to another.
    v_etag text := '"' || api.catalog_version() || '-'
                || CASE WHEN api.current_principal_id() IS NULL THEN 'anon' ELSE 'auth' END || '"';
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
        v_resp.content := NULL;
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
        'path', '/docs',
        'httpMethod', '^GET$',
        'name', 'openapi_docs',
        'title', 'Interactive API Explorer',
        'description', 'Renders a Scalar API reference UI for openapi.json, found relative to this page so it works under any gateway mount.',
        'access', 'public',
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
        || '<script id="api-reference" data-url="openapi.json"></script>'
        -- Pinned with SRI: this script runs in the API's own origin, so an
        -- unpinned @latest would execute whatever the CDN served that day.
        -- The cost is deliberate: the pin receives no upstream fixes, and a
        -- version bump must regenerate the hash, e.g.
        --   curl -sL <url> | openssl dgst -sha384 -binary | openssl base64 -A
        || '<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.71.0/dist/browser/standalone.js"'
        || ' integrity="sha384-I7aSmSxf06vl5HT10vzNOAryO+PFCAVHIGwhZerHn6yM/O0642381S3kw9o7fFQd"'
        || ' crossorigin="anonymous"></script>'
        || '</body>'
        || '</html>';

    v_resp.status_code := 200;
    v_resp.headers := extensions.hstore(ARRAY['content-type', 'text/html; charset=utf-8']);
    v_resp.content := convert_to(v_html, 'UTF8');
    RETURN v_resp;
END;
    $body$
);
