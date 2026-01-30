/*
<pgmi-meta
    id="a7f01000-0010-4000-8000-000000000002"
    idempotent="true">
  <description>
    Example handlers demonstrating REST, RPC, and MCP protocols.

    Each example shows the handler registration pattern:
    1. Call api.create_or_replace_rest/rpc/mcp_handler() with two arguments
    2. First argument: JSONB metadata describing the handler (routing, auth, etc.)
    3. Second argument: the handler function body that processes requests

    After each registration, an invocation demo shows how to call the handler
    through the gateway and read the response using api.content_json().

    These examples serve as a getting-started guide for new developers.
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
--        - language:     Handler language: plpgsql (default), sql, plv8
--        - requiresAuth: If true, gateway rejects requests without x-user-id header
--        - autoLog:      If true, request/response logged to api.rest_exchange
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
    -- Metadata: tells the router how to find and configure this handler
    jsonb_build_object(
        'id', 'e1000001-0001-4000-8000-000000000001',
        'uri', '^/hello(\\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'hello_world',
        'description', 'Simple hello world endpoint'
    ),
    -- Handler body: invoked by the router when the URL matches.
    -- "request" is the api.rest_request parameter injected by the framework.
    $body$
DECLARE
    v_name text;  -- Greeting target from query parameter
BEGIN
    -- Parse ?name=X from the URL. Defaults to 'World' if not provided.
    v_name := COALESCE(api.query_params((request).url)->'name', 'World');
    RETURN api.json_response(200, jsonb_build_object(
        'message', 'Hello, ' || v_name || '!',
        'timestamp', now()
    ));
END;
    $body$
);

-- Invoke through the REST gateway, just like the Python gateway would:
DO $$
DECLARE
    v_response api.http_response;
BEGIN
    -- GET /hello?name=Developer — routed by URI regex match
    v_response := api.rest_invoke('GET', '/hello?name=Developer');
    RAISE DEBUG '  → GET /hello?name=Developer  status=%, body=%',
        (v_response).status_code,
        api.content_json((v_response).content);
END $$;

-- ============================================================================
-- REST Example: Echo
-- ============================================================================
-- Demonstrates reading the request body as JSON.

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0002-4000-8000-000000000001',
        'uri', '^/echo(\\?.*)?$',
        'httpMethod', '^POST$',
        'name', 'echo',
        'description', 'Echo back the request body'
    ),
    -- The handler body echoes back method, URL, and parsed JSON body.
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

-- Invoke with a JSON body using the JSONB overload (auto-sets content-type):
DO $$
DECLARE
    v_response api.http_response;
BEGIN
    -- POST /echo with JSON body — uses the JSONB overload of rest_invoke
    v_response := api.rest_invoke('POST', '/echo', ''::extensions.hstore,
        '{"greeting": "hello from examples.sql"}'::jsonb);
    RAISE DEBUG '  → POST /echo  status=%, body=%',
        (v_response).status_code,
        api.content_json((v_response).content);
END $$;

-- ============================================================================
-- REST Example: Health Check (No Auth, No Logging)
-- ============================================================================
-- Demonstrates disabling authentication and exchange logging.
-- Ideal for Kubernetes liveness/readiness probes that fire frequently.

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0003-4000-8000-000000000001',
        'uri', '^/health(\\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'health_check',
        'description', 'Kubernetes liveness probe endpoint',
        'autoLog', false,        -- Don't flood rest_exchange with probe traffic
        'requiresAuth', false    -- Probes don't carry JWT tokens
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

-- Invoke the health check — no auth headers needed:
DO $$
DECLARE
    v_response api.http_response;
BEGIN
    v_response := api.rest_invoke('GET', '/health');
    RAISE DEBUG '  → GET /health  status=%, body=%',
        (v_response).status_code,
        api.content_json((v_response).content);
END $$;

-- ============================================================================
-- RPC Example: Calculate Sum
-- ============================================================================
-- Demonstrates a JSON-RPC handler. RPC handlers differ from REST:
-- - Matched by method name (not URL pattern)
-- - Request body is a JSON-RPC envelope: {"jsonrpc":"2.0","method":"...","params":{...},"id":...}
-- - Response uses api.jsonrpc_success() / api.jsonrpc_error()

SELECT api.create_or_replace_rpc_handler(
    jsonb_build_object(
        'id', 'e2000001-0001-4000-8000-000000000001',
        'methodName', 'math.sum',          -- JSON-RPC method name
        'description', 'Calculate sum of two numbers'
    ),
    $body$
DECLARE
    v_params jsonb;  -- JSON-RPC params object
    v_a numeric;
    v_b numeric;
BEGIN
    -- Extract params from the JSON-RPC envelope
    v_params := api.content_json((request).content)->'params';
    v_a := (v_params->>'a')::numeric;
    v_b := (v_params->>'b')::numeric;

    -- Return a JSON-RPC 2.0 success response.
    -- The id must be echoed back for request/response correlation.
    RETURN api.jsonrpc_success(
        jsonb_build_object('result', v_a + v_b),
        api.content_json((request).content)->'id'
    );
END;
    $body$
);

-- Invoke through the RPC gateway with a JSON-RPC envelope:
DO $$
DECLARE
    v_route_id uuid;
    v_response api.http_response;
BEGIN
    -- Step 1: Resolve the method name to a handler UUID
    v_route_id := api.rpc_resolve('math.sum');

    -- Step 2: Invoke with a JSON-RPC 2.0 envelope as the content body
    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc":"2.0","method":"math.sum","params":{"a":3,"b":4},"id":1}'::text, 'UTF8')
    );
    RAISE DEBUG '  → RPC math.sum(3,4)  status=%, body=%',
        (v_response).status_code,
        api.content_json((v_response).content);
END $$;

-- ============================================================================
-- RPC Example: Get Server Time
-- ============================================================================

SELECT api.create_or_replace_rpc_handler(
    jsonb_build_object(
        'id', 'e2000001-0002-4000-8000-000000000001',
        'methodName', 'system.time',
        'description', 'Get current server time'
    ),
    $body$
BEGIN
    RETURN api.jsonrpc_success(
        jsonb_build_object(
            'timestamp', now(),
            'timezone', current_setting('TIMEZONE')
        ),
        api.content_json((request).content)->'id'
    );
END;
    $body$
);

-- Invoke system.time:
DO $$
DECLARE
    v_route_id uuid;
    v_response api.http_response;
BEGIN
    v_route_id := api.rpc_resolve('system.time');
    v_response := api.rpc_invoke(
        v_route_id,
        ''::extensions.hstore,
        convert_to('{"jsonrpc":"2.0","method":"system.time","params":{},"id":2}'::text, 'UTF8')
    );
    RAISE DEBUG '  → RPC system.time  status=%, body=%',
        (v_response).status_code,
        api.content_json((v_response).content);
END $$;

-- ============================================================================
-- MCP Example: Database Info Tool
-- ============================================================================
-- Demonstrates an MCP tool handler. MCP tools differ from REST/RPC:
-- - Discovered via api.mcp_list_tools() (clients enumerate capabilities)
-- - Called via api.mcp_call_tool(name, arguments, context, request_id)
-- - Request is api.mcp_request (arguments jsonb, uri text, context jsonb, request_id text)
-- - Response is api.mcp_response (JSON-RPC 2.0 envelope)
-- - Use api.mcp_tool_result() and api.mcp_text() to build responses

SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0001-4000-8000-000000000001',
        'type', 'tool',                     -- MCP type: tool, resource, or prompt
        'name', 'database_info',
        'description', 'Get database version and connection info',
        'inputSchema', jsonb_build_object(   -- JSON Schema for tool inputs
            'type', 'object',
            'properties', jsonb_build_object(),
            'required', jsonb_build_array()
        )
    ),
    $body$
BEGIN
    -- Build a tool result containing text content blocks
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text(format(
            'Database: %s, Version: %s, User: %s',
            current_database(),
            version(),
            current_user
        ))),
        (request).request_id
    );
END;
    $body$
);

-- Invoke the MCP tool — returns a JSON-RPC 2.0 envelope:
DO $$
DECLARE
    v_response api.mcp_response;
BEGIN
    -- api.mcp_call_tool(name, arguments, context, request_id)
    v_response := api.mcp_call_tool('database_info', '{}'::jsonb, NULL, 'demo-1');
    RAISE DEBUG '  → MCP tool database_info  envelope=%', (v_response).envelope;
END $$;

-- ============================================================================
-- MCP Example: Table Schema Resource
-- ============================================================================
-- MCP resources are read via URI. The uri_template uses {placeholders} that
-- become regex capture groups for URL matching.

SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0002-4000-8000-000000000001',
        'type', 'resource',
        'name', 'table_schema',
        'description', 'Get table schema information',
        'uriTemplate', 'postgres:///{schema}/{table}',  -- Template variables become [^/]+ regex
        'mimeType', 'application/json'
    ),
    $body$
DECLARE
    v_uri_parts text[];  -- URI path segments after protocol prefix
    v_schema text;
    v_table text;
    v_columns jsonb;
BEGIN
    -- Parse the resource URI to extract schema and table names
    v_uri_parts := string_to_array(regexp_replace((request).uri, '^postgres:///', ''), '/');
    v_schema := v_uri_parts[1];
    v_table := v_uri_parts[2];

    -- Query information_schema for column metadata
    SELECT jsonb_agg(jsonb_build_object(
        'column_name', column_name,
        'data_type', data_type,
        'is_nullable', is_nullable
    ))
    INTO v_columns
    FROM information_schema.columns
    WHERE table_schema = COALESCE(v_schema, 'public')
      AND table_name = v_table;

    RETURN api.mcp_resource_result(
        jsonb_build_array(jsonb_build_object(
            'uri', (request).uri,
            'mimeType', 'application/json',
            'text', COALESCE(v_columns, '[]'::jsonb)::text
        )),
        (request).request_id
    );
END;
    $body$
);

-- Invoke the MCP resource — read the api.handler table schema:
DO $$
DECLARE
    v_response api.mcp_response;
BEGIN
    -- api.mcp_read_resource(uri, context, request_id)
    v_response := api.mcp_read_resource('postgres:///api/handler', NULL, 'demo-2');
    RAISE DEBUG '  → MCP resource postgres:///api/handler  envelope=%', (v_response).envelope;
END $$;

-- ============================================================================
-- MCP Example: SQL Query Prompt
-- ============================================================================
-- MCP prompts are templates that expand into conversation messages.
-- Clients discover prompts via api.mcp_list_prompts() and expand them
-- via api.mcp_get_prompt(name, arguments).

SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0003-4000-8000-000000000001',
        'type', 'prompt',
        'name', 'sql_assistant',
        'description', 'Generate a SQL query assistant prompt',
        'arguments', jsonb_build_array(   -- Prompt arguments (typed parameters)
            jsonb_build_object('name', 'task', 'description', 'Task description', 'required', true),
            jsonb_build_object('name', 'tables', 'description', 'Relevant tables', 'required', false)
        )
    ),
    $body$
DECLARE
    v_task text;    -- User's task description
    v_tables text;  -- Optional table hint
BEGIN
    -- Extract arguments from the MCP request
    v_task := (request).arguments->>'task';
    v_tables := COALESCE((request).arguments->>'tables', 'any relevant tables');

    -- Build a prompt result: array of {role, content} message objects
    RETURN api.mcp_prompt_result(
        jsonb_build_array(
            jsonb_build_object(
                'role', 'user',
                'content', jsonb_build_object(
                    'type', 'text',
                    'text', format(
                        'You are a PostgreSQL expert. Help me write a SQL query for: %s. Consider using: %s.',
                        v_task, v_tables
                    )
                )
            )
        ),
        (request).request_id
    );
END;
    $body$
);

-- Invoke the MCP prompt — expands the template with arguments:
DO $$
DECLARE
    v_response api.mcp_response;
BEGIN
    -- api.mcp_get_prompt(name, arguments, context, request_id)
    v_response := api.mcp_get_prompt(
        'sql_assistant',
        '{"task": "find all critical path activities", "tables": "project_design.activity"}'::jsonb,
        NULL, 'demo-3'
    );
    RAISE DEBUG '  → MCP prompt sql_assistant  envelope=%', (v_response).envelope;
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
        'requiresAuth', true
    ),
    $body$
DECLARE
    v_user jsonb;
BEGIN
    SELECT jsonb_build_object(
        'user_id', user_id,
        'email', email,
        'display_name', display_name,
        'email_verified', email_verified,
        'member_org_ids', member_org_ids,
        'owner_org_ids', owner_org_ids
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
        'requiresAuth', true
    ),
    $body$
DECLARE
    v_orgs jsonb;
BEGIN
    SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'id', o.object_id,
        'name', o.name,
        'slug', o.slug,
        'is_personal', o.is_personal
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
    RAISE DEBUG '  + RPC: math.sum - Calculate sum of two numbers';
    RAISE DEBUG '  + RPC: system.time - Get server time';
    RAISE DEBUG '  + MCP Tool: database_info - Get database info';
    RAISE DEBUG '  + MCP Resource: table_schema - Get table schema';
    RAISE DEBUG '  + MCP Prompt: sql_assistant - SQL query assistant';
END $$;
