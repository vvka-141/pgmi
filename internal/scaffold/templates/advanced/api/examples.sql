/*
<pgmi-meta
    id="a7f01000-0010-4000-8000-000000000001"
    idempotent="true">
  <description>
    Example handlers demonstrating REST, RPC, and MCP protocols
  </description>
  <sortKeys>
    <key>005/001</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing example handlers'; END $$;

-- ============================================================================
-- REST Example: Hello World
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0001-4000-8000-000000000001',
        'uri', '^/hello(\\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'hello_world',
        'description', 'Simple hello world endpoint'
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

-- ============================================================================
-- REST Example: Echo
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0002-4000-8000-000000000001',
        'uri', '^/echo(\\?.*)?$',
        'httpMethod', '^POST$',
        'name', 'echo',
        'description', 'Echo back the request body'
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

-- ============================================================================
-- REST Example: Health Check
-- ============================================================================

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0003-4000-8000-000000000001',
        'uri', '^/health(\\?.*)?$',
        'httpMethod', '^GET$',
        'name', 'health_check',
        'description', 'Kubernetes liveness probe endpoint',
        'autoLog', false,
        'requiresAuth', false
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

-- ============================================================================
-- RPC Example: Calculate Sum
-- ============================================================================

SELECT api.create_or_replace_rpc_handler(
    jsonb_build_object(
        'id', 'e2000001-0001-4000-8000-000000000001',
        'methodName', 'math.sum',
        'description', 'Calculate sum of two numbers'
    ),
    $body$
DECLARE
    v_params jsonb;
    v_a numeric;
    v_b numeric;
BEGIN
    v_params := api.content_json((request).content)->'params';
    v_a := (v_params->>'a')::numeric;
    v_b := (v_params->>'b')::numeric;

    RETURN api.jsonrpc_success(
        jsonb_build_object('result', v_a + v_b),
        api.content_json((request).content)->'id'
    );
END;
    $body$
);

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

-- ============================================================================
-- MCP Example: Database Info Tool
-- ============================================================================

SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0001-4000-8000-000000000001',
        'type', 'tool',
        'name', 'database_info',
        'description', 'Get database version and connection info',
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(),
            'required', jsonb_build_array()
        )
    ),
    $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text(format(
            'Database: %s, Version: %s, User: %s',
            current_database(),
            version(),
            current_user
        ))),
        (request).request_id,
        false
    );
END;
    $body$
);

-- ============================================================================
-- MCP Example: Table Schema Resource
-- ============================================================================

SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0002-4000-8000-000000000001',
        'type', 'resource',
        'name', 'table_schema',
        'description', 'Get table schema information',
        'uriTemplate', 'postgres:///{schema}/{table}',
        'mimeType', 'application/json'
    ),
    $body$
DECLARE
    v_uri_parts text[];
    v_schema text;
    v_table text;
    v_columns jsonb;
BEGIN
    v_uri_parts := string_to_array(regexp_replace((request).uri, '^postgres:///', ''), '/');
    v_schema := v_uri_parts[1];
    v_table := v_uri_parts[2];

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

-- ============================================================================
-- MCP Example: SQL Query Prompt
-- ============================================================================

SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'e3000001-0003-4000-8000-000000000001',
        'type', 'prompt',
        'name', 'sql_assistant',
        'description', 'Generate a SQL query assistant prompt',
        'arguments', jsonb_build_array(
            jsonb_build_object('name', 'task', 'description', 'Task description', 'required', true),
            jsonb_build_object('name', 'tables', 'description', 'Relevant tables', 'required', false)
        )
    ),
    $body$
DECLARE
    v_task text;
    v_tables text;
BEGIN
    v_task := (request).arguments->>'task';
    v_tables := COALESCE((request).arguments->>'tables', 'any relevant tables');

    RETURN (
        jsonb_build_object('messages', jsonb_build_array(
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
        )),
        (request).request_id
    )::api.mcp_response;
END;
    $body$
);

DO $$ BEGIN
    RAISE NOTICE '  ✓ REST: GET /hello - Hello world endpoint';
    RAISE NOTICE '  ✓ REST: POST /echo - Echo request body';
    RAISE NOTICE '  ✓ REST: GET /health - Health check (no logging)';
    RAISE NOTICE '  ✓ RPC: math.sum - Calculate sum of two numbers';
    RAISE NOTICE '  ✓ RPC: system.time - Get server time';
    RAISE NOTICE '  ✓ MCP Tool: database_info - Get database info';
    RAISE NOTICE '  ✓ MCP Resource: table_schema - Get table schema';
    RAISE NOTICE '  ✓ MCP Prompt: sql_assistant - SQL query assistant';
END $$;
