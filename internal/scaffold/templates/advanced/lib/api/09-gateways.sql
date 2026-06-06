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
-- TRUST BOUNDARY (security critical):
--
-- api.set_auth_context trusts x-user-id / x-user-email / x-tenant-id headers
-- WITHOUT cryptographic verification. It is the deployment's responsibility to
-- ensure only trusted traffic reaches api.rest_invoke / api.rpc_invoke — these
-- headers MUST be stripped from client requests and re-issued by a trusted
-- gateway that has authenticated the user (e.g., a reverse proxy validating
-- a JWT and emitting x-user-id, or PostgREST with role-based auth).
--
-- To help detect misuse, x-user-id must be in 'provider|subject' form. Raw
-- subject strings (no pipe) are rejected so that casual attempts to forge
-- x-user-id: alice fail closed.

CREATE OR REPLACE FUNCTION internal.set_auth_user_id(p_user_id text)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_max_len constant int := 4096;
BEGIN
    PERFORM set_config('auth.user_id', '', true);
    PERFORM set_config('auth.idp_subject', '', true);
    IF p_user_id IS NOT NULL
       AND length(p_user_id) <= v_max_len
       AND position('|' IN p_user_id) > 1                 -- non-empty provider prefix
       AND position('|' IN p_user_id) < length(p_user_id) -- non-empty subject suffix
    THEN
        PERFORM set_config('auth.user_id', p_user_id, true);
        PERFORM set_config('auth.idp_subject', p_user_id, true);
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION api.set_auth_context(p_headers extensions.hstore)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_max_len constant int := 4096;
BEGIN
    -- Reset every auth GUC before conditionally setting it: gateways run
    -- per-request in a possibly reused session, and set_config(is_local=>true)
    -- is transaction-scoped, so an unreset GUC would bleed request N's identity
    -- into request N+1 when N+1 omits the header.
    PERFORM internal.set_auth_user_id(p_headers->'x-user-id');
    PERFORM set_config('auth.user_email', '', true);
    PERFORM set_config('auth.tenant_id', '', true);
    PERFORM set_config('auth.token', '', true);

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

COMMENT ON FUNCTION api.set_auth_context(extensions.hstore) IS
    'Gateway-only trust boundary: maps x-user-id (format provider|subject_id), x-user-email, x-tenant-id, authorization headers into session GUCs. Callers MUST be a trusted gateway that has already verified the identity — these headers carry no integrity check.';

CREATE OR REPLACE FUNCTION internal.setup_auth_session(p_headers extensions.hstore)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, membership, extensions, pg_temp
AS $$
DECLARE
    v_subject text;
    v_email text;
    v_provider text;
    v_subject_id text;
BEGIN
    v_subject := p_headers->'x-user-id';
    IF v_subject IS NULL OR length(v_subject) > 4096 THEN
        RETURN;
    END IF;

    IF position('|' IN v_subject) = 0 THEN
        RETURN;
    END IF;

    PERFORM set_config('auth.idp_subject', v_subject, true);
    PERFORM set_config('auth.user_id', v_subject, true);

    v_email := p_headers->'x-user-email';
    IF v_email IS NOT NULL AND length(v_email) <= 4096 THEN
        v_provider := api.parse_idp_provider(v_subject);
        v_subject_id := api.parse_idp_subject_id(v_subject);
        PERFORM membership.upsert_user(v_provider, v_subject_id, v_email);
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
    v_path text;
    v_start_time timestamptz;
    v_execution_ms numeric;
BEGIN
    v_start_time := clock_timestamp();
    RAISE DEBUG 'rest_invoke: % %', p_method, p_url;

    IF length(p_method) > 16 THEN
        RETURN api.problem_response(400, 'Bad Request', 'HTTP method too long');
    END IF;
    IF length(p_url) > 8192 THEN
        RETURN api.problem_response(414, 'URI Too Long', 'URL exceeds maximum length');
    END IF;

    p_method := upper(trim(p_method));
    p_url := trim(p_url);
    p_headers := COALESCE(p_headers, ''::extensions.hstore);

    v_version := COALESCE(
        p_headers->'x-api-version',
        p_headers->'accept-version',
        ''
    );

    -- Match against the path only; query string is parsed separately by the
    -- handler via api.query_params(). Routes can use plain regex like
    -- '^/users/\d+$' without hand-anchoring '(\?.*)?$'.
    v_path := api.url_path(p_url);

    SELECT h.handler_exec_sql, h.object_id, h.response_headers, h.produces, h.requires_auth,
           h.output_json_schema,
           r.route_name, r.auto_log
    INTO v_route
    FROM api.rest_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id
    WHERE v_path ~ r.address_regexp
      AND p_method ~ r.method_regexp
      AND v_version ~ r.version_regexp
    ORDER BY r.sequence_number DESC
    LIMIT 1;

    IF v_route.handler_exec_sql IS NULL THEN
        RAISE DEBUG 'rest_invoke: No route matched';
        RETURN api.problem_response(404, 'Not Found', 'No route matches ' || p_method || ' ' || p_url);
    END IF;

    RAISE DEBUG 'rest_invoke: Matched route %', v_route.route_name;

    -- Resolve identity first, then gate on session state (not header presence)
    -- so a present-but-malformed x-user-id fails closed.
    PERFORM api.set_auth_context(p_headers);

    IF v_route.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RAISE DEBUG 'rest_invoke: Auth required but missing';
        RETURN api.problem_response(401, 'Unauthorized', 'Authentication required');
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

    v_request := (p_method, p_url, p_headers, p_content)::api.rest_request;

    BEGIN
        RAISE DEBUG 'rest_invoke: Invoking handler %', v_route.object_id;
        EXECUTE v_route.handler_exec_sql INTO v_response USING v_request;

        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;

        -- REST $schema injection: only when opt-in via x-include-schema=true AND
        -- the body parses as a JSON object. For array/scalar bodies, wrap in
        -- {"data": body, "$schema": schema} so the schema describes a nested
        -- value rather than corrupting the root shape. Non-JSON or malformed
        -- bodies are passed through unchanged (RAISE DEBUG, not silent swallow).
        IF v_route.output_json_schema IS NOT NULL
           AND COALESCE((
               SELECT lower(value) = 'true'
               FROM jsonb_each_text(v_route.response_headers)
               WHERE lower(key) = 'x-include-schema'
               LIMIT 1
           ), false) THEN
            DECLARE
                v_body jsonb;
                v_merged jsonb;
            BEGIN
                v_body := api.content_json((v_response).content);
                IF jsonb_typeof(v_body) = 'object' THEN
                    v_merged := v_body || jsonb_build_object('$schema', v_route.output_json_schema::jsonb);
                ELSE
                    v_merged := jsonb_build_object(
                        'data', v_body,
                        '$schema', v_route.output_json_schema::jsonb
                    );
                END IF;
                v_response := (
                    (v_response).status_code,
                    (v_response).headers,
                    convert_to(v_merged::text, 'UTF8')
                )::api.http_response;
            EXCEPTION WHEN OTHERS THEN
                RAISE DEBUG 'rest_invoke: $schema injection skipped (non-JSON body): %', SQLERRM;
            END;
        END IF;

        -- Merge registered response_headers (except the x-include-schema
        -- directive, which controls $schema injection above and MUST NOT
        -- appear on the wire). Keys are lowercased for case-insensitive
        -- semantics expected by HTTP clients.
        v_response.headers := COALESCE(v_response.headers, ''::extensions.hstore);
        IF v_route.response_headers IS NOT NULL AND v_route.response_headers <> '{}'::jsonb THEN
            v_response.headers := v_response.headers || COALESCE((
                SELECT extensions.hstore(
                    array_agg(lower(key)),
                    array_agg(value)
                )
                FROM jsonb_each_text(v_route.response_headers)
                WHERE lower(key) <> 'x-include-schema'
            ), ''::extensions.hstore);
        END IF;

        v_response.headers := v_response.headers || extensions.hstore(ARRAY[
            'content-length', COALESCE(octet_length((v_response).content), 0)::text,
            'x-execution-time-ms', v_execution_ms::text,
            'x-route-id', v_route.object_id::text
        ]);
        IF NOT v_response.headers ? 'content-type' THEN
            v_response.headers := v_response.headers || 'content-type=>application/json; charset=utf-8'::extensions.hstore;
        END IF;

        IF v_route.auto_log THEN
            INSERT INTO api.rest_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_route.object_id, v_request, v_response, now());
        END IF;

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        RAISE DEBUG 'rest_invoke: Handler exception: %', SQLERRM;
        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;

        -- Log SQLSTATE + truncated SQLERRM. Full SQLERRM may include
        -- attacker-supplied input or PII (handlers commonly raise with
        -- "Invalid email: <user_input>"); truncating limits the blast radius
        -- if exchange-table grants ever loosen.
        v_response := api.problem_response(500, 'Internal Server Error',
            'sqlstate=' || SQLSTATE || ' detail=' || LEFT(SQLERRM, 200));
        v_response.headers := extensions.hstore(ARRAY[
            'content-type', 'application/json; charset=utf-8',
            'content-length', COALESCE(octet_length((v_response).content), 0)::text,
            'x-execution-time-ms', v_execution_ms::text,
            'x-error-sqlstate', SQLSTATE
        ]);
        IF v_route.object_id IS NOT NULL THEN
            INSERT INTO api.rest_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_route.object_id, v_request, v_response, now());
        END IF;

        -- Return sanitized error to client (hide internal details)
        RETURN api.problem_response(500, 'Internal Server Error', 'An internal error occurred');
    END;
END;
$$;

CREATE OR REPLACE FUNCTION api.rest_invoke(
    p_method text,
    p_url text,
    p_headers extensions.hstore,
    p_content jsonb
) RETURNS api.http_response
LANGUAGE plpgsql AS $$
DECLARE
    v_headers extensions.hstore;
    v_content_bytes bytea;
BEGIN
    v_headers := COALESCE(p_headers, ''::extensions.hstore);

    IF p_content IS NOT NULL THEN
        IF NOT v_headers ? 'content-type' THEN
            v_headers := v_headers || 'content-type=>application/json'::extensions.hstore;
        END IF;
        v_content_bytes := convert_to(p_content::text, 'UTF8');
    END IF;

    RETURN api.rest_invoke(p_method, p_url, v_headers, v_content_bytes);
END;
$$;

CREATE OR REPLACE FUNCTION api.rest_invoke(
    p_method text,
    p_url text,
    p_headers extensions.hstore,
    p_content xml
) RETURNS api.http_response
LANGUAGE plpgsql AS $$
DECLARE
    v_headers extensions.hstore;
    v_content_bytes bytea;
BEGIN
    v_headers := COALESCE(p_headers, ''::extensions.hstore);

    IF p_content IS NOT NULL THEN
        IF NOT v_headers ? 'content-type' THEN
            v_headers := v_headers || 'content-type=>application/xml'::extensions.hstore;
        END IF;
        v_content_bytes := convert_to(p_content::text, 'UTF8');
    END IF;

    RETURN api.rest_invoke(p_method, p_url, v_headers, v_content_bytes);
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
    RAISE DEBUG 'rpc_invoke: route_id=%', p_route_id;

    BEGIN
        v_json_id := api.content_json(p_content)->'id';
    EXCEPTION WHEN OTHERS THEN
        v_json_id := NULL;
    END;

    SELECT h.handler_exec_sql, h.object_id, h.requires_auth,
           h.response_headers, h.output_json_schema,
           r.method_name, r.auto_log
    INTO v_handler
    FROM api.handler h
    JOIN api.rpc_route r ON r.handler_object_id = h.object_id
    WHERE h.object_id = p_route_id AND h.handler_type = 'rpc';

    IF v_handler.handler_exec_sql IS NULL THEN
        RAISE DEBUG 'rpc_invoke: Method not found';
        RETURN api.jsonrpc_error(-32601, 'Method not found', v_json_id);
    END IF;

    RAISE DEBUG 'rpc_invoke: Matched method %', v_handler.method_name;

    -- Resolve identity first, then gate on session state (not header presence)
    -- so a present-but-malformed x-user-id fails closed.
    PERFORM api.set_auth_context(p_headers);

    IF v_handler.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RAISE DEBUG 'rpc_invoke: Auth required but missing';
        RETURN api.jsonrpc_error(-32001, 'Authentication required', v_json_id);
    END IF;

    v_request := (p_route_id, p_headers, p_content)::api.rpc_request;

    BEGIN
        RAISE DEBUG 'rpc_invoke: Invoking handler %', v_handler.object_id;
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;

        -- RPC $schema injection: merge into result member only (never at top
        -- level of the JSON-RPC envelope). JSON-RPC 2.0 responses MUST NOT have
        -- extra top-level keys. Injecting into result is spec-compliant because
        -- result is "Any" type. Skip for error responses (result absent) or
        -- when result is not a JSON object.
        IF v_handler.output_json_schema IS NOT NULL
           AND COALESCE((
               SELECT lower(value) = 'true'
               FROM jsonb_each_text(v_handler.response_headers)
               WHERE lower(key) = 'x-include-schema'
               LIMIT 1
           ), false) THEN
            DECLARE
                v_body jsonb;
                v_merged jsonb;
            BEGIN
                v_body := api.content_json((v_response).content);
                IF jsonb_typeof(v_body) = 'object'
                   AND jsonb_typeof(v_body->'result') = 'object' THEN
                    v_merged := jsonb_set(
                        v_body,
                        '{result,$schema}',
                        v_handler.output_json_schema::jsonb,
                        true
                    );
                    v_response := (
                        (v_response).status_code,
                        (v_response).headers,
                        convert_to(v_merged::text, 'UTF8')
                    )::api.http_response;
                END IF;
            EXCEPTION WHEN OTHERS THEN
                RAISE DEBUG 'rpc_invoke: $schema injection skipped (malformed envelope): %', SQLERRM;
            END;
        END IF;

        -- Merge registered response_headers (except x-include-schema directive).
        -- Keys lowercased for HTTP case-insensitive semantics.
        v_response.headers := COALESCE(v_response.headers, ''::extensions.hstore);
        IF v_handler.response_headers IS NOT NULL AND v_handler.response_headers <> '{}'::jsonb THEN
            v_response.headers := v_response.headers || COALESCE((
                SELECT extensions.hstore(
                    array_agg(lower(key)),
                    array_agg(value)
                )
                FROM jsonb_each_text(v_handler.response_headers)
                WHERE lower(key) <> 'x-include-schema'
            ), ''::extensions.hstore);
        END IF;

        v_response.headers := v_response.headers || extensions.hstore(ARRAY[
            'content-length', COALESCE(octet_length((v_response).content), 0)::text,
            'x-execution-time-ms', v_execution_ms::text,
            'x-rpc-method', v_handler.method_name
        ]);
        IF NOT v_response.headers ? 'content-type' THEN
            v_response.headers := v_response.headers || 'content-type=>application/json; charset=utf-8'::extensions.hstore;
        END IF;

        IF v_handler.auto_log THEN
            INSERT INTO api.rpc_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_handler.object_id, v_request, v_response, now());
        END IF;

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        RAISE DEBUG 'rpc_invoke: Handler exception: %', SQLERRM;
        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;

        -- Log SQLSTATE + truncated SQLERRM (see rest_invoke for rationale).
        v_response := api.jsonrpc_error(-32603,
            'sqlstate=' || SQLSTATE || ' detail=' || LEFT(SQLERRM, 200), v_json_id);
        v_response.headers := extensions.hstore(ARRAY[
            'content-type', 'application/json; charset=utf-8',
            'content-length', COALESCE(octet_length((v_response).content), 0)::text,
            'x-execution-time-ms', v_execution_ms::text,
            'x-error-sqlstate', SQLSTATE
        ]);
        IF v_handler.object_id IS NOT NULL THEN
            INSERT INTO api.rpc_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_handler.object_id, v_request, v_response, now());
        END IF;

        -- Return sanitized error to client (hide internal details)
        RETURN api.jsonrpc_error(-32603, 'Internal error', v_json_id);
    END;
END;
$$;

-- ============================================================================
-- MCP Tool Invocation
-- ============================================================================
-- Exception handling follows MCP spec: tool execution failures return
-- result.isError=true (via api.mcp_tool_error), NOT a JSON-RPC error envelope.
-- JSON-RPC -32601 is reserved for "tool not found" (true protocol error).

DROP FUNCTION IF EXISTS api.mcp_call_tool(text, jsonb, jsonb, text);

CREATE OR REPLACE FUNCTION api.mcp_call_tool(
    p_name text,
    p_arguments jsonb,
    p_context jsonb DEFAULT NULL,
    p_request_id jsonb DEFAULT NULL
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
    RAISE DEBUG 'mcp_call_tool: %', p_name;

    SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name
    INTO v_handler
    FROM api.handler h
    JOIN api.mcp_route r ON r.handler_object_id = h.object_id
    WHERE r.mcp_name = p_name AND r.mcp_type = 'tool';

    IF v_handler.handler_exec_sql IS NULL THEN
        RAISE DEBUG 'mcp_call_tool: Tool not found';
        RETURN api.mcp_error(-32601, 'Tool not found: ' || p_name, p_request_id);
    END IF;

    RAISE DEBUG 'mcp_call_tool: Matched tool %', v_handler.mcp_name;

    -- Reset every auth GUC, then set from context, so a malformed id (no
    -- provider|subject pipe) is rejected and a prior request's identity cannot
    -- leak into an MCP call that omits context. Mirrors api.set_auth_context.
    PERFORM internal.set_auth_user_id(p_context->>'user_id');
    PERFORM set_config('auth.user_email', '', true);
    PERFORM set_config('auth.token', '', true);
    PERFORM set_config('auth.tenant_id', '', true);
    IF p_context->>'tenant_id' IS NOT NULL THEN
        PERFORM set_config('auth.tenant_id', p_context->>'tenant_id', true);
    END IF;

    IF v_handler.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RAISE DEBUG 'mcp_call_tool: Auth required but missing';
        RETURN api.mcp_error(-32001, 'Authentication required: user_id missing from context', p_request_id);
    END IF;

    v_request := (p_arguments, NULL, p_context, p_request_id)::api.mcp_request;

    BEGIN
        RAISE DEBUG 'mcp_call_tool: Invoking handler %', v_handler.object_id;
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
        VALUES (v_handler.object_id, 'tool', v_handler.mcp_name, v_request, v_response);

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        RAISE DEBUG 'mcp_call_tool: Handler exception: %', SQLERRM;

        -- C1 fix: MCP spec requires tool *execution* failures to use
        -- result.isError=true (via mcp_tool_error), NOT a JSON-RPC error
        -- envelope (which is reserved for protocol-level errors).
        -- C3 fix: persist SQLSTATE + truncated SQLERRM (limits exposure if
        -- exchange-table grants are loosened).
        v_response := api.mcp_tool_error(
            'sqlstate=' || SQLSTATE || ' detail=' || LEFT(SQLERRM, 200),
            p_request_id);
        IF v_handler.object_id IS NOT NULL THEN
            INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
            VALUES (v_handler.object_id, 'tool', v_handler.mcp_name, v_request, v_response);
        END IF;

        -- Return sanitized isError result to client (MCP spec-compliant)
        RETURN api.mcp_tool_error('Tool execution failed', p_request_id);
    END;
END;
$$;

-- ============================================================================
-- MCP Resource Read
-- ============================================================================

DROP FUNCTION IF EXISTS api.mcp_read_resource(text, jsonb, text);

CREATE OR REPLACE FUNCTION api.mcp_read_resource(
    p_uri text,
    p_context jsonb DEFAULT NULL,
    p_request_id jsonb DEFAULT NULL
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
    RAISE DEBUG 'mcp_read_resource: %', p_uri;

    SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name
    INTO v_handler
    FROM api.handler h
    JOIN api.mcp_route r ON r.handler_object_id = h.object_id
    WHERE r.mcp_type = 'resource'
      AND p_uri ~ api.uri_template_to_regex(r.uri_template);

    IF v_handler.handler_exec_sql IS NULL THEN
        RAISE DEBUG 'mcp_read_resource: Resource not found';
        RETURN api.mcp_error(-32601, 'Resource not found: ' || p_uri, p_request_id);
    END IF;

    RAISE DEBUG 'mcp_read_resource: Matched resource %', v_handler.mcp_name;

    -- Reset every auth GUC, then set from context, so a malformed id (no
    -- provider|subject pipe) is rejected and a prior request's identity cannot
    -- leak into an MCP call that omits context. Mirrors api.set_auth_context.
    PERFORM internal.set_auth_user_id(p_context->>'user_id');
    PERFORM set_config('auth.user_email', '', true);
    PERFORM set_config('auth.token', '', true);
    PERFORM set_config('auth.tenant_id', '', true);
    IF p_context->>'tenant_id' IS NOT NULL THEN
        PERFORM set_config('auth.tenant_id', p_context->>'tenant_id', true);
    END IF;

    IF v_handler.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RAISE DEBUG 'mcp_read_resource: Auth required but missing';
        RETURN api.mcp_error(-32001, 'Authentication required: user_id missing from context', p_request_id);
    END IF;

    v_request := (NULL, p_uri, p_context, p_request_id)::api.mcp_request;

    BEGIN
        RAISE DEBUG 'mcp_read_resource: Invoking handler %', v_handler.object_id;
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
        VALUES (v_handler.object_id, 'resource', v_handler.mcp_name, v_request, v_response);

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        RAISE DEBUG 'mcp_read_resource: Handler exception: %', SQLERRM;

        -- Log SQLSTATE + truncated SQLERRM (see rest_invoke for rationale).
        v_response := api.mcp_error(-32603,
            'sqlstate=' || SQLSTATE || ' detail=' || LEFT(SQLERRM, 200), p_request_id);
        IF v_handler.object_id IS NOT NULL THEN
            INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
            VALUES (v_handler.object_id, 'resource', v_handler.mcp_name, v_request, v_response);
        END IF;

        -- Return sanitized error to client (hide internal details)
        RETURN api.mcp_error(-32603, 'Internal error', p_request_id);
    END;
END;
$$;

-- ============================================================================
-- MCP Prompt Expansion
-- ============================================================================

DROP FUNCTION IF EXISTS api.mcp_get_prompt(text, jsonb, jsonb, text);

CREATE OR REPLACE FUNCTION api.mcp_get_prompt(
    p_name text,
    p_arguments jsonb,
    p_context jsonb DEFAULT NULL,
    p_request_id jsonb DEFAULT NULL
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
    RAISE DEBUG 'mcp_get_prompt: %', p_name;

    SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name
    INTO v_handler
    FROM api.handler h
    JOIN api.mcp_route r ON r.handler_object_id = h.object_id
    WHERE r.mcp_name = p_name AND r.mcp_type = 'prompt';

    IF v_handler.handler_exec_sql IS NULL THEN
        RAISE DEBUG 'mcp_get_prompt: Prompt not found';
        RETURN api.mcp_error(-32601, 'Prompt not found: ' || p_name, p_request_id);
    END IF;

    RAISE DEBUG 'mcp_get_prompt: Matched prompt %', v_handler.mcp_name;

    -- Reset every auth GUC, then set from context, so a malformed id (no
    -- provider|subject pipe) is rejected and a prior request's identity cannot
    -- leak into an MCP call that omits context. Mirrors api.set_auth_context.
    PERFORM internal.set_auth_user_id(p_context->>'user_id');
    PERFORM set_config('auth.user_email', '', true);
    PERFORM set_config('auth.token', '', true);
    PERFORM set_config('auth.tenant_id', '', true);
    IF p_context->>'tenant_id' IS NOT NULL THEN
        PERFORM set_config('auth.tenant_id', p_context->>'tenant_id', true);
    END IF;

    IF v_handler.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RAISE DEBUG 'mcp_get_prompt: Auth required but missing';
        RETURN api.mcp_error(-32001, 'Authentication required: user_id missing from context', p_request_id);
    END IF;

    v_request := (p_arguments, NULL, p_context, p_request_id)::api.mcp_request;

    BEGIN
        RAISE DEBUG 'mcp_get_prompt: Invoking handler %', v_handler.object_id;
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
        VALUES (v_handler.object_id, 'prompt', v_handler.mcp_name, v_request, v_response);

        RETURN v_response;

    EXCEPTION WHEN OTHERS THEN
        RAISE DEBUG 'mcp_get_prompt: Handler exception: %', SQLERRM;

        -- Log SQLSTATE + truncated SQLERRM (see rest_invoke for rationale).
        v_response := api.mcp_error(-32603,
            'sqlstate=' || SQLSTATE || ' detail=' || LEFT(SQLERRM, 200), p_request_id);
        IF v_handler.object_id IS NOT NULL THEN
            INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
            VALUES (v_handler.object_id, 'prompt', v_handler.mcp_name, v_request, v_response);
        END IF;

        -- Return sanitized error to client (hide internal details)
        RETURN api.mcp_error(-32603, 'Internal error', p_request_id);
    END;
END;
$$;

-- ============================================================================
-- MCP Discovery Functions
-- ============================================================================
-- NO PAGINATION YET: these functions return the entire list in a single call.
-- MCP clients MUST NOT rely on cursor behaviour. Pagination (RFC-style
-- nextCursor) is planned post-v1 — keyset on mcp_name. Until then, servers
-- with >~500 tools will send large payloads.
--
-- NO listChanged NOTIFICATIONS: api.mcp_server_capabilities declares no
-- listChanged capability because we do not yet emit notifications/tools/
-- list_changed (or resources/, prompts/). Clients see a static tool list for
-- the duration of a connection. Integration path: LISTEN/NOTIFY on a channel
-- triggered by api.create_or_replace_mcp_handler, fanned out by the MCP
-- transport gateway.
--
-- TAGS placement: pgmi surfaces tags inside the spec-defined `_meta` object
-- (an extension slot reserved by MCP for server-specific data). Top-level
-- `tags` would be a spec violation under strict clients.
--
-- AUTH FILTERING: tools that require authentication are hidden from
-- mcp_list_tools when auth.user_id is not set in the session. MCP spec
-- allows either (a) hide-then-reject or (b) expose-and-return-isError;
-- pgmi uses (a) because hidden tools are the idiomatic MCP UX.

DROP FUNCTION IF EXISTS api.mcp_list_tools();
DROP FUNCTION IF EXISTS api.mcp_list_tools(text[]);

CREATE OR REPLACE FUNCTION api.mcp_list_tools(p_tags text[] DEFAULT NULL)
RETURNS jsonb
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, pg_temp
AS $$
    WITH norm AS (
        -- NULL or empty array both mean "no tag filter"
        SELECT CASE WHEN p_tags IS NULL OR cardinality(p_tags) = 0
                    THEN NULL::text[]
                    ELSE p_tags
               END AS tag_filter
    )
    SELECT jsonb_build_object('tools', COALESCE(jsonb_agg(
        jsonb_strip_nulls(
            jsonb_build_object(
                'name', r.mcp_name,
                'title', h.title,
                'description', h.description,
                'inputSchema', r.input_schema,
                'outputSchema', h.output_json_schema::jsonb,
                '_meta', CASE WHEN r.tags = '{}' THEN NULL
                              ELSE jsonb_build_object('tags', to_jsonb(r.tags)) END
            )
        )
    ), '[]'::jsonb))
    FROM api.mcp_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id
    CROSS JOIN norm
    WHERE r.mcp_type = 'tool'
      AND (norm.tag_filter IS NULL OR r.tags && norm.tag_filter)
      -- Hide auth-required tools from callers with no identity. Callers with
      -- auth.user_id set see every tool (including the ones that require auth).
      AND (NOT h.requires_auth
           OR NULLIF(current_setting('auth.user_id', true), '') IS NOT NULL);
$$;

COMMENT ON FUNCTION api.mcp_list_tools(text[]) IS
    'MCP tool discovery. Returns {"tools":[...]} with name, title, description, inputSchema, outputSchema, and _meta.tags (pgmi extension). Hides tools requiring auth when the session has no auth.user_id. Pass p_tags to filter by tag overlap (NULL or empty = no filter).';

CREATE OR REPLACE FUNCTION api.mcp_list_resources()
RETURNS jsonb
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, pg_temp
AS $$
    -- A resource with no RFC 6570 {placeholder} is a static URI per MCP spec;
    -- emit as `uri`. Otherwise emit as `uriTemplate` (RFC 6570 Level 1 subset).
    SELECT jsonb_build_object('resources', COALESCE(jsonb_agg(
        jsonb_strip_nulls(
            jsonb_build_object(
                'name', r.mcp_name,
                'title', h.title,
                'description', h.description,
                'uri', CASE WHEN r.uri_template ~ '\{[^}]+\}' THEN NULL ELSE r.uri_template END,
                'uriTemplate', CASE WHEN r.uri_template ~ '\{[^}]+\}' THEN r.uri_template ELSE NULL END,
                'mimeType', r.mime_type
            )
        )
    ), '[]'::jsonb))
    FROM api.mcp_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id
    WHERE r.mcp_type = 'resource'
      AND (NOT h.requires_auth
           OR NULLIF(current_setting('auth.user_id', true), '') IS NOT NULL);
$$;

COMMENT ON FUNCTION api.mcp_list_resources() IS
    'MCP resource discovery. Emits uri (static) or uriTemplate (RFC 6570 placeholders detected) based on uri_template shape. Hides auth-required resources from unauthenticated sessions.';

CREATE OR REPLACE FUNCTION api.mcp_list_prompts()
RETURNS jsonb
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, pg_temp
AS $$
    SELECT jsonb_build_object('prompts', COALESCE(jsonb_agg(
        jsonb_strip_nulls(
            jsonb_build_object(
                'name', r.mcp_name,
                'title', h.title,
                'description', h.description,
                'arguments', r.arguments
            )
        )
    ), '[]'::jsonb))
    FROM api.mcp_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id
    WHERE r.mcp_type = 'prompt'
      AND (NOT h.requires_auth
           OR NULLIF(current_setting('auth.user_id', true), '') IS NOT NULL);
$$;

COMMENT ON FUNCTION api.mcp_list_prompts() IS
    'MCP prompt discovery. Strips NULL fields for spec compliance (clients reject "title": null). Hides auth-required prompts from unauthenticated sessions.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.set_auth_context() - authentication header extraction';
    RAISE NOTICE '  ✓ api.rest_invoke(bytea) - REST gateway with URL routing';
    RAISE NOTICE '  ✓ api.rest_invoke(jsonb) - REST overload for JSON content';
    RAISE NOTICE '  ✓ api.rest_invoke(xml) - REST overload for XML content';
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
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION internal.setup_auth_session(extensions.hstore) TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION internal.set_auth_user_id(text) TO %I', v_api_role);
END $$;
