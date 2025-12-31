/*
<pgmi-meta
    id="a7f01000-0009-4000-8000-000000000001"
    idempotent="true">
  <description>
    Protocol gateways: REST, RPC, and MCP request invocation
  </description>
  <sortKeys>
    <key>004/009</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing protocol gateways'; END $$;

-- ============================================================================
-- Authentication Context
-- ============================================================================

CREATE OR REPLACE FUNCTION api.set_auth_context(p_headers extensions.hstore)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_user_id text;
    v_max_len constant int := 4096;
BEGIN
    v_user_id := COALESCE(p_headers->'x-user-id', p_headers->'user-id');

    IF v_user_id IS NOT NULL AND length(v_user_id) <= v_max_len THEN
        PERFORM set_config('auth.user_id', v_user_id, true);
    END IF;

    IF p_headers->'x-user-email' IS NOT NULL AND length(p_headers->'x-user-email') <= v_max_len THEN
        PERFORM set_config('auth.user_email', p_headers->'x-user-email', true);
    END IF;

    IF p_headers->'x-tenant-id' IS NOT NULL AND length(p_headers->'x-tenant-id') <= v_max_len THEN
        PERFORM set_config('auth.tenant_id', p_headers->'x-tenant-id', true);
    END IF;

    IF p_headers->'authorization' IS NOT NULL AND length(p_headers->'authorization') <= v_max_len THEN
        PERFORM set_config('auth.token', p_headers->'authorization', true);
    END IF;
END;
$$;

-- ============================================================================
-- REST Gateway
-- ============================================================================

CREATE OR REPLACE FUNCTION api.rest_invoke(
    p_method text,
    p_url text,
    p_headers extensions.hstore DEFAULT ''::extensions.hstore,
    p_content bytea DEFAULT NULL
) RETURNS api.http_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
DECLARE
    v_request api.rest_request;
    v_response api.http_response;
    v_route record;
    v_version text;
    v_start_time timestamptz;
    v_execution_ms numeric;
BEGIN
    v_start_time := clock_timestamp();
    p_method := upper(trim(p_method));
    p_url := trim(p_url);
    p_headers := COALESCE(p_headers, ''::extensions.hstore);

    v_version := COALESCE(
        p_headers->'x-api-version',
        p_headers->'accept-version',
        ''
    );

    SELECT h.handler_exec_sql, h.object_id, h.response_headers, h.produces, h.requires_auth,
           r.route_name, r.auto_log
    INTO v_route
    FROM api.rest_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id
    WHERE p_url ~ r.address_regexp
      AND p_method ~ r.method_regexp
      AND v_version ~ r.version_regexp
    ORDER BY r.sequence_number DESC
    LIMIT 1;

    IF v_route.handler_exec_sql IS NULL THEN
        RETURN api.problem_response(404, 'Not Found', 'No route matches ' || p_method || ' ' || p_url);
    END IF;

    IF v_route.requires_auth AND (p_headers->'x-user-id') IS NULL THEN
        RETURN api.problem_response(401, 'Unauthorized', 'Authentication required: x-user-id header missing');
    END IF;

    IF p_headers->'accept' IS NOT NULL
       AND p_headers->'accept' NOT LIKE '%*/*%'
       AND NOT EXISTS (
           SELECT 1 FROM unnest(v_route.produces) AS p
           WHERE p_headers->'accept' LIKE '%' || p || '%'
       ) THEN
        RETURN api.problem_response(
            406,
            'Not Acceptable',
            format('Supported content types: %s', array_to_string(v_route.produces, ', '))
        );
    END IF;

    PERFORM api.set_auth_context(p_headers);

    v_request := (p_method, p_url, p_headers, p_content)::api.rest_request;

    BEGIN
        EXECUTE v_route.handler_exec_sql INTO v_response USING v_request;

        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;

        v_response.headers := extensions.hstore(ARRAY[
            'content-type', 'application/json',
            'x-execution-time-ms', v_execution_ms::text,
            'x-route-id', v_route.object_id::text
        ]) || COALESCE(v_response.headers, ''::extensions.hstore);

        IF v_route.auto_log THEN
            INSERT INTO api.rest_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_route.object_id, v_request, v_response, now());
        END IF;

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;
        RETURN api.problem_response(500, 'Internal Server Error', SQLERRM);
    END;
END;
$$;

-- ============================================================================
-- RPC Resolution
-- ============================================================================

CREATE OR REPLACE FUNCTION api.rpc_resolve(p_method_name text)
RETURNS uuid
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, pg_temp
AS $$
    SELECT handler_object_id FROM api.rpc_route WHERE method_name = p_method_name;
$$;

-- ============================================================================
-- RPC Gateway
-- ============================================================================

CREATE OR REPLACE FUNCTION api.rpc_invoke(
    p_route_id uuid,
    p_headers extensions.hstore DEFAULT ''::extensions.hstore,
    p_content bytea DEFAULT NULL
) RETURNS api.http_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
DECLARE
    v_request api.rpc_request;
    v_response api.http_response;
    v_handler record;
    v_route record;
    v_start_time timestamptz;
    v_execution_ms numeric;
    v_json_id jsonb;
BEGIN
    v_start_time := clock_timestamp();
    p_headers := COALESCE(p_headers, ''::extensions.hstore);

    BEGIN
        v_json_id := api.content_json(p_content)->'id';
    EXCEPTION WHEN OTHERS THEN
        v_json_id := NULL;
    END;

    SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.method_name, r.auto_log
    INTO v_handler
    FROM api.handler h
    JOIN api.rpc_route r ON r.handler_object_id = h.object_id
    WHERE h.object_id = p_route_id AND h.handler_type = 'rpc';

    IF v_handler.handler_exec_sql IS NULL THEN
        RETURN api.jsonrpc_error(-32601, 'Method not found', v_json_id);
    END IF;

    IF v_handler.requires_auth AND (p_headers->'x-user-id') IS NULL THEN
        RETURN api.jsonrpc_error(-32001, 'Authentication required: x-user-id header missing', v_json_id);
    END IF;

    PERFORM api.set_auth_context(p_headers);

    v_request := (p_route_id, p_headers, p_content)::api.rpc_request;

    BEGIN
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;

        v_response.headers := extensions.hstore(ARRAY[
            'x-execution-time-ms', v_execution_ms::text,
            'x-rpc-method', v_handler.method_name
        ]) || COALESCE(v_response.headers, ''::extensions.hstore);

        IF v_handler.auto_log THEN
            INSERT INTO api.rpc_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_handler.object_id, v_request, v_response, now());
        END IF;

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        RETURN api.jsonrpc_error(-32603, SQLERRM, v_json_id);
    END;
END;
$$;

-- ============================================================================
-- MCP Tool Invocation
-- ============================================================================

CREATE OR REPLACE FUNCTION api.mcp_call_tool(
    p_name text,
    p_arguments jsonb,
    p_context jsonb DEFAULT NULL,
    p_request_id text DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
DECLARE
    v_request api.mcp_request;
    v_response api.mcp_response;
    v_handler record;
BEGIN
    SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name
    INTO v_handler
    FROM api.handler h
    JOIN api.mcp_route r ON r.handler_object_id = h.object_id
    WHERE r.mcp_name = p_name AND r.mcp_type = 'tool';

    IF v_handler.handler_exec_sql IS NULL THEN
        RETURN api.mcp_error(-32601, 'Tool not found: ' || p_name, p_request_id);
    END IF;

    IF p_context IS NOT NULL THEN
        IF p_context->>'user_id' IS NOT NULL THEN
            PERFORM set_config('auth.user_id', p_context->>'user_id', true);
        END IF;
        IF p_context->>'tenant_id' IS NOT NULL THEN
            PERFORM set_config('auth.tenant_id', p_context->>'tenant_id', true);
        END IF;
    END IF;

    IF v_handler.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RETURN api.mcp_error(-32001, 'Authentication required: user_id missing from context', p_request_id);
    END IF;

    v_request := (p_arguments, NULL, p_context, p_request_id)::api.mcp_request;

    BEGIN
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
        VALUES (v_handler.object_id, 'tool', v_handler.mcp_name, v_request, v_response);

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        RETURN api.mcp_error(-32603, SQLERRM, p_request_id);
    END;
END;
$$;

-- ============================================================================
-- MCP Resource Read
-- ============================================================================

CREATE OR REPLACE FUNCTION api.mcp_read_resource(
    p_uri text,
    p_context jsonb DEFAULT NULL,
    p_request_id text DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
DECLARE
    v_request api.mcp_request;
    v_response api.mcp_response;
    v_handler record;
BEGIN
    SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name
    INTO v_handler
    FROM api.handler h
    JOIN api.mcp_route r ON r.handler_object_id = h.object_id
    WHERE r.mcp_type = 'resource'
      AND p_uri ~ ('^' || regexp_replace(r.uri_template, '\{[^}]+\}', '[^/]+', 'g') || '$');

    IF v_handler.handler_exec_sql IS NULL THEN
        RETURN api.mcp_error(-32601, 'Resource not found: ' || p_uri, p_request_id);
    END IF;

    IF p_context IS NOT NULL THEN
        IF p_context->>'user_id' IS NOT NULL THEN
            PERFORM set_config('auth.user_id', p_context->>'user_id', true);
        END IF;
        IF p_context->>'tenant_id' IS NOT NULL THEN
            PERFORM set_config('auth.tenant_id', p_context->>'tenant_id', true);
        END IF;
    END IF;

    IF v_handler.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RETURN api.mcp_error(-32001, 'Authentication required: user_id missing from context', p_request_id);
    END IF;

    v_request := (NULL, p_uri, p_context, p_request_id)::api.mcp_request;

    BEGIN
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
        VALUES (v_handler.object_id, 'resource', v_handler.mcp_name, v_request, v_response);

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        RETURN api.mcp_error(-32603, SQLERRM, p_request_id);
    END;
END;
$$;

-- ============================================================================
-- MCP Prompt Expansion
-- ============================================================================

CREATE OR REPLACE FUNCTION api.mcp_get_prompt(
    p_name text,
    p_arguments jsonb,
    p_context jsonb DEFAULT NULL,
    p_request_id text DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
DECLARE
    v_request api.mcp_request;
    v_response api.mcp_response;
    v_handler record;
BEGIN
    SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name
    INTO v_handler
    FROM api.handler h
    JOIN api.mcp_route r ON r.handler_object_id = h.object_id
    WHERE r.mcp_name = p_name AND r.mcp_type = 'prompt';

    IF v_handler.handler_exec_sql IS NULL THEN
        RETURN api.mcp_error(-32601, 'Prompt not found: ' || p_name, p_request_id);
    END IF;

    IF p_context IS NOT NULL THEN
        IF p_context->>'user_id' IS NOT NULL THEN
            PERFORM set_config('auth.user_id', p_context->>'user_id', true);
        END IF;
        IF p_context->>'tenant_id' IS NOT NULL THEN
            PERFORM set_config('auth.tenant_id', p_context->>'tenant_id', true);
        END IF;
    END IF;

    IF v_handler.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RETURN api.mcp_error(-32001, 'Authentication required: user_id missing from context', p_request_id);
    END IF;

    v_request := (p_arguments, NULL, p_context, p_request_id)::api.mcp_request;

    BEGIN
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
        VALUES (v_handler.object_id, 'prompt', v_handler.mcp_name, v_request, v_response);

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        RETURN api.mcp_error(-32603, SQLERRM, p_request_id);
    END;
END;
$$;

-- ============================================================================
-- MCP Discovery Functions
-- ============================================================================

CREATE OR REPLACE FUNCTION api.mcp_list_tools()
RETURNS jsonb
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, core, pg_temp
AS $$
    SELECT jsonb_build_object('tools', COALESCE(jsonb_agg(
        jsonb_build_object(
            'name', r.mcp_name,
            'description', core.get_attached_text(r.handler_object_id, 'description'),
            'inputSchema', r.input_schema
        )
    ), '[]'::jsonb))
    FROM api.mcp_route r
    WHERE r.mcp_type = 'tool';
$$;

CREATE OR REPLACE FUNCTION api.mcp_list_resources()
RETURNS jsonb
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, core, pg_temp
AS $$
    SELECT jsonb_build_object('resources', COALESCE(jsonb_agg(
        jsonb_build_object(
            'name', r.mcp_name,
            'description', core.get_attached_text(r.handler_object_id, 'description'),
            'uri', r.uri_template,
            'mimeType', r.mime_type
        )
    ), '[]'::jsonb))
    FROM api.mcp_route r
    WHERE r.mcp_type = 'resource';
$$;

CREATE OR REPLACE FUNCTION api.mcp_list_prompts()
RETURNS jsonb
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, core, pg_temp
AS $$
    SELECT jsonb_build_object('prompts', COALESCE(jsonb_agg(
        jsonb_build_object(
            'name', r.mcp_name,
            'description', core.get_attached_text(r.handler_object_id, 'description'),
            'arguments', r.arguments
        )
    ), '[]'::jsonb))
    FROM api.mcp_route r
    WHERE r.mcp_type = 'prompt';
$$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.set_auth_context() - authentication header extraction';
    RAISE NOTICE '  ✓ api.rest_invoke() - REST gateway with URL routing';
    RAISE NOTICE '  ✓ api.rpc_resolve() - RPC method name to UUID resolution';
    RAISE NOTICE '  ✓ api.rpc_invoke() - RPC gateway with UUID-based invocation';
    RAISE NOTICE '  ✓ api.mcp_call_tool() - MCP tool invocation';
    RAISE NOTICE '  ✓ api.mcp_read_resource() - MCP resource read';
    RAISE NOTICE '  ✓ api.mcp_get_prompt() - MCP prompt expansion';
    RAISE NOTICE '  ✓ api.mcp_list_tools/resources/prompts() - MCP discovery';
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
