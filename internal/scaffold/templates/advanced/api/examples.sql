/*
<pgmi-meta
    id="a7f01000-0010-4000-8000-000000000002"
    idempotent="true">
  <description>
    Getting-started REST handlers: hello world, echo, health check,
    current user, and organization listing.

    Each example shows the handler registration pattern:
    1. Call api.create_or_replace_rest_handler() with two arguments
    2. First argument: JSONB metadata describing the handler (routing, auth, etc.)
    3. Second argument: the handler function body that processes requests

    After each registration, an invocation demo shows how to call the handler
    through the gateway and read the response using api.content_json().
  </description>
  <sortKeys>
    <key>005/001</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE DEBUG '-> Installing example handlers'; END $$;

-- ============================================================================
-- REST Example: Hello World
-- ============================================================================
-- Demonstrates the simplest possible REST handler.
--
-- api.create_or_replace_rest_handler() takes two arguments:
--
--   1. METADATA (jsonb): Routing and behavior configuration.
--      The router uses this to match incoming HTTP requests to this handler:
--        - id:           Stable UUID — survives redeploys without breaking references
--        - uri:          POSIX regex matched against the request URL path
--        - httpMethod:   POSIX regex matched against the HTTP method (default: any)
--        - name:         Becomes the PostgreSQL function name (api.<name>)
--        - description:  Human-readable — shown in pgAdmin and introspection views
--        - language:     Handler language: plpgsql (default) or sql
--        - requiresAuth: If true, gateway rejects requests without a resolved user.
--                        DEFAULTS TO TRUE — omit it and the endpoint is authenticated.
--                        Set 'requiresAuth', false explicitly for public endpoints.
--        - autoLog:      If true, request/response logged to api.rest_exchange (default true)
--        - outputSchema: REQUIRED on every REST handler (the OpenAPI test enforces it).
--        - requiredTransactionIsolation: Minimum isolation floor the gateway enforces
--                        (read committed | repeatable read | serializable). Omit for none.
--                        The caller must open the transaction at >= this level or the
--                        gateway returns 428; see lib/api/00-transaction-isolation.sql.
--
--   2. HANDLER BODY (text): The function body executed when the route matches.
--      - The function receives a single parameter called "request" (api.rest_request)
--        containing: method, url, headers, content (raw bytea body)
--      - The function must return api.http_response (status_code, headers, content)
--      - Use api.json_response() to build the response
--      - Use api.content_json() to parse the request body
--      - Use api.query_params() to parse URL query parameters
--      - Use api.header() for case-insensitive header lookup

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0001-4000-8000-000000000001',
        'uri', '^/hello(\\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'hello_world',
        'description', 'Simple hello world endpoint',
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'message', jsonb_build_object('type', 'string'),
                'timestamp', jsonb_build_object('type', 'string', 'format', 'date-time')
            ),
            'required', jsonb_build_array('message', 'timestamp')
        )
    ),
    $body$
DECLARE
    v_name text;
BEGIN
    v_name := COALESCE(api.query_params((request).url)->'name', 'World');
    RETURN api.json_response(200, jsonb_build_object(
        'message', 'Hello, ' || v_name || '!',
        'timestamp', now()
    ));
END;
    $body$
);

DO $$
DECLARE
    v_response api.http_response;
BEGIN
    v_response := api.rest_invoke('GET', '/hello?name=Developer');
    RAISE DEBUG '  -> GET /hello?name=Developer  status=%, body=%',
        (v_response).status_code,
        api.content_json((v_response).content);
END $$;

-- ============================================================================
-- REST Example: Echo
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0002-4000-8000-000000000001',
        'uri', '^/echo(\\?.*)?$',
        'httpMethod', '^POST$',
        'name', 'echo',
        'description', 'Echo back the request body',
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'description', 'Any JSON payload to echo back'
        ),
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'method', jsonb_build_object('type', 'string'),
                'url', jsonb_build_object('type', 'string'),
                'body', jsonb_build_object('type', 'object')
            ),
            'required', jsonb_build_array('method', 'url', 'body')
        )
    ),
    $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object(
        'method', (request).method,
        'url', (request).url,
        'body', api.content_json((request).content)
    ));
END;
    $body$
);

DO $$
DECLARE
    v_response api.http_response;
BEGIN
    v_response := api.rest_invoke('POST', '/echo', ''::extensions.hstore,
        '{"greeting": "hello from examples.sql"}'::jsonb);
    RAISE DEBUG '  -> POST /echo  status=%, body=%',
        (v_response).status_code,
        api.content_json((v_response).content);
END $$;

-- ============================================================================
-- REST Example: Health Check (No Auth, No Logging)
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0003-4000-8000-000000000001',
        'uri', '^/health(\\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'health_check',
        'description', 'Kubernetes liveness probe endpoint',
        'autoLog', false,
        'requiresAuth', false,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'status', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('healthy')),
                'timestamp', jsonb_build_object('type', 'string', 'format', 'date-time')
            ),
            'required', jsonb_build_array('status', 'timestamp')
        )
    ),
    $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object(
        'status', 'healthy',
        'timestamp', now()
    ));
END;
    $body$
);

DO $$
DECLARE
    v_response api.http_response;
BEGIN
    v_response := api.rest_invoke('GET', '/health');
    RAISE DEBUG '  -> GET /health  status=%, body=%',
        (v_response).status_code,
        api.content_json((v_response).content);
END $$;

-- ============================================================================
-- REST Example: Current User (Membership Integration)
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0004-4000-8000-000000000001',
        'uri', '^/me(\\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'get_current_user',
        'description', 'Get authenticated user profile from membership schema',
        'requiresAuth', true,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'userId', jsonb_build_object('type', 'string'),
                'email', jsonb_build_object('type', 'string', 'format', 'email'),
                'displayName', jsonb_build_object('type', 'string'),
                'emailVerified', jsonb_build_object('type', 'boolean'),
                'memberOrgIds', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'string', 'format', 'uuid')),
                'ownerOrgIds', jsonb_build_object('type', 'array', 'items', jsonb_build_object('type', 'string', 'format', 'uuid'))
            ),
            'required', jsonb_build_array('userId', 'email')
        )
    ),
    $body$
DECLARE
    v_user jsonb;
BEGIN
    -- camelCase keys: pgmi JSON convention (columns stay snake_case)
    SELECT jsonb_build_object(
        'userId', user_id,
        'email', email,
        'displayName', display_name,
        'emailVerified', email_verified,
        'memberOrgIds', member_org_ids,
        'ownerOrgIds', owner_org_ids
    ) INTO v_user
    FROM api.vw_current_user;

    IF v_user IS NULL THEN
        RETURN api.problem_response(404, 'Not Found', 'User not found for current session');
    END IF;

    RETURN api.json_response(200, v_user);
END;
    $body$
);

-- ============================================================================
-- REST Example: List Organizations (RLS-Filtered)
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0005-4000-8000-000000000001',
        'uri', '^/organizations(\\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'list_organizations',
        'description', 'List organizations the authenticated user belongs to (RLS-filtered)',
        'requiresAuth', true,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'organizations', jsonb_build_object(
                    'type', 'array',
                    'items', jsonb_build_object(
                        'type', 'object',
                        'properties', jsonb_build_object(
                            'id', jsonb_build_object('type', 'string', 'format', 'uuid'),
                            'name', jsonb_build_object('type', 'string'),
                            'slug', jsonb_build_object('type', 'string'),
                            'isPersonal', jsonb_build_object('type', 'boolean')
                        )
                    )
                )
            ),
            'required', jsonb_build_array('organizations')
        )
    ),
    $body$
DECLARE
    v_orgs jsonb;
BEGIN
    SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'id', o.object_id,
        'name', o.name,
        'slug', o.slug,
        'isPersonal', o.is_personal
    )), '[]'::jsonb) INTO v_orgs
    FROM membership.vw_active_organizations o
    WHERE o.object_id = ANY(api.current_member_org_ids());

    RETURN api.json_response(200, jsonb_build_object('organizations', v_orgs));
END;
    $body$
);

DO $$ BEGIN
    RAISE DEBUG '  + REST: GET /hello - Hello world endpoint';
    RAISE DEBUG '  + REST: POST /echo - Echo request body';
    RAISE DEBUG '  + REST: GET /health - Health check (no auth, no logging)';
    RAISE DEBUG '  + REST: GET /me - Current user profile (membership)';
    RAISE DEBUG '  + REST: GET /organizations - User organizations (RLS-filtered)';
END $$;
