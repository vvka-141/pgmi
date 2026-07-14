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

CREATE OR REPLACE FUNCTION internal.apply_mcp_auth_context(p_context jsonb)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM internal.set_auth_user_id(p_context->>'user_id');
    PERFORM set_config('auth.user_email', '', true);
    PERFORM set_config('auth.token', '', true);
    PERFORM set_config('auth.tenant_id', '', true);
    IF p_context->>'user_email' IS NOT NULL THEN
        PERFORM set_config('auth.user_email', p_context->>'user_email', true);
    END IF;
    IF p_context->>'tenant_id' IS NOT NULL THEN
        PERFORM set_config('auth.tenant_id', p_context->>'tenant_id', true);
    END IF;
    -- JIT-provision so current_user_id() resolves; no-op when the context omits
    -- a valid identity or email. Idempotent across pooled requests.
    PERFORM internal.provision_current_user(p_context->>'user_email');
END;
$$;

COMMENT ON FUNCTION internal.apply_mcp_auth_context(jsonb) IS
    'MCP auth-context trust boundary: unconditionally resets auth GUCs, then applies a validated user_id (provider|subject) and optional tenant_id from p_context. Called with p_context NULL to clear identity. Shared by the MCP dispatcher and invocation handlers.';

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

-- JIT-provisions the membership.user row for the currently-authenticated
-- identity. Reads the already-validated auth.idp_subject GUC (set by
-- set_auth_context / apply_mcp_auth_context via set_auth_user_id, which
-- guarantees provider|subject form) so provisioning shares the gateway's
-- validation. Idempotent (membership.upsert_user upserts), safe to call on
-- every request in a pooled session. SECURITY DEFINER so it can reach
-- upsert_user, which is revoked from the api/customer roles.
CREATE OR REPLACE FUNCTION internal.provision_current_user(p_email text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, membership, extensions, pg_temp
AS $$
DECLARE
    v_subject text := NULLIF(current_setting('auth.idp_subject', true), '');
BEGIN
    IF v_subject IS NULL OR p_email IS NULL OR length(p_email) > 4096 THEN
        RETURN;
    END IF;
    PERFORM membership.upsert_user(
        api.parse_idp_provider(v_subject),
        api.parse_idp_subject_id(v_subject),
        p_email
    );
END;
$$;

-- Gateway auth path: set the validated trust-boundary GUCs, then JIT-provision
-- the membership.user row so api.current_user_id() resolves on first request.
CREATE OR REPLACE FUNCTION internal.setup_auth_session(p_headers extensions.hstore)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, membership, extensions, pg_temp
AS $$
BEGIN
    PERFORM api.set_auth_context(p_headers);
    PERFORM internal.provision_current_user(p_headers->'x-user-email');
END;
$$;

-- ============================================================================
-- REST Gateway
-- ============================================================================

-- Response-header finalization shared by the REST and RPC gateways: merges
-- handler-registered headers (keys lowercased for HTTP case-insensitive
-- semantics; the x-include-schema directive controls $schema injection and
-- MUST NOT appear on the wire), then stamps content-length, timing, and the
-- protocol-specific extras, defaulting content-type to JSON when the handler
-- set none. Later concatenations win, so stamps override registered headers.
CREATE OR REPLACE FUNCTION internal.finalize_response_headers(
    p_response api.http_response,
    p_registered jsonb,
    p_execution_ms numeric,
    p_extra extensions.hstore
) RETURNS extensions.hstore
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    WITH merged AS (
        SELECT COALESCE((p_response).headers, ''::extensions.hstore)
            || COALESCE((
                SELECT extensions.hstore(array_agg(lower(key)), array_agg(value))
                FROM jsonb_each_text(p_registered)
                WHERE lower(key) <> 'x-include-schema'
            ), ''::extensions.hstore)
            || extensions.hstore(ARRAY[
                'content-length', COALESCE(octet_length((p_response).content), 0)::text,
                'x-execution-time-ms', p_execution_ms::text
            ])
            || COALESCE(p_extra, ''::extensions.hstore) AS h
    )
    -- hstore(k, v) constructor, not a '=>' literal: the value's embedded
    -- space is a syntax error under hstore's unquoted-literal parsing.
    SELECT CASE WHEN h ? 'content-type' THEN h
                ELSE h || extensions.hstore('content-type', 'application/json; charset=utf-8')
           END
    FROM merged;
$$;

COMMENT ON FUNCTION internal.finalize_response_headers(api.http_response, jsonb, numeric, extensions.hstore) IS
    'Merges handler-registered response headers (lowercased, x-include-schema stripped) and stamps content-length, x-execution-time-ms, protocol extras, and a JSON content-type default. Shared by rest_invoke and rpc_invoke.';

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
    v_content_type text;
    v_iso_shortfall text;
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

    SELECT h.handler_exec_sql, h.object_id, h.response_headers, h.accepts, h.produces, h.requires_auth,
           h.output_json_schema, h.required_transaction_isolation,
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
    PERFORM internal.setup_auth_session(p_headers);

    IF v_route.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RAISE DEBUG 'rest_invoke: Auth required but missing';
        RETURN api.problem_response(401, 'Unauthorized', 'Authentication required');
    END IF;

    -- Enforce the route's transaction isolation floor. The gateway can only READ
    -- the level; the caller must open the transaction at the required level
    -- before the first statement (see lib/api/00-transaction-isolation.sql).
    v_iso_shortfall := internal.transaction_isolation_shortfall(v_route.required_transaction_isolation);
    IF v_iso_shortfall IS NOT NULL THEN
        RAISE DEBUG 'rest_invoke: isolation too weak (need %, have %)',
            v_route.required_transaction_isolation, v_iso_shortfall;
        RETURN api.problem_response(
            428, 'Precondition Required',
            format('Route requires %s isolation but current transaction uses %s.',
                   v_route.required_transaction_isolation, v_iso_shortfall),
            code => 'pgmi.transaction_isolation_too_weak'
        );
    END IF;

    -- Enforce the handler's declared accepts against the request Content-Type.
    -- Only when the request carries a Content-Type (a body); the default
    -- accepts of {*/*} matches everything, so this only bites handlers that
    -- explicitly narrow the types they accept.
    v_content_type := btrim(split_part(COALESCE(p_headers->'content-type', ''), ';', 1));
    IF v_content_type <> ''
       AND NOT api.accept_matches(array_to_string(v_route.accepts, ', '), ARRAY[v_content_type]) THEN
        RETURN api.problem_response(
            415,
            'Unsupported Media Type',
            format('Supported request content types: %s', array_to_string(v_route.accepts, ', '))
        );
    END IF;

    IF NOT api.accept_matches(p_headers->'accept', v_route.produces) THEN
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

        v_response.headers := internal.finalize_response_headers(
            v_response, v_route.response_headers, v_execution_ms,
            extensions.hstore('x-route-id', v_route.object_id::text));

        IF v_route.auto_log THEN
            INSERT INTO api.rest_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_route.object_id, v_request, v_response, now());
        END IF;

        RETURN v_response;

    EXCEPTION
    -- 40001 / 40P01 are transient: the caller's remedy is to abort and retry the
    -- whole transaction from a fresh snapshot. Two reasons they must NOT be
    -- caught here:
    --   1. Flattened into a 500, the client cannot tell "your transaction lost a
    --      race" from "this handler is broken", so it cannot know to retry.
    --   2. Catching them is unsafe. The failed statement is rolled back to this
    --      block's implicit savepoint, but the transaction stays alive and COMMITS
    --      — the handler's write silently vanishes while the client is told
    --      "internal error". Verified live: a caught 40001 commits, losing the write.
    -- A savepoint cannot refresh the snapshot, so no in-SQL retry can converge
    -- under repeatable read / serializable. Retry belongs to whoever owns BEGIN.
    WHEN serialization_failure OR deadlock_detected THEN
        RAISE;

    WHEN OTHERS THEN
    DECLARE
        v_sqlstate text := SQLSTATE;
        v_status int;
        v_title text;
        v_client_detail text;
    BEGIN
        RAISE DEBUG 'rest_invoke: Handler exception: %', SQLERRM;
        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;

        -- Map common constraint violations to 4xx instead of a blanket 500 so
        -- clients, caches, and retry logic see the right class. Messages stay
        -- generic per class — SQLERRM/DETAIL are never sent to the client.
        CASE v_sqlstate
            WHEN '23505' THEN v_status := 409; v_title := 'Conflict';             v_client_detail := 'Resource already exists';
            WHEN '23514' THEN v_status := 422; v_title := 'Unprocessable Entity'; v_client_detail := 'A submitted value violates a constraint';
            WHEN '23502' THEN v_status := 400; v_title := 'Bad Request';          v_client_detail := 'A required value is missing';
            WHEN '23503' THEN v_status := 400; v_title := 'Bad Request';          v_client_detail := 'References a resource that does not exist';
            ELSE              v_status := 500; v_title := 'Internal Server Error'; v_client_detail := 'An internal error occurred';
        END CASE;

        -- Logged copy keeps SQLSTATE + truncated SQLERRM. Full SQLERRM may
        -- include attacker-supplied input or PII (handlers commonly raise
        -- "Invalid email: <user_input>"); truncating limits the blast radius
        -- if exchange-table grants ever loosen.
        v_response := api.problem_response(v_status, v_title,
            'sqlstate=' || v_sqlstate || ' detail=' || LEFT(SQLERRM, 200));
        v_response.headers := extensions.hstore(ARRAY[
            'content-type', 'application/json; charset=utf-8',
            'content-length', COALESCE(octet_length((v_response).content), 0)::text,
            'x-execution-time-ms', v_execution_ms::text,
            'x-error-sqlstate', v_sqlstate
        ]);
        INSERT INTO api.rest_exchange (handler_object_id, request, response, completed_at)
        VALUES (v_route.object_id, v_request, v_response, now());

        -- Return sanitized error to client (hide internal details)
        RETURN api.problem_response(v_status, v_title, v_client_detail);
    END;
    END;
END;
$$;

COMMENT ON FUNCTION api.rest_invoke(text, text, extensions.hstore, bytea) IS
    'REST gateway. Routes method+url to a registered handler, enforces auth and content negotiation, logs exchanges. SECURITY DEFINER.';

CREATE OR REPLACE FUNCTION api.rest_invoke(
    p_method text,
    p_url text,
    p_headers extensions.hstore,
    p_content jsonb
) RETURNS api.http_response
LANGUAGE sql AS $$
    SELECT api.rest_invoke(
        p_method,
        p_url,
        CASE
            WHEN p_content IS NOT NULL
                 AND NOT COALESCE(p_headers, ''::extensions.hstore) ? 'content-type'
            THEN COALESCE(p_headers, ''::extensions.hstore)
                 || 'content-type=>application/json'::extensions.hstore
            ELSE COALESCE(p_headers, ''::extensions.hstore)
        END,
        CASE WHEN p_content IS NOT NULL
             THEN convert_to(p_content::text, 'UTF8')
        END
    );
$$;

CREATE OR REPLACE FUNCTION api.rest_invoke(
    p_method text,
    p_url text,
    p_headers extensions.hstore,
    p_content xml
) RETURNS api.http_response
LANGUAGE sql AS $$
    SELECT api.rest_invoke(
        p_method,
        p_url,
        CASE
            WHEN p_content IS NOT NULL
                 AND NOT COALESCE(p_headers, ''::extensions.hstore) ? 'content-type'
            THEN COALESCE(p_headers, ''::extensions.hstore)
                 || 'content-type=>application/xml'::extensions.hstore
            ELSE COALESCE(p_headers, ''::extensions.hstore)
        END,
        CASE WHEN p_content IS NOT NULL
             THEN convert_to(p_content::text, 'UTF8')
        END
    );
$$;

COMMENT ON FUNCTION api.rest_invoke(text, text, extensions.hstore, jsonb) IS
    'REST gateway overload: auto-sets content-type to application/json when a jsonb body is provided.';

COMMENT ON FUNCTION api.rest_invoke(text, text, extensions.hstore, xml) IS
    'REST gateway overload: auto-sets content-type to application/xml when an xml body is provided.';

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

COMMENT ON FUNCTION api.rpc_resolve(text) IS
    'Resolves an RPC method name to its handler UUID. Returns NULL if not registered.';

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
    v_iso_shortfall text;
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
           h.response_headers, h.output_json_schema, h.required_transaction_isolation,
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
    PERFORM internal.setup_auth_session(p_headers);

    IF v_handler.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RAISE DEBUG 'rpc_invoke: Auth required but missing';
        RETURN api.jsonrpc_error(-32001, 'Authentication required', v_json_id);
    END IF;

    -- Enforce the route's transaction isolation floor (see rest_invoke). The
    -- precise HTTP status (428) rides on the response while the JSON-RPC error
    -- stays in its correct class; the machine token is carried in error.data.code.
    v_iso_shortfall := internal.transaction_isolation_shortfall(v_handler.required_transaction_isolation);
    IF v_iso_shortfall IS NOT NULL THEN
        RAISE DEBUG 'rpc_invoke: isolation too weak (need %, have %)',
            v_handler.required_transaction_isolation, v_iso_shortfall;
        RETURN api.json_response(428, jsonb_build_object(
            'jsonrpc', '2.0',
            'error', jsonb_build_object(
                'code', -32600,
                'message', format('Route requires %s isolation but current transaction uses %s.',
                                  v_handler.required_transaction_isolation, v_iso_shortfall),
                'data', jsonb_build_object('code', 'pgmi.transaction_isolation_too_weak')
            ),
            'id', v_json_id
        ));
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

        v_response.headers := internal.finalize_response_headers(
            v_response, v_handler.response_headers, v_execution_ms,
            extensions.hstore('x-rpc-method', v_handler.method_name));

        IF v_handler.auto_log THEN
            INSERT INTO api.rpc_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_handler.object_id, v_request, v_response, now());
        END IF;

        RETURN v_response;

    EXCEPTION
    -- Propagate the retryable class untouched — see rest_invoke for why catching
    -- it both hides the retry signal and can commit a lost write.
    WHEN serialization_failure OR deadlock_detected THEN
        RAISE;

    WHEN OTHERS THEN
    DECLARE
        v_sqlstate text := SQLSTATE;
        v_status int;
        v_rpc_code int;
        v_client_msg text;
    BEGIN
        RAISE DEBUG 'rpc_invoke: Handler exception: %', SQLERRM;
        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;

        -- Map common constraint violations to a 4xx HTTP status. JSON-RPC has
        -- no code for "conflict", so the precise status rides on the HTTP
        -- response while the error code stays in its correct class: -32602
        -- (Invalid params) for client-caused errors, -32603 for server errors.
        CASE v_sqlstate
            WHEN '23505' THEN v_status := 409; v_rpc_code := -32602; v_client_msg := 'Resource already exists';
            WHEN '23514' THEN v_status := 422; v_rpc_code := -32602; v_client_msg := 'A submitted value violates a constraint';
            WHEN '23502' THEN v_status := 400; v_rpc_code := -32602; v_client_msg := 'A required value is missing';
            WHEN '23503' THEN v_status := 400; v_rpc_code := -32602; v_client_msg := 'References a resource that does not exist';
            ELSE              v_status := 500; v_rpc_code := -32603; v_client_msg := 'Internal error';
        END CASE;

        -- Logged copy keeps SQLSTATE + truncated SQLERRM (see rest_invoke for
        -- the truncation rationale).
        v_response := api.json_response(v_status, jsonb_build_object(
            'jsonrpc', '2.0',
            'error', jsonb_build_object('code', v_rpc_code,
                'message', 'sqlstate=' || v_sqlstate || ' detail=' || LEFT(SQLERRM, 200)),
            'id', v_json_id
        ));
        v_response.headers := extensions.hstore(ARRAY[
            'content-type', 'application/json; charset=utf-8',
            'content-length', COALESCE(octet_length((v_response).content), 0)::text,
            'x-execution-time-ms', v_execution_ms::text,
            'x-error-sqlstate', v_sqlstate
        ]);
        INSERT INTO api.rpc_exchange (handler_object_id, request, response, completed_at)
        VALUES (v_handler.object_id, v_request, v_response, now());

        -- Return sanitized error to client (hide internal details)
        RETURN api.json_response(v_status, jsonb_build_object(
            'jsonrpc', '2.0',
            'error', jsonb_build_object('code', v_rpc_code, 'message', v_client_msg),
            'id', v_json_id
        ));
    END;
    END;
END;
$$;

COMMENT ON FUNCTION api.rpc_invoke(uuid, extensions.hstore, bytea) IS
    'RPC gateway. Invokes a handler by UUID, enforces auth, maps constraint violations to JSON-RPC error codes. SECURITY DEFINER.';

-- ============================================================================
-- MCP Gateway (tools/call, resources/read, prompts/get)
-- ============================================================================
-- Exception handling follows MCP spec: tool execution failures return
-- result.isError=true (via api.mcp_tool_error), NOT a JSON-RPC error envelope.
--
-- An unknown tool/resource/prompt NAME returns -32602 (Invalid params): the
-- method (tools/call, resources/read, prompts/get) was found and dispatched
-- correctly — only the name/uri argument identifies nothing. The spec
-- standardizes not-found to -32602 (SEP-2164,
-- https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2164).
-- -32601 (Method not found) stays reserved for genuinely-unknown JSON-RPC
-- methods (the dispatcher ELSE branch). Auth failures keep -32001.

-- Shared invocation path for the three MCP entry points. Route lookup and
-- request shape differ per type; everything security-relevant — auth context,
-- auth gate, isolation floor, exchange logging, error sanitization — lives
-- here exactly once. Runs SECURITY INVOKER inside the SECURITY DEFINER
-- wrappers below, inheriting their search_path and owner privileges.
CREATE OR REPLACE FUNCTION internal.mcp_dispatch(
    p_mcp_type text,
    p_name_or_uri text,
    p_arguments jsonb,
    p_context jsonb,
    p_request_id jsonb
) RETURNS api.mcp_response
LANGUAGE plpgsql AS $$
DECLARE
    v_request api.mcp_request;
    v_response api.mcp_response;
    v_handler record;
    v_iso_shortfall text;
BEGIN
    RAISE DEBUG 'mcp_dispatch: % %', p_mcp_type, p_name_or_uri;

    IF p_mcp_type = 'resource' THEN
        -- Deterministic precedence when more than one template matches: most
        -- specific (longest template) first, mcp_name as a stable tiebreak.
        -- Without this, the chosen handler (and its requires_auth) would be
        -- nondeterministic.
        SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name, h.required_transaction_isolation
        INTO v_handler
        FROM api.handler h
        JOIN api.mcp_route r ON r.handler_object_id = h.object_id
        WHERE r.mcp_type = 'resource'
          AND p_name_or_uri ~ r.uri_regexp
        ORDER BY length(r.uri_template) DESC, r.mcp_name
        LIMIT 1;
    ELSE
        SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name, h.required_transaction_isolation
        INTO v_handler
        FROM api.handler h
        JOIN api.mcp_route r ON r.handler_object_id = h.object_id
        WHERE r.mcp_name = p_name_or_uri AND r.mcp_type = p_mcp_type;
    END IF;

    IF v_handler.handler_exec_sql IS NULL THEN
        RAISE DEBUG 'mcp_dispatch: % not found', p_mcp_type;
        RETURN api.mcp_error(-32602, initcap(p_mcp_type) || ' not found: ' || p_name_or_uri, p_request_id);
    END IF;

    RAISE DEBUG 'mcp_dispatch: Matched %', v_handler.mcp_name;

    PERFORM internal.apply_mcp_auth_context(p_context);

    IF v_handler.requires_auth AND NULLIF(current_setting('auth.user_id', true), '') IS NULL THEN
        RAISE DEBUG 'mcp_dispatch: Auth required but missing';
        RETURN api.mcp_error(-32001, 'Authentication required: user_id missing from context', p_request_id);
    END IF;

    -- Enforce the route's transaction isolation floor (see rest_invoke).
    v_iso_shortfall := internal.transaction_isolation_shortfall(v_handler.required_transaction_isolation);
    IF v_iso_shortfall IS NOT NULL THEN
        RETURN api.mcp_error(
            -32600,
            format('Route requires %s isolation but current transaction uses %s.',
                   v_handler.required_transaction_isolation, v_iso_shortfall),
            p_request_id,
            jsonb_build_object('code', 'pgmi.transaction_isolation_too_weak')
        );
    END IF;

    v_request := CASE WHEN p_mcp_type = 'resource'
        THEN (NULL, p_name_or_uri, p_context, p_request_id)::api.mcp_request
        ELSE (p_arguments, NULL, p_context, p_request_id)::api.mcp_request
    END;

    BEGIN
        RAISE DEBUG 'mcp_dispatch: Invoking handler %', v_handler.object_id;
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
        VALUES (v_handler.object_id, p_mcp_type, v_handler.mcp_name, v_request, v_response);

        RETURN v_response;

    EXCEPTION
    -- Propagate the retryable class untouched — see rest_invoke for why catching
    -- it both hides the retry signal and can commit a lost write. An isError=true
    -- tool result would look like a handler bug to the client, not a transient
    -- conflict it should retry.
    WHEN serialization_failure OR deadlock_detected THEN
        RAISE;

    WHEN OTHERS THEN
        RAISE DEBUG 'mcp_dispatch: Handler exception: %', SQLERRM;

        -- MCP spec: tool *execution* failures use result.isError=true, NOT a
        -- JSON-RPC error envelope (reserved for protocol-level errors);
        -- resource/prompt failures stay protocol errors (-32603). Logged copy
        -- keeps SQLSTATE + truncated SQLERRM (see rest_invoke for the
        -- truncation rationale); the client gets a sanitized message.
        v_response := CASE WHEN p_mcp_type = 'tool'
            THEN api.mcp_tool_error('sqlstate=' || SQLSTATE || ' detail=' || LEFT(SQLERRM, 200), p_request_id)
            ELSE api.mcp_error(-32603, 'sqlstate=' || SQLSTATE || ' detail=' || LEFT(SQLERRM, 200), p_request_id)
        END;
        INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
        VALUES (v_handler.object_id, p_mcp_type, v_handler.mcp_name, v_request, v_response);

        RETURN CASE WHEN p_mcp_type = 'tool'
            THEN api.mcp_tool_error('Tool execution failed', p_request_id)
            ELSE api.mcp_error(-32603, 'Internal error', p_request_id)
        END;
    END;
END;
$$;

COMMENT ON FUNCTION internal.mcp_dispatch(text, text, jsonb, jsonb, jsonb) IS
    'Shared MCP invocation path: route lookup, auth context, auth gate, isolation floor, handler EXECUTE, exchange logging, sanitized errors. Called only by the api.mcp_* SECURITY DEFINER wrappers.';

DROP FUNCTION IF EXISTS api.mcp_call_tool(text, jsonb, jsonb, text);

CREATE OR REPLACE FUNCTION api.mcp_call_tool(
    p_name text,
    p_arguments jsonb,
    p_context jsonb DEFAULT NULL,
    p_request_id jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE sql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
    SELECT internal.mcp_dispatch('tool', p_name, p_arguments, p_context, p_request_id);
$$;

COMMENT ON FUNCTION api.mcp_call_tool(text, jsonb, jsonb, jsonb) IS
    'MCP tools/call. Resolves tool by name, applies auth context, invokes handler. Execution failures use result.isError=true per MCP spec.';

DROP FUNCTION IF EXISTS api.mcp_read_resource(text, jsonb, text);

CREATE OR REPLACE FUNCTION api.mcp_read_resource(
    p_uri text,
    p_context jsonb DEFAULT NULL,
    p_request_id jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE sql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
    SELECT internal.mcp_dispatch('resource', p_uri, NULL, p_context, p_request_id);
$$;

COMMENT ON FUNCTION api.mcp_read_resource(text, jsonb, jsonb) IS
    'MCP resources/read. Matches URI against registered templates with longest-match precedence.';

DROP FUNCTION IF EXISTS api.mcp_get_prompt(text, jsonb, jsonb, text);

CREATE OR REPLACE FUNCTION api.mcp_get_prompt(
    p_name text,
    p_arguments jsonb,
    p_context jsonb DEFAULT NULL,
    p_request_id jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE sql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
    SELECT internal.mcp_dispatch('prompt', p_name, p_arguments, p_context, p_request_id);
$$;

COMMENT ON FUNCTION api.mcp_get_prompt(text, jsonb, jsonb, jsonb) IS
    'MCP prompts/get. Resolves prompt by name, applies auth context, returns expanded messages.';

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
    -- resources/list returns concrete Resource objects only (required `uri`).
    -- Templated resources (RFC 6570 {placeholder}) are returned by the separate
    -- resources/templates/list method (api.mcp_list_resource_templates).
    SELECT jsonb_build_object('resources', COALESCE(jsonb_agg(
        jsonb_strip_nulls(
            jsonb_build_object(
                'name', r.mcp_name,
                'title', h.title,
                'description', h.description,
                'uri', r.uri_template,
                'mimeType', r.mime_type
            )
        )
    ), '[]'::jsonb))
    FROM api.mcp_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id
    WHERE r.mcp_type = 'resource'
      AND r.uri_template !~ '\{[^}]+\}'
      AND (NOT h.requires_auth
           OR NULLIF(current_setting('auth.user_id', true), '') IS NOT NULL);
$$;

COMMENT ON FUNCTION api.mcp_list_resources() IS
    'MCP resources/list discovery. Emits only concrete Resource objects (static uri). Templated resources are served by resources/templates/list. Hides auth-required resources from unauthenticated sessions.';

CREATE OR REPLACE FUNCTION api.mcp_list_resource_templates()
RETURNS jsonb
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, pg_temp
AS $$
    -- resources/templates/list returns ResourceTemplate objects (required
    -- `uriTemplate`) for resources whose uri carries an RFC 6570 placeholder.
    SELECT jsonb_build_object('resourceTemplates', COALESCE(jsonb_agg(
        jsonb_strip_nulls(
            jsonb_build_object(
                'name', r.mcp_name,
                'title', h.title,
                'description', h.description,
                'uriTemplate', r.uri_template,
                'mimeType', r.mime_type
            )
        )
    ), '[]'::jsonb))
    FROM api.mcp_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id
    WHERE r.mcp_type = 'resource'
      AND r.uri_template ~ '\{[^}]+\}'
      AND (NOT h.requires_auth
           OR NULLIF(current_setting('auth.user_id', true), '') IS NOT NULL);
$$;

COMMENT ON FUNCTION api.mcp_list_resource_templates() IS
    'MCP resources/templates/list discovery. Emits ResourceTemplate objects (uriTemplate) for resources with RFC 6570 placeholders. Hides auth-required resources from unauthenticated sessions.';

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
    RAISE NOTICE '  ✓ api.mcp_list_resource_templates() - MCP templated-resource discovery';
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
    EXECUTE format('GRANT EXECUTE ON FUNCTION internal.apply_mcp_auth_context(jsonb) TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION internal.provision_current_user(text) TO %I', v_api_role);
END $$;
