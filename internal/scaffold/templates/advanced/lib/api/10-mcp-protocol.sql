/*
<pgmi-meta
    id="a7f01000-0010-4000-8000-000000000001"
    idempotent="true">
  <description>
    MCP protocol layer: initialize handshake, ping, and unified request dispatcher.

    This file implements the core Model Context Protocol (MCP) server functionality,
    enabling AI clients (like Claude Desktop, VS Code Copilot, etc.) to connect to
    PostgreSQL and invoke tools, read resources, and expand prompts.

    Key Components:
    - api.mcp_server_info()        - Returns server identity (name, version)
    - api.mcp_server_capabilities() - Declares supported features (tools, resources, prompts)
    - api.mcp_initialize()         - Handles the MCP handshake (required first call)
    - api.mcp_ping()               - Keepalive response
    - api.mcp_handle_request()     - Unified dispatcher routing all MCP JSON-RPC methods

    Protocol Compliance:
    - JSON-RPC 2.0 envelope format
    - MCP specification versions: 2024-11-05, 2025-03-26, 2025-06-18, 2025-11-25
      (unknown versions negotiate down to the server's best supported version)

    Usage from HTTP Gateway:
      SELECT (api.mcp_handle_request($request_json, $context_json)).envelope;

    Usage from psql:
      SELECT (api.mcp_handle_request('{"jsonrpc":"2.0","id":"1","method":"initialize",
        "params":{"protocolVersion":"2024-11-05"}}'::jsonb)).envelope;
  </description>
  <sortKeys>
    <key>004/010</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '-> Installing MCP protocol layer'; END $$;

-- ============================================================================
-- MCP Server Info
-- ============================================================================
-- Returns server identity for the MCP handshake response.
--
-- The server name and version can be configured via session settings:
--   SET mcp.server_name = 'my-database-server';
--   SET mcp.server_version = '2.0.0';
--
-- If not configured, defaults to the database name and version 1.0.0.
--
-- Returns:
--   {"name": "database_name", "version": "1.0.0"}
--
-- Example:
--   SELECT api.mcp_server_info();
--   -- {"name": "myapp", "version": "1.0.0"}

CREATE OR REPLACE FUNCTION api.mcp_server_info()
RETURNS jsonb
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object(
        'name', COALESCE(
            NULLIF(current_setting('mcp.server_name', true), ''),
            current_database()
        ),
        'version', COALESCE(
            NULLIF(current_setting('mcp.server_version', true), ''),
            '1.0.0'
        )
    );
$$;

-- ============================================================================
-- MCP Server Capabilities
-- ============================================================================
-- Declares which MCP features this server supports.
--
-- Currently declares support for:
--   - tools: Executable actions (tools/list, tools/call)
--   - resources: Data access (resources/list, resources/read)
--   - prompts: Message templates (prompts/list, prompts/get)
--
-- Known gap: listChanged notifications are NOT declared because pgmi does not
-- yet emit notifications/{tools,resources,prompts}/list_changed when the MCP
-- registry mutates. Long-lived clients will see a stale list until reconnect.
-- Integration path: LISTEN/NOTIFY on a channel fired by registration
-- functions, fanned out by the MCP transport gateway. Declaring listChanged
-- capability MUST remain in lockstep with that notification plumbing.
--
-- Returns:
--   {"tools": {}, "resources": {}, "prompts": {}}
--
-- Example:
--   SELECT api.mcp_server_capabilities();

CREATE OR REPLACE FUNCTION api.mcp_server_capabilities()
RETURNS jsonb
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object(
        'tools', jsonb_build_object(),
        'resources', jsonb_build_object(),
        'prompts', jsonb_build_object()
    );
$$;

COMMENT ON FUNCTION api.mcp_server_capabilities() IS
    'MCP capabilities advertised during initialize. Empty objects declare support without listChanged — pgmi does not yet emit list_changed notifications; see function body for integration notes.';

-- ============================================================================
-- MCP Initialize Handler
-- ============================================================================
-- Handles the MCP initialization handshake. This MUST be the first method
-- called by any MCP client before invoking tools, resources, or prompts.
--
-- The client sends its desired protocol version; the server validates it
-- and returns server info and capabilities.
--
-- Parameters:
--   p_params     - JSON object with "protocolVersion" (e.g., "2024-11-05")
--   p_request_id - JSON-RPC request ID to echo in response
--
-- Returns:
--   Success: {jsonrpc: "2.0", id: "...", result: {protocolVersion, serverInfo, capabilities}}
--   Error:   {jsonrpc: "2.0", id: "...", error: {code: -32602, message: "..."}}
--
-- Supported Protocol Versions:
--   - "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"
--   - An unknown version negotiates to the server's best (newest) supported
--     version rather than erroring, per the MCP lifecycle.
--
-- Example Request:
--   {"jsonrpc": "2.0", "id": "1", "method": "initialize",
--    "params": {"protocolVersion": "2024-11-05"}}
--
-- Example Response:
--   {"jsonrpc": "2.0", "id": "1", "result": {
--     "protocolVersion": "2024-11-05",
--     "serverInfo": {"name": "mydb", "version": "1.0.0"},
--     "capabilities": {"tools": {}, "resources": {}, "prompts": {}}
--   }}

DROP FUNCTION IF EXISTS api.mcp_initialize(jsonb, text);

CREATE OR REPLACE FUNCTION api.mcp_initialize(
    p_params jsonb,
    p_request_id jsonb
) RETURNS api.mcp_response
LANGUAGE plpgsql STABLE AS $$
DECLARE
    v_client_version text;
    -- Ascending order: the last element is the server's best (newest) version.
    v_supported_versions text[] := ARRAY['2024-11-05', '2025-03-26', '2025-06-18', '2025-11-25'];
    v_negotiated_version text;
BEGIN
    v_client_version := p_params->>'protocolVersion';

    IF v_client_version IS NULL THEN
        RETURN api.mcp_error(-32602, 'Missing required parameter: protocolVersion', p_request_id);
    END IF;

    -- Negotiate per the MCP lifecycle: echo the client's version if supported,
    -- otherwise return the server's best supported version and let the client
    -- decide whether to proceed or disconnect. (Erroring on mismatch breaks
    -- current clients that propose a newer date than the server knows.)
    IF v_client_version = ANY(v_supported_versions) THEN
        v_negotiated_version := v_client_version;
    ELSE
        v_negotiated_version := v_supported_versions[array_upper(v_supported_versions, 1)];
    END IF;

    RETURN api.mcp_success(
        jsonb_build_object(
            'protocolVersion', v_negotiated_version,
            'serverInfo', api.mcp_server_info(),
            'capabilities', api.mcp_server_capabilities()
        ),
        p_request_id
    );
END;
$$;

-- ============================================================================
-- MCP Ping Handler
-- ============================================================================
-- Responds to keepalive/liveness checks from MCP clients.
--
-- Parameters:
--   p_request_id - JSON-RPC request ID to echo in response
--
-- Returns:
--   {jsonrpc: "2.0", id: "...", result: {}}
--
-- Example Request:
--   {"jsonrpc": "2.0", "id": "2", "method": "ping"}
--
-- Example Response:
--   {"jsonrpc": "2.0", "id": "2", "result": {}}

DROP FUNCTION IF EXISTS api.mcp_ping(text);

CREATE OR REPLACE FUNCTION api.mcp_ping(p_request_id jsonb)
RETURNS api.mcp_response
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success('{}'::jsonb, p_request_id);
$$;

-- ============================================================================
-- MCP Unified Request Dispatcher
-- ============================================================================
-- Routes all MCP JSON-RPC requests to the appropriate handler functions.
--
-- This is the main entry point for MCP clients. It parses the JSON-RPC
-- envelope, validates the request, sets up authentication context, and
-- dispatches to the appropriate handler.
--
-- Parameters:
--   p_request - Complete JSON-RPC 2.0 request object
--   p_context - Optional authentication context {"user_id": "...", "tenant_id": "..."}
--
-- Returns:
--   JSON-RPC 2.0 response envelope (success or error)
--
-- Supported Methods:
--   +--------------------------+----------------------------------+
--   | Method                   | Handler                          |
--   +--------------------------+----------------------------------+
--   | initialize               | api.mcp_initialize()             |
--   | notifications/initialized| No-op (returns empty success)    |
--   | ping                     | api.mcp_ping()                   |
--   | tools/list               | api.mcp_list_tools()             |
--   | tools/call               | api.mcp_call_tool()              |
--   | resources/list           | api.mcp_list_resources()         |
--   | resources/templates/list | api.mcp_list_resource_templates()|
--   | resources/read           | api.mcp_read_resource()          |
--   | prompts/list             | api.mcp_list_prompts()           |
--   | prompts/get              | api.mcp_get_prompt()             |
--   +--------------------------+----------------------------------+
--
-- JSON-RPC Error Codes:
--   -32600 Invalid Request (missing jsonrpc, method, or null request)
--   -32601 Method not found
--   -32602 Invalid params (used by initialize for version mismatch)
--   -32603 Internal error (exception during handler execution)
--
-- Usage (direct SQL):
--   SELECT (api.mcp_handle_request(
--     '{"jsonrpc":"2.0","id":"1","method":"tools/list"}'::jsonb
--   )).envelope;
--
-- Usage (with authentication):
--   SELECT (api.mcp_handle_request(
--     '{"jsonrpc":"2.0","id":"1","method":"tools/call",
--       "params":{"name":"my_tool","arguments":{}}}'::jsonb,
--     '{"user_id":"auth0|123"}'::jsonb
--   )).envelope;
--
-- Usage (from HTTP gateway):
--   result = conn.execute(
--     "SELECT (api.mcp_handle_request($1, $2)).envelope",
--     [request_json, context_json]
--   )

-- Dispatcher returns NULL (envelope=NULL) for JSON-RPC notifications. The
-- caller MUST check envelope IS NULL before sending a response to the wire —
-- notifications MUST NOT receive a response per JSON-RPC 2.0 spec.

CREATE OR REPLACE FUNCTION api.mcp_handle_request(
    p_request jsonb,
    p_context jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
DECLARE
    v_jsonrpc text;
    v_method text;
    v_params jsonb;
    v_id jsonb;
    v_is_notification boolean;
BEGIN
    -- Validate request is not null
    IF p_request IS NULL THEN
        RETURN api.mcp_error(-32600, 'Invalid Request: null request', NULL);
    END IF;

    -- Extract JSON-RPC envelope fields. v_id is jsonb to preserve type (string,
    -- integer, null). A request without an id member is a notification.
    v_jsonrpc := p_request->>'jsonrpc';
    v_method := p_request->>'method';
    v_id := p_request->'id';
    v_is_notification := NOT (p_request ? 'id');
    v_params := COALESCE(p_request->'params', '{}'::jsonb);

    -- Validate JSON-RPC 2.0 version
    IF v_jsonrpc IS NULL OR v_jsonrpc != '2.0' THEN
        IF v_is_notification THEN
            RETURN ROW(NULL::jsonb)::api.mcp_response;
        END IF;
        RETURN api.mcp_error(-32600, 'Invalid Request: missing or invalid jsonrpc version', v_id);
    END IF;

    -- Validate method is present
    IF v_method IS NULL THEN
        IF v_is_notification THEN
            RETURN ROW(NULL::jsonb)::api.mcp_response;
        END IF;
        RETURN api.mcp_error(-32600, 'Invalid Request: missing method', v_id);
    END IF;

    -- JSON-RPC 2.0: notifications (no id) MUST NOT receive a response.
    -- Dispatch the method body for its side-effects but return NULL envelope.
    IF v_is_notification OR v_method LIKE 'notifications/%' THEN
        RETURN ROW(NULL::jsonb)::api.mcp_response;
    END IF;

    -- Reset and apply validated auth context unconditionally, before any method
    -- (including discovery: tools/list, resources/list, prompts/list). Calling
    -- with NULL clears identity, so a forged id (no provider|subject pipe) is
    -- rejected and a prior request's identity cannot bleed into a context-less
    -- request through the discovery filter.
    PERFORM internal.apply_mcp_auth_context(p_context);

    -- Route to appropriate handler based on method
    CASE v_method
        WHEN 'initialize' THEN
            RETURN api.mcp_initialize(v_params, v_id);

        WHEN 'ping' THEN
            RETURN api.mcp_ping(v_id);

        WHEN 'tools/list' THEN
            RETURN api.mcp_success(api.mcp_list_tools(), v_id);

        WHEN 'tools/call' THEN
            RETURN api.mcp_call_tool(
                v_params->>'name',
                COALESCE(v_params->'arguments', '{}'::jsonb),
                p_context,
                v_id
            );

        WHEN 'resources/list' THEN
            RETURN api.mcp_success(api.mcp_list_resources(), v_id);

        WHEN 'resources/templates/list' THEN
            RETURN api.mcp_success(api.mcp_list_resource_templates(), v_id);

        WHEN 'resources/read' THEN
            RETURN api.mcp_read_resource(
                v_params->>'uri',
                p_context,
                v_id
            );

        WHEN 'prompts/list' THEN
            RETURN api.mcp_success(api.mcp_list_prompts(), v_id);

        WHEN 'prompts/get' THEN
            RETURN api.mcp_get_prompt(
                v_params->>'name',
                COALESCE(v_params->'arguments', '{}'::jsonb),
                p_context,
                v_id
            );

        ELSE
            RETURN api.mcp_error(-32601, 'Method not found: ' || v_method, v_id);
    END CASE;

EXCEPTION WHEN OTHERS THEN
    -- Log detail for operators; return a sanitized message so table/constraint/
    -- column names and schema paths are not exposed to the client. Mirrors
    -- rest_invoke / rpc_invoke / the invocation handlers.
    RAISE WARNING 'mcp_handle_request: sqlstate=% detail=%', SQLSTATE, SQLERRM;
    RETURN api.mcp_error(-32603, 'Internal error', v_id);
END;
$$;

-- ============================================================================
-- Inline Tests (validate during deployment)
-- ============================================================================

DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
BEGIN
    -- Test: Initialize handshake (string id)
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'mcp_handle_request initialize: missing jsonrpc';
    END IF;
    IF v_envelope->'result'->'serverInfo' IS NULL THEN
        RAISE EXCEPTION 'mcp_handle_request initialize: missing serverInfo';
    END IF;

    -- Test: Integer id round-trips as integer (JSON-RPC 2.0 type preservation)
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":42,"method":"ping"}'::jsonb);
    v_envelope := (v_response).envelope;
    IF jsonb_typeof(v_envelope->'id') != 'number' OR (v_envelope->>'id')::int != 42 THEN
        RAISE EXCEPTION 'mcp_handle_request: integer id type not preserved, got %', v_envelope->'id';
    END IF;

    -- Test: Ping keepalive (string id)
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"2","method":"ping"}'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->>'id' != '2' THEN
        RAISE EXCEPTION 'mcp_handle_request ping: wrong id';
    END IF;

    -- Test: Unknown method returns -32601
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","id":"3","method":"unknown_method"}'::jsonb);
    v_envelope := (v_response).envelope;
    IF (v_envelope->'error'->>'code')::int != -32601 THEN
        RAISE EXCEPTION 'mcp_handle_request unknown method: wrong error code';
    END IF;

    -- Test: Missing jsonrpc returns -32600 (request has id, so it's a request not a notification)
    v_response := api.mcp_handle_request('{"id":"4","method":"ping"}'::jsonb);
    v_envelope := (v_response).envelope;
    IF (v_envelope->'error'->>'code')::int != -32600 THEN
        RAISE EXCEPTION 'mcp_handle_request missing jsonrpc: wrong error code';
    END IF;

    -- Test: notifications/initialized MUST NOT receive a response
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","method":"notifications/initialized"}'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope IS NOT NULL THEN
        RAISE EXCEPTION 'mcp_handle_request notifications/initialized: MUST NOT return a response envelope, got %', v_envelope;
    END IF;

    -- Test: any notification (no id) MUST NOT receive a response
    v_response := api.mcp_handle_request('{"jsonrpc":"2.0","method":"tools/list"}'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope IS NOT NULL THEN
        RAISE EXCEPTION 'mcp_handle_request notification (no id) MUST NOT return response envelope, got %', v_envelope;
    END IF;
END $$;

DO $$ BEGIN
    RAISE NOTICE '  + api.mcp_server_info() - server name and version';
    RAISE NOTICE '  + api.mcp_server_capabilities() - declared capabilities';
    RAISE NOTICE '  + api.mcp_initialize() - MCP handshake handler';
    RAISE NOTICE '  + api.mcp_ping() - keepalive response';
    RAISE NOTICE '  + api.mcp_handle_request() - unified JSON-RPC dispatcher';
END $$;

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_server_info() TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_server_capabilities() TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_initialize(jsonb, jsonb) TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_ping(jsonb) TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_handle_request(jsonb, jsonb) TO %I', v_api_role);

    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_server_info() TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_server_capabilities() TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_initialize(jsonb, jsonb) TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_ping(jsonb) TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.mcp_handle_request(jsonb, jsonb) TO %I', v_admin_role);
END $$;
