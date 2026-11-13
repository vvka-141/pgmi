/*
<pgmi-meta
    id="a7f01000-0010-4000-8000-000000000001"
    idempotent="true">
  <description>
    MCP protocol layer: both MCP eras on one endpoint, and the unified request dispatcher.

    The era is chosen per request, with no connection state. A request whose
    params._meta names io.modelcontextprotocol/protocolVersion, and every
    server/discover, is served as MCP 2026-07-28: stateless, no handshake.
    Any other request is legacy: initialize negotiates up to 2025-11-25 and
    ping answers. The legacy era stays while mainstream clients still send initialize.

    Key Components:
    - api.mcp_supported_versions()        - the versions a request may name in _meta
    - api.mcp_legacy_versions()           - the versions legacy initialize negotiates
    - api.mcp_request_policy()            - before dispatch: the refusal, the era, the transaction
    - internal.mcp_server_instructions()  - the server instructions hook (NULL by default)
    - api.mcp_server_capabilities()       - tools, resources, prompts (no listChanged)
    - api.mcp_discover()                  - the server/discover result
    - api.mcp_initialize(), api.mcp_ping() - the legacy handshake and keepalive
    - api.mcp_handle_request()            - the unified dispatcher

    pgmi tools answer in one round trip: the request is the transaction
    boundary, so a result is always resultType "complete", never input_required.

    Usage from psql:
      SELECT (api.mcp_handle_request('{"jsonrpc":"2.0","id":1,"method":"server/discover"}'::jsonb)).envelope;
  </description>
  <sortKeys>
    <key>004/010</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION api.mcp_supported_versions()
RETURNS text[]
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ARRAY['2026-07-28'];
$$;

COMMENT ON FUNCTION api.mcp_supported_versions() IS
    'The protocol versions a request may name in params._meta (the stateless era). Another version answers -32022 with data {supported, requested}. Legacy initialize negotiates api.mcp_legacy_versions().';

-- Ascending: the last element is the newest.
CREATE OR REPLACE FUNCTION api.mcp_legacy_versions()
RETURNS text[]
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ARRAY['2024-11-05', '2025-06-18', '2025-11-25'];
$$;

COMMENT ON FUNCTION api.mcp_legacy_versions() IS
    'The legacy-era protocol versions, ascending: initialize negotiates them, and an MCP-Protocol-Version header may name them without the 2026-07-28 header checks.';

-- What every agent should know before its first call. Replace this function
-- in a file sorted after lib/ to publish instructions.
CREATE OR REPLACE FUNCTION internal.mcp_server_instructions()
RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT NULL::text;
$$;

COMMENT ON FUNCTION internal.mcp_server_instructions() IS
    'Hook: the MCP server instructions published by server/discover and legacy initialize. NULL (the default) publishes none. Replace it in a file sorted after lib/.';

-- listChanged is not declared: there is no subscriptions/listen stream, and a
-- client lists again once a list's ttlMs has passed (api.mcp_cache_policy()).
CREATE OR REPLACE FUNCTION api.mcp_server_capabilities()
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT '{"tools": {}, "resources": {}, "prompts": {}}'::jsonb;
$$;

COMMENT ON FUNCTION api.mcp_server_capabilities() IS
    'MCP capabilities: tools, resources and prompts, without listChanged (no subscriptions/listen stream; lists are re-read after their ttlMs).';

-- The same for every caller, so any cache may keep it (cacheScope public).
CREATE OR REPLACE FUNCTION api.mcp_discover()
RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object(
               'supportedVersions', to_jsonb(api.mcp_supported_versions()),
               'capabilities', api.mcp_server_capabilities(),
               'ttlMs', api.mcp_cache_policy()->'ttlMs',
               'cacheScope', 'public')
        || CASE WHEN i.text IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('instructions', i.text) END
    FROM (SELECT internal.mcp_server_instructions()) AS i(text);
$$;

COMMENT ON FUNCTION api.mcp_discover() IS
    'The server/discover result before api.mcp_success adds resultType and serverInfo: supportedVersions, capabilities, instructions (when the hook returns any), ttlMs and cacheScope public.';

-- ============================================================================
-- Legacy era: initialize and ping
-- ============================================================================
-- An unknown version is not an error: the server answers with its newest
-- legacy version and the client decides whether to go on (MCP lifecycle).

CREATE OR REPLACE FUNCTION api.mcp_initialize(
    p_params jsonb,
    p_request_id jsonb
) RETURNS api.mcp_response
LANGUAGE plpgsql STABLE AS $$
DECLARE
    v_supported text[] := api.mcp_legacy_versions();
    v_client text := p_params->>'protocolVersion';
    v_instructions text := internal.mcp_server_instructions();
BEGIN
    IF v_client IS NULL THEN
        RETURN api.mcp_error(-32602, 'Missing required parameter: protocolVersion', p_request_id);
    END IF;

    RETURN api.mcp_success(
        jsonb_build_object(
            'protocolVersion', CASE WHEN v_client = ANY (v_supported) THEN v_client
                                    ELSE v_supported[cardinality(v_supported)] END,
            'serverInfo', api.mcp_server_info(),
            'capabilities', api.mcp_server_capabilities())
        || CASE WHEN v_instructions IS NULL THEN '{}'::jsonb
                ELSE jsonb_build_object('instructions', v_instructions) END,
        p_request_id
    );
END;
$$;

COMMENT ON FUNCTION api.mcp_initialize(jsonb, jsonb) IS
    'Legacy-era MCP handshake: negotiates 2024-11-05 through 2025-11-25 (an unknown version gets 2025-11-25), returns serverInfo, capabilities and the instructions hook.';

CREATE OR REPLACE FUNCTION api.mcp_ping(p_request_id jsonb)
RETURNS api.mcp_response
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success('{}'::jsonb, p_request_id);
$$;

COMMENT ON FUNCTION api.mcp_ping(jsonb) IS
    'Legacy-era MCP keepalive. Returns an empty result with the echoed request id.';

-- ============================================================================
-- Refusals before dispatch
-- ============================================================================
-- Whether a request's params._meta names a protocol version: that is what
-- makes it a 2026-07-28 era request.
CREATE OR REPLACE FUNCTION internal.mcp_names_protocol_version(p_meta jsonb)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE(jsonb_typeof(p_meta) = 'object' AND p_meta ? 'io.modelcontextprotocol/protocolVersion', false);
$$;

-- A 2026-07-28 request must name a version in api.mcp_supported_versions()
-- (else -32022, data {supported, requested}) and declare
-- io.modelcontextprotocol/clientCapabilities (else -32602); server/discover
-- may omit _meta entirely (a probe). NULL for anything else, a legacy request,
-- a notification or a malformed envelope included: the dispatcher answers those.
CREATE OR REPLACE FUNCTION internal.mcp_meta_refusal(p_request jsonb)
RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT CASE
               WHEN NOT internal.mcp_names_protocol_version(r.meta)
                    OR p_request->>'jsonrpc' IS DISTINCT FROM '2.0'
                    OR COALESCE(jsonb_typeof(r.id) NOT IN ('string', 'number'), true)
                    OR r.method IS NULL OR r.method LIKE 'notifications/%'
                   THEN NULL
               WHEN jsonb_typeof(r.meta->'io.modelcontextprotocol/protocolVersion') IS DISTINCT FROM 'string'
                    OR NOT (r.meta->>'io.modelcontextprotocol/protocolVersion') = ANY (api.mcp_supported_versions())
                   THEN (api.mcp_error(-32022, 'Unsupported protocol version', r.id,
                            jsonb_build_object('supported', to_jsonb(api.mcp_supported_versions()),
                                               'requested', r.meta->'io.modelcontextprotocol/protocolVersion'))).envelope
               WHEN r.method <> 'server/discover' AND NOT r.meta ? 'io.modelcontextprotocol/clientCapabilities'
                   THEN (api.mcp_error(-32602,
                            'Invalid params: missing required _meta field: io.modelcontextprotocol/clientCapabilities',
                            r.id)).envelope
           END
    FROM (SELECT CASE WHEN jsonb_typeof(p_request) = 'object' THEN p_request->'id' END AS id,
                 CASE WHEN jsonb_typeof(p_request) = 'object' THEN p_request->>'method' END AS method,
                 CASE WHEN jsonb_typeof(p_request->'params'->'_meta') = 'object'
                      THEN p_request->'params'->'_meta' ELSE '{}'::jsonb END AS meta) r;
$$;

COMMENT ON FUNCTION internal.mcp_meta_refusal(jsonb) IS
    'The -32022 or -32602 envelope for a 2026-07-28 request whose params._meta names an unsupported version or omits clientCapabilities (server/discover excepted); NULL otherwise.';

-- Mcp-Name may carry =?base64?<value>?= (Streamable HTTP, value encoding).
CREATE OR REPLACE FUNCTION internal.mcp_header_value(p_value text)
RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    v_encoded text := substring(p_value FROM '^=\?base64\?([A-Za-z0-9+/]*={0,2})\?=$');
BEGIN
    IF v_encoded IS NULL THEN
        RETURN p_value;
    END IF;
    RETURN convert_from(decode(v_encoded, 'base64'), 'UTF8');
EXCEPTION WHEN invalid_parameter_value OR character_not_in_repertoire THEN
    RETURN NULL;
END;
$$;

COMMENT ON FUNCTION internal.mcp_header_value(text) IS
    'An MCP header value as compared with the body: the =?base64?...?= form decoded as UTF-8, NULL when that form does not decode; any other value as sent.';

-- The Streamable HTTP checks of one POST, from the three headers and the body.
-- From 2026-07-28 on the headers repeat the body so a proxy can route on them;
-- the two must agree (-32020, data.header names the one that does not). A
-- body whose _meta names a version needs that version in the header, so a
-- missing or legacy header on it is the same refusal. A header naming a
-- version the server does not speak is -32022. Then the _meta checks of
-- internal.mcp_meta_refusal.
CREATE OR REPLACE FUNCTION internal.mcp_request_refusal(
    p_request jsonb,
    p_protocol_version text,
    p_mcp_method text,
    p_mcp_name text
) RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
DECLARE
    v_body jsonb := CASE WHEN jsonb_typeof(p_request) = 'object' THEN p_request ELSE '{}'::jsonb END;
    v_id jsonb := CASE WHEN jsonb_typeof(v_body->'id') IN ('string', 'number') THEN v_body->'id' END;
    v_params jsonb := CASE WHEN jsonb_typeof(v_body->'params') = 'object' THEN v_body->'params' ELSE '{}'::jsonb END;
    v_meta_version text := CASE WHEN jsonb_typeof(v_params->'_meta') = 'object'
                                THEN v_params->'_meta'->>'io.modelcontextprotocol/protocolVersion' END;
    v_method text := CASE WHEN jsonb_typeof(v_body->'method') = 'string' THEN v_body->>'method' END;
    v_target text := CASE v_method WHEN 'tools/call' THEN 'name' WHEN 'prompts/get' THEN 'name'
                                   WHEN 'resources/read' THEN 'uri' END;
    v_modern boolean := p_protocol_version = ANY (api.mcp_supported_versions());
    v_header text;
BEGIN
    -- A version the server does not speak is -32022 with what it does speak:
    -- the 2026-07-28 era for a body that names its version in _meta, every
    -- version otherwise. A legacy header on such a body is the same answer.
    IF p_protocol_version IS NOT NULL AND NOT v_modern
       AND (NOT p_protocol_version = ANY (api.mcp_legacy_versions()) OR v_meta_version = p_protocol_version) THEN
        RETURN (api.mcp_error(-32022, 'Unsupported protocol version', v_id,
            jsonb_build_object('supported', to_jsonb(CASE WHEN v_meta_version IS NOT NULL THEN api.mcp_supported_versions()
                                                          ELSE api.mcp_legacy_versions() || api.mcp_supported_versions() END),
                               'requested', p_protocol_version))).envelope;
    END IF;

    v_header := CASE
        WHEN NOT COALESCE(v_modern, false) THEN
            CASE WHEN v_meta_version IS NOT NULL THEN 'MCP-Protocol-Version' END
        WHEN v_method IS NULL OR internal.mcp_header_value(p_mcp_method) IS DISTINCT FROM v_method THEN
            'Mcp-Method'
        WHEN v_target IS NOT NULL
             AND (jsonb_typeof(v_params->v_target) IS DISTINCT FROM 'string'
                  OR internal.mcp_header_value(p_mcp_name) IS DISTINCT FROM v_params->>v_target) THEN
            'Mcp-Name'
        WHEN (v_body ? 'id' OR v_meta_version IS NOT NULL) AND v_meta_version IS DISTINCT FROM p_protocol_version THEN
            'MCP-Protocol-Version'
    END;
    IF v_header IS NOT NULL THEN
        RETURN (api.mcp_error(-32020, format('Header mismatch: %s is missing or does not match the body', v_header),
            v_id, jsonb_build_object('header', v_header))).envelope;
    END IF;

    RETURN internal.mcp_meta_refusal(p_request);
END;
$$;

COMMENT ON FUNCTION internal.mcp_request_refusal(jsonb, text, text, text) IS
    'The refusal envelope of one MCP POST from its MCP-Protocol-Version, Mcp-Method and Mcp-Name headers (NULL when absent) and its body: -32022 for a header version the server does not speak (listing the 2026-07-28 era when the body names its version in _meta, every version otherwise), -32020 with data.header for a header that disagrees with the body, then internal.mcp_meta_refusal. NULL when the request may be dispatched.';

-- ============================================================================
-- MCP Unified Request Dispatcher
-- ============================================================================
--   +--------------------------+-------------------+-------------------+
--   | Method                   | 2026-07-28 era    | legacy era        |
--   +--------------------------+-------------------+-------------------+
--   | server/discover          | api.mcp_discover  | (always 2026)     |
--   | initialize               | -32601            | api.mcp_initialize|
--   | ping                     | -32601            | api.mcp_ping      |
--   | tools/list, tools/call   | yes               | yes               |
--   | resources/list, /read,   | yes               | yes               |
--   |   /templates/list        |                   |                   |
--   | prompts/list, /get       | yes               | yes               |
--   | notifications/*          | no response       | no response       |
--   | anything else            | -32601            | -32601            |
--   +--------------------------+-------------------+-------------------+
--
-- A 2026-07-28 request is refused first by internal.mcp_meta_refusal.
--
-- A NULL envelope means a notification: the caller sends nothing back.

CREATE OR REPLACE FUNCTION api.mcp_handle_request(
    p_request jsonb,
    p_context jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
DECLARE
    v_method text;
    v_params jsonb;
    v_id jsonb;
    v_is_notification boolean;
    v_modern boolean;
    v_refusal jsonb;
    v_response api.mcp_response;
BEGIN
    IF p_request IS NULL THEN
        RETURN api.mcp_error(-32600, 'Invalid Request: null request', NULL);
    END IF;

    IF jsonb_typeof(p_request) <> 'object' THEN
        RETURN api.mcp_error(-32600, 'Invalid Request: expected a JSON-RPC 2.0 request object', NULL);
    END IF;

    -- A request without an id member is a notification.
    v_method := p_request->>'method';
    v_id := p_request->'id';
    v_is_notification := NOT (p_request ? 'id');
    v_params := COALESCE(p_request->'params', '{}'::jsonb);

    IF p_request->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        IF v_is_notification THEN
            RETURN ROW(NULL::jsonb)::api.mcp_response;
        END IF;
        RETURN api.mcp_error(-32600, 'Invalid Request: missing or invalid jsonrpc version', v_id);
    END IF;

    -- MCP: the id is a string or an integer and never null; any JSON number
    -- passes here. When the id could not be read, JSON-RPC 2.0 requires a null
    -- response id.
    IF NOT v_is_notification AND jsonb_typeof(v_id) NOT IN ('string', 'number') THEN
        RETURN api.mcp_error(-32600, 'Invalid Request: id must be a string or number', NULL);
    END IF;

    IF v_method IS NULL THEN
        IF v_is_notification THEN
            RETURN ROW(NULL::jsonb)::api.mcp_response;
        END IF;
        RETURN api.mcp_error(-32600, 'Invalid Request: missing method', v_id);
    END IF;

    -- No notification has a side effect here, so none is dispatched.
    IF v_is_notification OR v_method LIKE 'notifications/%' THEN
        RETURN ROW(NULL::jsonb)::api.mcp_response;
    END IF;

    v_modern := v_method = 'server/discover' OR internal.mcp_names_protocol_version(v_params->'_meta');

    v_refusal := internal.mcp_meta_refusal(p_request);
    IF v_refusal IS NOT NULL THEN
        RETURN ROW(v_refusal)::api.mcp_response;
    END IF;

    -- Resolve the context before any method, discovery included. A NULL
    -- context resolves to nobody, so a prior request's identity cannot bleed
    -- into a context-less request through the discovery filter.
    v_refusal := internal.resolve_mcp_caller(p_context);
    IF v_refusal IS NOT NULL THEN
        RETURN internal.mcp_caller_refusal(v_refusal, v_id);
    END IF;

    IF v_method IN ('tools/call', 'resources/read', 'prompts/get') AND jsonb_typeof(v_params) IS DISTINCT FROM 'object' THEN
        RETURN api.mcp_error(-32602, format('Invalid params: %s requires a params object', v_method), v_id);
    END IF;

    CASE
        WHEN v_method = 'server/discover' THEN
            RETURN api.mcp_success(api.mcp_discover(), v_id);

        WHEN v_method = 'initialize' AND NOT v_modern THEN
            RETURN api.mcp_initialize(v_params, v_id);

        WHEN v_method = 'ping' AND NOT v_modern THEN
            RETURN api.mcp_ping(v_id);

        WHEN v_method = 'tools/list' THEN
            RETURN api.mcp_success(api.mcp_list_tools() || api.mcp_cache_policy(), v_id);

        WHEN v_method = 'tools/call' THEN
            RETURN api.mcp_call_tool(
                v_params->>'name',
                COALESCE(v_params->'arguments', '{}'::jsonb),
                p_context,
                v_id
            );

        WHEN v_method = 'resources/list' THEN
            RETURN api.mcp_success(api.mcp_list_resources() || api.mcp_cache_policy(), v_id);

        WHEN v_method = 'resources/templates/list' THEN
            RETURN api.mcp_success(api.mcp_list_resource_templates() || api.mcp_cache_policy(), v_id);

        WHEN v_method = 'resources/read' THEN
            -- A missing resource is -32602 from 2026-07-28 on; the eras
            -- negotiated through initialize (up to 2025-11-25) call it -32002.
            v_response := api.mcp_read_resource(v_params->>'uri', p_context, v_id);
            IF NOT v_modern AND (v_response).envelope->'error'->'code' = '-32602'::jsonb
               AND (v_response).envelope->'error'->'data' ? 'uri' THEN
                v_response := ROW(jsonb_set((v_response).envelope, '{error,code}', '-32002'))::api.mcp_response;
            END IF;
            RETURN v_response;

        WHEN v_method = 'prompts/list' THEN
            RETURN api.mcp_success(api.mcp_list_prompts() || api.mcp_cache_policy(), v_id);

        WHEN v_method = 'prompts/get' THEN
            RETURN api.mcp_get_prompt(
                v_params->>'name',
                COALESCE(v_params->'arguments', '{}'::jsonb),
                p_context,
                v_id
            );

        ELSE
            RETURN api.mcp_error(-32601, 'Method not found: ' || v_method, v_id);
    END CASE;

EXCEPTION
-- Propagate the retryable class untouched. Swallowing it here rolls the
-- statement back to this block's implicit savepoint and lets the transaction
-- commit, so the handler's write vanishes while the client is told 'internal
-- error' and never learns it should retry.
WHEN serialization_failure OR deadlock_detected THEN
    RAISE;

WHEN OTHERS THEN
    RAISE WARNING 'mcp_handle_request: sqlstate=% detail=%', SQLSTATE, SQLERRM;
    RETURN api.mcp_error(-32603, 'Internal error', v_id);
END;
$$;

COMMENT ON FUNCTION api.mcp_handle_request(jsonb, jsonb) IS
    'MCP unified dispatcher. Serves a request whose params._meta names a protocol version (and server/discover) as MCP 2026-07-28, anything else as the legacy initialize era. Returns a NULL envelope for notifications. SECURITY DEFINER.';

-- ============================================================================
-- Before dispatch: the refusal, the era and the transaction
-- ============================================================================
-- One call per MCP POST, outside the dispatch transaction. The gateway passes
-- the body and the MCP-Protocol-Version, Mcp-Method and Mcp-Name headers as
-- sent (NULL when absent). A non-NULL refusal is the whole answer: the gateway
-- sends it and never dispatches, so a refused request logs no exchange.
-- modern says which column of the status table in gateways/PROTOCOL.md applies.
-- The transaction columns resolve the tool, prompt or resource the way
-- internal.mcp_dispatch will; any other method, and an unknown name, gets the
-- no-policy row, so there is always exactly one row. The request arrives as
-- text: a body every JSON parser accepts but jsonb refuses (a \u0000, a lone
-- surrogate) is refused here with -32700, not a cast error and a 500.
CREATE OR REPLACE FUNCTION internal.mcp_request_policy(
    p_request jsonb,
    p_requested_isolation text DEFAULT NULL,
    p_protocol_version text DEFAULT NULL,
    p_mcp_method text DEFAULT NULL,
    p_mcp_name text DEFAULT NULL
) RETURNS TABLE(
    transaction_isolation text,
    transaction_read_only boolean,
    transaction_deferrable boolean,
    modern boolean,
    refusal jsonb,
    handler_name text,
    input_annotations jsonb
)
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
    WITH target AS (
        SELECT CASE p_request->>'method'
                   WHEN 'tools/call'     THEN 'tool'
                   WHEN 'prompts/get'    THEN 'prompt'
                   WHEN 'resources/read' THEN 'resource'
               END AS mcp_type,
               CASE p_request->>'method'
                   WHEN 'resources/read' THEN p_request->'params'->>'uri'
                   ELSE p_request->'params'->>'name'
               END AS key
    ),
    route AS (
        SELECT h.min_transaction_isolation, h.read_only, h.handler_function_name,
               internal.input_annotations(h.input_json_schema) AS input_annotations
        FROM target t
        JOIN api.handler h ON h.object_id = internal.mcp_route_lookup(t.mcp_type, t.key)
    ),
    policy AS (
        SELECT * FROM route
        UNION ALL
        SELECT NULL, false, NULL, NULL WHERE NOT EXISTS (SELECT 1 FROM route)
    )
    SELECT p.*,
           COALESCE(p_protocol_version <> ALL (api.mcp_legacy_versions()), false)
               OR internal.mcp_names_protocol_version(p_request->'params'->'_meta'),
           internal.mcp_request_refusal(p_request, p_protocol_version, p_mcp_method, p_mcp_name),
           policy.handler_function_name,
           policy.input_annotations
    FROM policy
    CROSS JOIN LATERAL internal.resolve_transaction_policy(
        policy.min_transaction_isolation, policy.read_only, p_requested_isolation) p;
$$;

CREATE OR REPLACE FUNCTION api.mcp_request_policy(
    p_request text,
    p_requested_isolation text DEFAULT NULL,
    p_protocol_version text DEFAULT NULL,
    p_mcp_method text DEFAULT NULL,
    p_mcp_name text DEFAULT NULL
) RETURNS TABLE(
    transaction_isolation text,
    transaction_read_only boolean,
    transaction_deferrable boolean,
    modern boolean,
    refusal jsonb,
    handler_name text,
    input_annotations jsonb
)
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
DECLARE
    v_request jsonb;
BEGIN
    BEGIN
        v_request := p_request::jsonb;
    EXCEPTION WHEN data_exception THEN
        RETURN QUERY SELECT p.*, false, (api.mcp_error(-32700, 'Parse error', NULL)).envelope, NULL::text, NULL::jsonb
                     FROM internal.resolve_transaction_policy(NULL, false, p_requested_isolation) p;
        RETURN;
    END;
    RETURN QUERY SELECT * FROM internal.mcp_request_policy(
        v_request, p_requested_isolation, p_protocol_version, p_mcp_method, p_mcp_name);
END;
$$;

COMMENT ON FUNCTION api.mcp_request_policy(text, text, text, text, text) IS
    'Called by the gateway before api.mcp_handle_request with the raw request as text (one jsonb refuses is a -32700 refusal), the requested isolation and the MCP-Protocol-Version, Mcp-Method and Mcp-Name headers. Returns one row: the transaction characteristics to open with, modern (the 2026-07-28 era: a non-legacy header or a _meta version), refusal, the envelope to answer instead of dispatching (internal.mcp_request_refusal) or NULL; and the handler name and inputSchema annotations of the tool, prompt or resource, for the gateway''s interceptors.';

DO $$
DECLARE
    v_meta jsonb := '{"_meta": {"io.modelcontextprotocol/protocolVersion": "2026-07-28",
                                "io.modelcontextprotocol/clientCapabilities": {}}}';
    v_envelope jsonb;
BEGIN
    v_envelope := (api.mcp_handle_request('{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}'::jsonb)).envelope;
    IF v_envelope->'result'->>'protocolVersion' IS DISTINCT FROM '2024-11-05'
       OR v_envelope->'result'->'serverInfo' IS NULL THEN
        RAISE EXCEPTION 'mcp_handle_request legacy initialize: %', v_envelope;
    END IF;

    v_envelope := (api.mcp_handle_request('{"jsonrpc":"2.0","id":42,"method":"ping"}'::jsonb)).envelope;
    IF v_envelope->'id' IS DISTINCT FROM '42'::jsonb OR v_envelope->'result' IS NULL THEN
        RAISE EXCEPTION 'mcp_handle_request legacy ping: integer id or result lost: %', v_envelope;
    END IF;

    v_envelope := (api.mcp_handle_request(jsonb_build_object('jsonrpc', '2.0', 'id', 3, 'method', 'server/discover', 'params', v_meta))).envelope;
    IF v_envelope->'result'->'supportedVersions' IS DISTINCT FROM '["2026-07-28"]'::jsonb THEN
        RAISE EXCEPTION 'mcp_handle_request server/discover: %', v_envelope;
    END IF;

    v_envelope := (api.mcp_handle_request(jsonb_build_object('jsonrpc', '2.0', 'id', 4, 'method', 'ping', 'params', v_meta))).envelope;
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32601 THEN
        RAISE EXCEPTION 'mcp_handle_request: ping on 2026-07-28 must answer -32601, got %', v_envelope;
    END IF;

    v_envelope := (api.mcp_handle_request('{"id":"5","method":"ping"}'::jsonb)).envelope;
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32600 THEN
        RAISE EXCEPTION 'mcp_handle_request missing jsonrpc: wrong error code';
    END IF;

    IF (api.mcp_handle_request('{"jsonrpc":"2.0","method":"tools/list"}'::jsonb)).envelope IS NOT NULL THEN
        RAISE EXCEPTION 'mcp_handle_request: a notification must not receive a response';
    END IF;
END $$;
