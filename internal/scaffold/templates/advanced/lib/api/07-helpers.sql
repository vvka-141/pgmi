/*
<pgmi-meta
    id="a7f01000-0007-4000-8000-000000000001"
    idempotent="true">
  <description>
    Helper functions: content parsing, URL parsing, and response builders
  </description>
  <sortKeys>
    <key>004/007</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing API helper functions'; END $$;

-- ============================================================================
-- Content Parsing
-- ============================================================================

CREATE OR REPLACE FUNCTION api.content_json(content bytea)
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT convert_from(content, 'UTF8')::jsonb;
$$;

CREATE OR REPLACE FUNCTION api.content_text(content bytea)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT convert_from(content, 'UTF8');
$$;

CREATE OR REPLACE FUNCTION api.header(headers extensions.hstore, name text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT headers->lower(name);
$$;

-- Inline tests
DO $$
DECLARE
    v_json jsonb;
    v_text text;
    v_header text;
    v_headers extensions.hstore;
BEGIN
    v_json := api.content_json(convert_to('{"key": "value"}', 'UTF8'));
    IF v_json->>'key' != 'value' THEN
        RAISE EXCEPTION 'content_json failed';
    END IF;

    v_text := api.content_text(convert_to('hello world', 'UTF8'));
    IF v_text != 'hello world' THEN
        RAISE EXCEPTION 'content_text failed';
    END IF;

    v_headers := extensions.hstore(ARRAY['content-type', 'application/json']);
    v_header := api.header(v_headers, 'Content-Type');
    IF v_header != 'application/json' THEN
        RAISE EXCEPTION 'header case-insensitive lookup failed';
    END IF;
END $$;

-- ============================================================================
-- URL Parsing
-- ============================================================================

CREATE OR REPLACE FUNCTION api.query_params(url text)
RETURNS extensions.hstore
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    WITH params AS (
        SELECT unnest(string_to_array(split_part(url, '?', 2), '&')) AS param
    )
    SELECT COALESCE(
        extensions.hstore(
            array_agg(split_part(param, '=', 1)),
            array_agg(COALESCE(nullif(split_part(param, '=', 2), ''), ''))
        ),
        ''::extensions.hstore
    )
    FROM params WHERE param != '';
$$;

CREATE OR REPLACE FUNCTION api.url_path(url text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT split_part(url, '?', 1);
$$;

-- Inline tests
DO $$
DECLARE
    v_params extensions.hstore;
    v_path text;
BEGIN
    v_params := api.query_params('/api/users?name=john&age=30');
    IF v_params->'name' != 'john' OR v_params->'age' != '30' THEN
        RAISE EXCEPTION 'query_params failed';
    END IF;

    v_path := api.url_path('/api/users?name=john');
    IF v_path != '/api/users' THEN
        RAISE EXCEPTION 'url_path failed';
    END IF;
END $$;

-- ============================================================================
-- Response Builders
-- ============================================================================

CREATE OR REPLACE FUNCTION api.json_response(
    status_code integer,
    content jsonb
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (
        status_code,
        extensions.hstore('content-type', 'application/json'),
        convert_to(content::text, 'UTF8')
    )::api.http_response;
$$;

CREATE OR REPLACE FUNCTION api.problem_response(
    status_code integer,
    title text,
    detail text DEFAULT NULL,
    type_uri text DEFAULT NULL,
    instance text DEFAULT NULL
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (
        status_code,
        extensions.hstore('content-type', 'application/problem+json'),
        convert_to(
            jsonb_strip_nulls(jsonb_build_object(
                'type', COALESCE(type_uri, 'about:blank'),
                'title', title,
                'status', status_code,
                'detail', detail,
                'instance', instance
            ))::text,
            'UTF8'
        )
    )::api.http_response;
$$;

CREATE OR REPLACE FUNCTION api.error_response(
    status_code integer,
    message text
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.problem_response(status_code, message);
$$;

-- Inline tests
DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    v_response := api.json_response(200, '{"status": "ok"}'::jsonb);
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'json_response status failed';
    END IF;

    v_response := api.problem_response(404, 'Not Found', 'Resource does not exist');
    v_content := api.content_json((v_response).content);
    IF v_content->>'title' != 'Not Found' THEN
        RAISE EXCEPTION 'problem_response failed';
    END IF;
END $$;

-- ============================================================================
-- JSON-RPC Response Builders
-- ============================================================================

CREATE OR REPLACE FUNCTION api.jsonrpc_success(
    result jsonb,
    id jsonb DEFAULT NULL
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.json_response(200, jsonb_build_object(
        'jsonrpc', '2.0',
        'result', result,
        'id', id
    ));
$$;

CREATE OR REPLACE FUNCTION api.jsonrpc_error(
    code integer,
    message text,
    id jsonb DEFAULT NULL
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.json_response(
        CASE code
            WHEN -32700 THEN 400
            WHEN -32600 THEN 400
            WHEN -32601 THEN 404
            WHEN -32602 THEN 400
            WHEN -32603 THEN 500
            WHEN -32001 THEN 401
            ELSE 500
        END,
        jsonb_build_object(
            'jsonrpc', '2.0',
            'error', jsonb_build_object('code', code, 'message', message),
            'id', id
        )
    );
$$;

-- Inline tests
DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    v_response := api.jsonrpc_success('{"value": 42}'::jsonb, '"req-1"'::jsonb);
    v_content := api.content_json((v_response).content);
    IF v_content->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'jsonrpc_success failed';
    END IF;

    v_response := api.jsonrpc_error(-32601, 'Method not found');
    IF (v_response).status_code != 404 THEN
        RAISE EXCEPTION 'jsonrpc_error status mapping failed: expected 404, got %', (v_response).status_code;
    END IF;
END $$;

-- ============================================================================
-- MCP Response Builders (JSON-RPC 2.0 Compliant)
-- ============================================================================

CREATE OR REPLACE FUNCTION api.mcp_success(
    result jsonb,
    request_id text
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ROW(
        jsonb_build_object(
            'jsonrpc', '2.0',
            'id', request_id,
            'result', result
        )
    )::api.mcp_response;
$$;

CREATE OR REPLACE FUNCTION api.mcp_error(
    code integer,
    message text,
    request_id text,
    data jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ROW(
        jsonb_build_object(
            'jsonrpc', '2.0',
            'id', request_id,
            'error', jsonb_strip_nulls(jsonb_build_object(
                'code', code,
                'message', message,
                'data', data
            ))
        )
    )::api.mcp_response;
$$;

CREATE OR REPLACE FUNCTION api.mcp_tool_result(
    content jsonb,
    request_id text
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success(
        jsonb_build_object('content', content),
        request_id
    );
$$;

CREATE OR REPLACE FUNCTION api.mcp_tool_error(
    message text,
    request_id text
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_error(-32603, message, request_id);
$$;

CREATE OR REPLACE FUNCTION api.mcp_resource_result(
    contents jsonb,
    request_id text
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success(
        jsonb_build_object('contents', contents),
        request_id
    );
$$;

CREATE OR REPLACE FUNCTION api.mcp_resource_error(
    message text,
    request_id text
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_error(-32603, message, request_id);
$$;

CREATE OR REPLACE FUNCTION api.mcp_prompt_result(
    messages jsonb,
    request_id text
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success(
        jsonb_build_object('messages', messages),
        request_id
    );
$$;

CREATE OR REPLACE FUNCTION api.mcp_prompt_error(
    message text,
    request_id text
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_error(-32603, message, request_id);
$$;

CREATE OR REPLACE FUNCTION api.mcp_text(content text)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object('type', 'text', 'text', content);
$$;

-- Inline tests for MCP response builders
DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
    v_text jsonb;
BEGIN
    -- Test mcp_success
    v_response := api.mcp_success('{"value": 42}'::jsonb, 'req-1');
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'mcp_success: missing jsonrpc 2.0';
    END IF;
    IF v_envelope->>'id' != 'req-1' THEN
        RAISE EXCEPTION 'mcp_success: wrong id';
    END IF;
    IF v_envelope->'result' IS NULL THEN
        RAISE EXCEPTION 'mcp_success: missing result';
    END IF;

    -- Test mcp_error
    v_response := api.mcp_error(-32603, 'Internal error', 'req-2');
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'mcp_error: missing jsonrpc 2.0';
    END IF;
    IF v_envelope->>'id' != 'req-2' THEN
        RAISE EXCEPTION 'mcp_error: wrong id';
    END IF;
    IF (v_envelope->'error'->>'code')::int != -32603 THEN
        RAISE EXCEPTION 'mcp_error: wrong error code';
    END IF;

    -- Test mcp_tool_result
    v_response := api.mcp_tool_result('[{"type": "text", "text": "Hello"}]'::jsonb, 'req-3');
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'mcp_tool_result: missing jsonrpc 2.0';
    END IF;
    IF v_envelope->'result'->'content' IS NULL THEN
        RAISE EXCEPTION 'mcp_tool_result: missing content in result';
    END IF;

    -- Test mcp_tool_error
    v_response := api.mcp_tool_error('Tool failed', 'req-4');
    v_envelope := (v_response).envelope;
    IF v_envelope->'error' IS NULL THEN
        RAISE EXCEPTION 'mcp_tool_error: missing error object';
    END IF;

    -- Test mcp_text helper
    v_text := api.mcp_text('Hello world');
    IF v_text->>'type' != 'text' OR v_text->>'text' != 'Hello world' THEN
        RAISE EXCEPTION 'mcp_text failed';
    END IF;
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.content_json/text - bytea content parsing';
    RAISE NOTICE '  ✓ api.header - case-insensitive header lookup';
    RAISE NOTICE '  ✓ api.query_params/url_path - URL parsing';
    RAISE NOTICE '  ✓ api.json_response - JSON response builder';
    RAISE NOTICE '  ✓ api.problem_response - RFC 7807 error response';
    RAISE NOTICE '  ✓ api.jsonrpc_success/error - JSON-RPC 2.0 responses';
    RAISE NOTICE '  ✓ api.mcp_success/error - MCP JSON-RPC 2.0 base builders';
    RAISE NOTICE '  ✓ api.mcp_tool_result/error - MCP tool response builders';
    RAISE NOTICE '  ✓ api.mcp_resource_result/error - MCP resource response builders';
    RAISE NOTICE '  ✓ api.mcp_prompt_result/error - MCP prompt response builders';
END $$;

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO %I', v_api_role);
END $$;
