/*
<pgmi-meta
    id="a7f01000-0009-4000-8000-000000000001"
    idempotent="true">
  <description>
    Protocol dispatchers: REST, RPC, and MCP request invocation
  </description>
  <sortKeys>
    <key>004/009</key>
  </sortKeys>
</pgmi-meta>
*/

-- ============================================================================
-- Request context
-- ============================================================================
-- TRUST BOUNDARY (security critical):
--
-- A request names its caller only through opaque session handles:
--   x-login-session         the login session, opened by api.open_login_session
--                           after the transport signed the person in
--   x-organization-session  the browser tab's organization session (optional)
-- The application resolves them (internal.resolve_session_context) into the
-- principal, the organization and the permissions of the request; a caller
-- never names an identity or an organization. The transport keeps the login
-- handle in an httpOnly cookie and drops any client-sent copy of these headers.
--
-- An MCP request carries one of two credentials in its context:
--   {"connection": "..."}   the handle of a connection a person approved for
--                           an MCP client while signed in on the web
--   {"client": {"issuer", "subject", "clientId"}}
--                           the identity in an OAuth access token the gateway
--                           verified (signature, issuer, audience, expiry);
--                           the gateway never forwards the token itself
-- The application resolves either (internal.resolve_connection_context,
-- internal.resolve_client_context) into that person and the organization
-- chosen at approval; every other context key, an organization among them,
-- is ignored, and no one is provisioned. The client form trusts the gateway
-- as api.open_login_session does: only the api role reaches either.
--
-- What a non-public access declaration guarantees, precisely: the handles
-- resolve to a live session of an enabled person. Signing in is the
-- transport's job; the database only opens the session it is handed.

-- The framework default knows no sessions: every request is anonymous. The
-- application replaces this body in a file sorted after this one.
CREATE OR REPLACE FUNCTION internal.resolve_session_context(p_login_session text, p_organization_session text)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    RETURN;
END;
$$;

COMMENT ON FUNCTION internal.resolve_session_context(text, text) IS
    'Hook the application replaces: resolves a request''s login and organization session handles into its context, resetting whatever an earlier request in the transaction left. The default resolves nothing.';

CREATE OR REPLACE FUNCTION api.set_auth_context(p_headers extensions.hstore)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM internal.resolve_session_context(p_headers->'x-login-session', p_headers->'x-organization-session');
END;
$$;

COMMENT ON FUNCTION api.set_auth_context(extensions.hstore) IS
    'Gateway trust boundary: resolves the x-login-session and x-organization-session handles into the request context. No identity or organization is taken from a header.';

DROP FUNCTION IF EXISTS internal.resolve_connection_context(text);

CREATE FUNCTION internal.resolve_connection_context(p_connection text)
RETURNS jsonb
LANGUAGE plpgsql AS $$
BEGIN
    RETURN NULL;
END;
$$;

COMMENT ON FUNCTION internal.resolve_connection_context(text) IS
    'Hook the application replaces: resolves an MCP request''s connection handle into its context (the person who approved the connection, in the organization chosen at approval), resetting whatever an earlier request in the transaction left, or returns why a known connection cannot act yet, as {code, message, ...}. A handle that resolves to no one and no refusal is unknown. The default resolves nothing.';

CREATE OR REPLACE FUNCTION internal.resolve_client_context(p_issuer_id text, p_subject text, p_client_id text)
RETURNS jsonb
LANGUAGE plpgsql AS $$
BEGIN
    RETURN jsonb_build_object('code', 'pgmi.client_not_admitted', 'message', 'This server admits no OAuth client');
END;
$$;

COMMENT ON FUNCTION internal.resolve_client_context(text, text, text) IS
    'Hook the application replaces: resolves the identity the gateway verified in an MCP client''s access token (issuer, subject, client id) into the request context, or returns why the client cannot act yet, as {code, message, ...}. The default admits no one.';

DROP FUNCTION IF EXISTS internal.apply_mcp_auth_context(jsonb);

CREATE OR REPLACE FUNCTION internal.resolve_mcp_caller(p_context jsonb)
RETURNS jsonb
LANGUAGE plpgsql AS $$
DECLARE
    v_client jsonb := CASE WHEN jsonb_typeof(p_context->'client') = 'object' THEN p_context->'client' END;
    v_connection text := CASE WHEN jsonb_typeof(p_context) = 'object' THEN p_context->>'connection' END;
    v_refusal jsonb;
BEGIN
    IF v_client IS NOT NULL THEN
        RETURN internal.resolve_client_context(v_client->>'issuer', v_client->>'subject', v_client->>'clientId');
    END IF;
    v_refusal := internal.resolve_connection_context(v_connection);
    IF v_refusal IS NULL AND v_connection IS NOT NULL THEN
        IF api.current_principal_id() IS NULL THEN
            RETURN jsonb_build_object('code', 'pgmi.connection_unknown',
                                      'message', 'The connection handle is unknown, revoked or expired');
        END IF;
    END IF;
    RETURN v_refusal;
END;
$$;

COMMENT ON FUNCTION internal.resolve_mcp_caller(jsonb) IS
    'MCP trust boundary: resolves {client: {issuer, subject, clientId}} (an access token the gateway verified) or else {connection} (a handle) from the context and ignores every other key; a NULL or empty context resets to nobody. Returns NULL, or the refusal the caller answers as JSON-RPC -31001: pgmi.connection_unknown for a handle that resolves to no one (the gateway answers it 401), otherwise why a known client cannot act yet. Shared by the MCP dispatcher and invocation handlers.';

-- -31001 at HTTP 200, so every MCP client shows the message: a person must
-- act on the web (approve, resume, switch agents on) before the client can.
-- The gateway answers pgmi.connection_unknown as 401 instead.
CREATE OR REPLACE FUNCTION internal.mcp_caller_refusal(p_refusal jsonb, p_request_id jsonb)
RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE AS $$
    SELECT api.mcp_error(-31001, p_refusal->>'message', p_request_id, p_refusal - 'message');
$$;

-- The body never names the missing permission: the log has the truth.
CREATE OR REPLACE FUNCTION internal.access_denied_response(p_reason text)
RETURNS api.http_response
LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE p_reason
        WHEN 'unauthenticated' THEN
            api.problem_response(401, 'Unauthorized', 'Authentication required', code => 'pgmi.unauthenticated')
        WHEN 'organization_required' THEN
            api.problem_response(403, 'Forbidden', 'This operation needs an active organization',
                                 code => 'pgmi.organization_required')
        ELSE
            api.problem_response(403, 'Forbidden', 'You do not have access to this operation',
                                 code => 'pgmi.forbidden')
    END;
$$;

-- What a handler's uncaught error tells the client, shared by the REST, RPC
-- and MCP gateways. Only SQLSTATEs a client can cause are listed; everything
-- else, class 22 included (division_by_zero, a bad regex, an array subscript),
-- is a handler bug and answers 500, so it reaches 5xx alerting. The detail is
-- per SQLSTATE and never carries SQLERRM.
CREATE OR REPLACE FUNCTION internal.sqlstate_problem(
    p_sqlstate text,
    p_method text,
    p_read_only boolean,
    OUT status integer,
    OUT title text,
    OUT detail text,
    OUT code text,
    OUT rpc_code integer
)
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE(m.status, 500),
           COALESCE(m.title, 'Internal Server Error'),
           COALESCE(m.detail, 'An internal error occurred'),
           m.code,
           CASE WHEN m.status IS NULL THEN -32603
                WHEN m.status = 428 THEN -32600
                WHEN m.status = 403 THEN -32003
                ELSE -32602 END
    FROM (SELECT 1) one
    LEFT JOIN LATERAL (
        SELECT v.status, v.title, v.detail, v.code, v.method IS NOT NULL AS specific
        FROM (VALUES
            ('22P02', NULL,     400, 'Bad Request',          'A submitted value is malformed',                 'pgmi.malformed_value'),
            ('22021', NULL,     400, 'Bad Request',          'A submitted value is not valid text',            'pgmi.invalid_encoding'),
            ('22P05', NULL,     400, 'Bad Request',          'A submitted value is not valid text',            'pgmi.invalid_encoding'),
            ('22023', NULL,     400, 'Bad Request',          'A submitted parameter value is invalid',         'pgmi.invalid_parameter_value'),
            ('22007', NULL,     400, 'Bad Request',          'A submitted date or time is malformed',          'pgmi.malformed_datetime'),
            ('22001', NULL,     422, 'Unprocessable Entity', 'A submitted value is too long',                 'pgmi.value_too_long'),
            ('22003', NULL,     422, 'Unprocessable Entity', 'A submitted number is out of range',            'pgmi.number_out_of_range'),
            ('22008', NULL,     422, 'Unprocessable Entity', 'A submitted date or time is out of range',      'pgmi.datetime_out_of_range'),
            ('23502', NULL,     400, 'Bad Request',          'A required value is missing',                    'pgmi.value_required'),
            ('23503', 'DELETE', 409, 'Conflict',             'The resource is in use',                         'pgmi.resource_in_use'),
            ('23503', NULL,     400, 'Bad Request',          'References a resource that does not exist',      'pgmi.reference_not_found'),
            ('23505', NULL,     409, 'Conflict',             'Resource already exists',                        'pgmi.already_exists'),
            ('23P01', NULL,     409, 'Conflict',             'Conflicts with an existing resource',            'pgmi.conflict'),
            ('55000', NULL,     409, 'Conflict',             'The resource is not in a state that allows this', 'pgmi.invalid_state'),
            ('23514', NULL,     422, 'Unprocessable Entity', 'A submitted value violates a constraint',       'pgmi.constraint_violation'),
            ('P0002', NULL,     404, 'Not Found',            'Resource does not exist',                        'pgmi.not_found')
        ) AS v(sqlstate, method, status, title, detail, code)
        WHERE v.sqlstate = p_sqlstate AND (v.method IS NULL OR v.method = p_method)
        UNION ALL
        -- 25006 has two causes. A route that did not declare readOnly, run in a
        -- READ ONLY transaction, is the caller's precondition to fix; a route
        -- that declared readOnly and wrote anyway broke its own contract.
        SELECT 428, 'Precondition Required',
               'Route requires a read-write transaction but the current transaction is READ ONLY',
               'pgmi.transaction_read_write_required', false
        WHERE p_sqlstate = '25006' AND NOT p_read_only
        ORDER BY specific DESC
        LIMIT 1
    ) m ON true;
$$;

COMMENT ON FUNCTION internal.sqlstate_problem(text, text, boolean) IS
    'Maps a handler''s uncaught SQLSTATE to status, title, client detail, pgmi.* problem code and JSON-RPC code. Unlisted SQLSTATEs are 500 / -32603.';

-- ============================================================================
-- REST Gateway
-- ============================================================================

-- The routing rule: which live handler serves a method and a canonical path.
-- The most specific route wins: a literal segment beats a parameter, so
-- /tasks/stats is never taken for a task id. The literal prefix is tested
-- before the regex because PostgreSQL caches only a few compiled regexes per
-- backend. The gateway, the transaction-policy lookup and the access
-- simulation all call this, so they cannot disagree about the target.
CREATE OR REPLACE FUNCTION internal.rest_route_lookup(p_method text, p_path text)
RETURNS uuid
LANGUAGE plpgsql STABLE
SET search_path = api, internal, common, pg_temp
AS $$
BEGIN
    RETURN (
        SELECT r.handler_object_id
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE starts_with(p_path, r.address_prefix)
          AND p_path ~ r.address_regexp
          AND p_method ~ r.method_regexp
        ORDER BY r.specificity DESC, r.sequence_number DESC, r.route_name
        LIMIT 1);
END $$;

-- The route's path parameters by name, percent-decoded ('+' is literal in a
-- path, unlike a query), and the ones whose value is not of the declared type.
CREATE OR REPLACE FUNCTION internal.rest_path_params(
    p_path text,
    p_address_regexp text,
    p_spec jsonb,
    OUT path_params jsonb,
    OUT invalid_params jsonb
)
LANGUAGE sql IMMUTABLE PARALLEL SAFE
SET search_path = api, internal, extensions, common, pg_temp
AS $$
    WITH value AS (
        SELECT s.param->>'name' AS name, s.param->>'type' AS type,
               api.url_decode(replace(m.captures[s.ord::int], '+', '%2B')) AS value
        FROM jsonb_array_elements(p_spec) WITH ORDINALITY AS s(param, ord)
        CROSS JOIN (SELECT regexp_match(p_path, p_address_regexp) AS captures) m
    )
    SELECT COALESCE(jsonb_object_agg(name, value), '{}'::jsonb),
           jsonb_agg(api.invalid_param(name, 'must be ' || CASE type WHEN 'uuid' THEN 'a uuid' ELSE 'an integer' END))
               FILTER (WHERE (type = 'uuid' AND common.try_cast(value, NULL::uuid) IS NULL)
                          OR (type = 'int' AND (value !~ '^-?[0-9]+$' OR common.try_cast(value, NULL::int) IS NULL)))
    FROM value;
$$;

-- Vary members of both lists, lowercased and deduplicated in first-seen
-- order; '*' from the handler wins, since it already varies on everything.
CREATE OR REPLACE FUNCTION internal.vary_union(p_handler text, p_gateway text)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE WHEN btrim(COALESCE(p_handler, '')) = '*' THEN '*' ELSE (
        SELECT string_agg(member, ', ' ORDER BY first_seen)
        FROM (
            SELECT lower(btrim(m)) AS member, min(ord) AS first_seen
            FROM unnest(string_to_array(p_gateway || ',' || COALESCE(p_handler, ''), ',')) WITH ORDINALITY AS t(m, ord)
            WHERE btrim(m) <> ''
            GROUP BY lower(btrim(m))
        ) members
    ) END
$$;

-- HTTP field names are case-insensitive (RFC 9110 5.1); the gateways read
-- lowercase keys, so a transport passing X-Login-Session or Content-Type (Go's
-- http.Header does) would lose its session and skip content negotiation.
-- NULL when two names differ only in case: hstore keeps one of them, and which
-- one is unspecified, so a client-sent X-Login-Session could win over the
-- transport's x-login-session. The gateways answer 400 instead of guessing.
CREATE OR REPLACE FUNCTION internal.lowercase_header_keys(p_headers extensions.hstore)
RETURNS extensions.hstore
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE WHEN count(DISTINCT lower(key)) = count(*)
                THEN COALESCE(extensions.hstore(array_agg(lower(key)), array_agg(value)), ''::extensions.hstore)
           END
    FROM extensions.each(p_headers)
$$;

-- Response-header finalization shared by the REST and RPC gateways: merges
-- handler-registered headers (keys lowercased for HTTP case-insensitive
-- semantics; the x-include-schema directive controls $schema injection and
-- MUST NOT appear on the wire), then stamps content-length, timing, the catalog
-- version, and the protocol-specific extras, defaulting content-type to JSON
-- when the handler set none. Later concatenations win, so stamps override
-- registered headers.
--
-- x-pgmi-catalog-version is the whole point of stamping it HERE rather than only
-- on /openapi.json: a client learns its cached contract went stale from any
-- response it was already making, instead of polling the spec to find out.
--
-- STABLE, not IMMUTABLE: api.catalog_version() reads the handler registry.
CREATE OR REPLACE FUNCTION internal.finalize_response_headers(
    p_response api.http_response,
    p_registered jsonb,
    p_execution_ms numeric,
    p_extra extensions.hstore
) RETURNS extensions.hstore
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    WITH handler AS (
        -- Lowercase the handler's OWN headers too, not just the registered ones.
        -- HTTP header names are case-insensitive, but hstore keys are not, and the
        -- content-type guard below is a case-sensitive `?`. A handler returning
        -- 'Content-Type' would slip past that guard and the JSON default would be
        -- appended alongside it — the response then carrying two conflicting
        -- content-types. Normalising every key here makes right-wins dedup work
        -- regardless of how a hand-built response cased its keys.
        SELECT COALESCE((
                SELECT extensions.hstore(array_agg(lower(key)), array_agg(value))
                FROM extensions.each(COALESCE((p_response).headers, ''::extensions.hstore))
            ), ''::extensions.hstore)
            || COALESCE((
                SELECT extensions.hstore(array_agg(lower(key)), array_agg(value))
                FROM jsonb_each_text(p_registered)
                WHERE lower(key) <> 'x-include-schema'
            ), ''::extensions.hstore) AS hh
    ),
    merged AS (
        SELECT hh
            || extensions.hstore(ARRAY[
                'content-length', COALESCE(octet_length((p_response).content), 0)::text,
                'x-execution-time-ms', p_execution_ms::text,
                'x-pgmi-catalog-version', api.catalog_version()
            ])
            -- The API selects representations on accept and scopes bodies to
            -- the session: the client sends it as the __Host-session cookie,
            -- which the gateway turns into x-login-session, so Vary names
            -- cookie, the header a cache actually sees. Identity travels there
            -- and not in Authorization, so RFC 9111 §3.5's shared-cache prohibition
            -- never engages: without Vary, a heuristically-caching intermediary
            -- may store one user's GET /me and replay it to another. Only
            -- PostgreSQL knows this, so the fronting proxy cannot add it.
            -- A union, not an overwrite: a handler's own Vary (say
            -- accept-language) must survive, or a shared cache serves one
            -- language to everyone.
            || extensions.hstore('vary', internal.vary_union(hh->'vary', 'accept, cookie, x-organization-session'))
            || COALESCE(p_extra, ''::extensions.hstore) AS h
        FROM handler
    )
    -- hstore(k, v) constructor, not a '=>' literal: the value's embedded
    -- space is a syntax error under hstore's unquoted-literal parsing.
    SELECT CASE
        -- RFC 9110 §15.4.5: a 304 carries the representation metadata a 200
        -- would have sent. content-length: 0 and an invented charset are not
        -- that -- they contradict the 200 the client already cached. 204 is
        -- bodiless for the same reason.
        WHEN (p_response).status_code IN (204, 304) THEN h - 'content-length'::text
        WHEN h ? 'content-type' THEN h
        ELSE h || extensions.hstore('content-type', 'application/json; charset=utf-8')
    END
    FROM merged;
$$;

COMMENT ON FUNCTION internal.finalize_response_headers(api.http_response, jsonb, numeric, extensions.hstore) IS
    'Merges handler-registered response headers (lowercased, x-include-schema stripped) and stamps content-length, x-execution-time-ms, Vary, protocol extras, and a JSON content-type default. Omits body-framing headers on 204/304. Shared by rest_invoke and rpc_invoke.';

-- Every early return goes through here so error responses keep two headers:
-- x-pgmi-catalog-version, because a stale cached route table shows up exactly
-- as a 404, and Vary, without which a 401/406 is cached against the wrong request.
CREATE OR REPLACE FUNCTION internal.finalize_error(
    p_response api.http_response,
    p_start_time timestamptz,
    p_extra extensions.hstore DEFAULT ''::extensions.hstore
) RETURNS api.http_response
LANGUAGE sql VOLATILE PARALLEL SAFE AS $$
    SELECT (
        (p_response).status_code,
        internal.finalize_response_headers(
            p_response,
            '{}'::jsonb,
            extract(epoch FROM (clock_timestamp() - p_start_time)) * 1000,
            -- An error response is never a cacheable representation of the
            -- resource, and several of these are identity-dependent.
            COALESCE(p_extra, ''::extensions.hstore)
                || extensions.hstore('cache-control', 'no-store')
        ),
        (p_response).content
    )::api.http_response;
$$;

COMMENT ON FUNCTION internal.finalize_error(api.http_response, timestamptz, extensions.hstore) IS
    'Stamps a gateway error response with the same header set as a successful one, plus cache-control: no-store.';

-- RFC 9110 §9.3.2: a HEAD response has no content, the error ones included.
-- content-length keeps what GET would have sent.
CREATE OR REPLACE FUNCTION internal.finalize_rest_error(
    p_head boolean,
    p_response api.http_response,
    p_start_time timestamptz,
    p_extra extensions.hstore DEFAULT ''::extensions.hstore
) RETURNS api.http_response
LANGUAGE sql VOLATILE PARALLEL SAFE AS $$
    SELECT (f.status_code, f.headers, CASE WHEN p_head THEN NULL ELSE f.content END)::api.http_response
    FROM internal.finalize_error(p_response, p_start_time, p_extra) AS f;
$$;

COMMENT ON FUNCTION internal.finalize_rest_error(boolean, api.http_response, timestamptz, extensions.hstore) IS
    'internal.finalize_error for the REST gateway: drops the content of a HEAD response.';

-- Exchange logs keep requests and responses for replay and audit, which must
-- not turn them into a store of live credentials: bearer tokens, raw API keys,
-- session cookies, session and MCP connection handles are dropped from the
-- logged copy only.
-- The Referer goes too: a page address can carry a one-time code.
CREATE OR REPLACE FUNCTION internal.application_unloggable_headers()
RETURNS text[]
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT '{}'::text[];
$$;

COMMENT ON FUNCTION internal.application_unloggable_headers() IS
    'Hook the application replaces: lower-case names of further request and response headers the exchange log must not keep, on top of the framework''s credential headers and referer. The default adds none.';

CREATE OR REPLACE FUNCTION internal.unloggable_headers()
RETURNS text[]
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT ARRAY['authorization', 'proxy-authorization', 'cookie', 'set-cookie', 'x-api-key',
                 'x-login-session', 'x-organization-session', 'x-connection',
                 'x-auth-token', 'x-access-token', 'x-refresh-token', 'x-csrf-token', 'x-xsrf-token',
                 'referer']
           || internal.application_unloggable_headers();
$$;

CREATE OR REPLACE FUNCTION internal.without_credentials(p_headers extensions.hstore)
RETURNS extensions.hstore
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT COALESCE(extensions.hstore(array_agg(h.key), array_agg(h.value)), ''::extensions.hstore)
    FROM extensions.each(p_headers) AS h
    WHERE lower(h.key) <> ALL (internal.unloggable_headers());
$$;

CREATE OR REPLACE FUNCTION internal.loggable(p_request api.rest_request)
RETURNS api.rest_request
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT (p_request.method, p_request.url, internal.without_credentials(p_request.headers), p_request.content, p_request.path_params)::api.rest_request;
$$;

CREATE OR REPLACE FUNCTION internal.loggable(p_request api.rpc_request)
RETURNS api.rpc_request
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT (p_request.route_id, internal.without_credentials(p_request.headers), p_request.content)::api.rpc_request;
$$;

-- The MCP context carries identity and may carry a token; the exchange log
-- keeps who called, not what they authenticated with.
CREATE OR REPLACE FUNCTION internal.loggable(p_request api.mcp_request)
RETURNS api.mcp_request
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (p_request.arguments, p_request.uri,
            (SELECT jsonb_object_agg(c.key, c.value)
             FROM jsonb_each(p_request.context) AS c
             WHERE lower(c.key) !~ '(token|authorization|password|secret|api_?key|cookie|credential|session|connection)'),
            p_request.request_id)::api.mcp_request;
$$;

CREATE OR REPLACE FUNCTION internal.loggable(p_response api.http_response)
RETURNS api.http_response
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT (p_response.status_code, internal.without_credentials(p_response.headers), p_response.content)::api.http_response;
$$;

-- What replay needs to act as the original caller. A logged request keeps no
-- credential, so the gateway stores the context the request resolved to and
-- api.replay_exchange applies it instead of re-presenting a handle. The pair
-- is replaced together by an application whose context holds more than the
-- principal (membership/ does).
CREATE OR REPLACE FUNCTION internal.request_log_context()
RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
BEGIN
    RETURN jsonb_strip_nulls(jsonb_build_object('principalId', api.current_principal_id()));
END;
$$;

COMMENT ON FUNCTION internal.request_log_context() IS
    'Hook the application replaces: the request context the exchange log stores for replay. The default is the principal id. organizationId and actualPrincipalId, when present, must be uuids: the log stores them as uuid columns.';

CREATE OR REPLACE FUNCTION internal.apply_request_log_context(p_context jsonb)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'internal.apply_request_log_context() is not implemented: replay needs the application to apply a logged context'
        USING ERRCODE = 'feature_not_supported';
END;
$$;

COMMENT ON FUNCTION internal.apply_request_log_context(jsonb) IS
    'Hook the application replaces with internal.request_log_context(): sets the request context from what that hook stored. The framework knows no sessions, so its default refuses.';

CREATE OR REPLACE FUNCTION internal.rest_dispatch(
    p_method text,
    p_url text,
    p_headers extensions.hstore,
    p_content bytea,
    p_context_from_headers boolean
) RETURNS api.http_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
DECLARE
    v_request api.rest_request;
    v_response api.http_response;
    v_route record;
    v_params record;
    v_path text;
    v_content_type text;
    v_iso_shortfall text;
    v_start_time timestamptz;
    v_execution_ms numeric;
    v_match_method text;
    v_is_head boolean;
    v_allow text;
    v_invalid_params jsonb;
    v_body jsonb;
    v_body_violation text;
    v_auth_extra extensions.hstore;
    v_denial text;
    v_location text;
BEGIN
    v_start_time := clock_timestamp();
    RAISE DEBUG 'rest_invoke: % %', p_method, p_url;
    v_is_head := upper(trim(p_method)) = 'HEAD';

    IF length(p_method) > 16 THEN
        RETURN internal.finalize_rest_error(v_is_head,
            api.problem_response(400, 'Bad Request', 'HTTP method too long', code => 'pgmi.method_too_long'), v_start_time);
    END IF;
    IF length(p_url) > 8192 THEN
        RETURN internal.finalize_rest_error(v_is_head,
            api.problem_response(414, 'URI Too Long', 'URL exceeds maximum length', code => 'pgmi.uri_too_long'), v_start_time);
    END IF;

    p_method := upper(trim(p_method));
    p_url := trim(p_url);
    p_headers := internal.lowercase_header_keys(p_headers);
    IF p_headers IS NULL THEN
        RETURN internal.finalize_rest_error(v_is_head,
            api.problem_response(400, 'Bad Request', 'Header names repeat with different case', code => 'pgmi.header_collision'), v_start_time);
    END IF;

    -- Match against the path only; the query string is checked against the
    -- route's declared query contract and parsed by the handler.
    v_path := api.canonical_path(api.url_path(p_url));

    -- RFC 9110 §9.3.2: HEAD is GET without a body. The default method_regexp
    -- does not list it, so routing HEAD literally 404s every route that took
    -- the default. Match it as GET and strip the body on the way out.
    v_match_method := CASE WHEN v_is_head THEN 'GET' ELSE p_method END;

    SELECT h.handler_exec_sql, h.object_id, h.response_headers, h.accepts, h.produces, h.access,
           h.output_json_schema, h.input_json_schema, h.min_transaction_isolation, h.read_only,
           r.route_name, r.auto_log, r.query_schema, r.address_regexp, r.path_params, r.location_template
    INTO v_route
    FROM api.rest_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id
    WHERE r.handler_object_id = internal.rest_route_lookup(v_match_method, v_path);

    IF v_route.handler_exec_sql IS NULL THEN
        -- A path that exists under other methods answers 405 with the mandatory
        -- Allow header (RFC 9110 §15.5.6), not 404. The proxy in front cannot
        -- synthesize this: it has no route table.
        --
        -- Method sets are public: OPTIONS and 405 list every method of the
        -- path, whoever asks. Hiding them buys nothing, since 401 versus 404
        -- already tells a stranger the path exists, and a CORS preflight
        -- carries no credentials by definition.
        SELECT string_agg(DISTINCT upper(m.method), ', ' ORDER BY upper(m.method))
        INTO v_allow
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        CROSS JOIN LATERAL unnest(api.openapi_methods(r.method_regexp)) AS m(method)
        WHERE starts_with(v_path, r.address_prefix)
          AND v_path ~ r.address_regexp;

        IF v_allow IS NOT NULL THEN
            -- HEAD is served wherever GET is, and OPTIONS wherever a route matches.
            IF v_allow ~ 'GET' AND v_allow !~ 'HEAD' THEN
                v_allow := v_allow || ', HEAD';
            END IF;
            v_allow := v_allow || ', OPTIONS';

            IF p_method = 'OPTIONS' THEN
                RETURN internal.finalize_rest_error(v_is_head,
                    (204, ''::extensions.hstore, NULL)::api.http_response,
                    v_start_time,
                    extensions.hstore('allow', v_allow));
            END IF;

            RAISE DEBUG 'rest_invoke: path matched but method % not allowed', p_method;
            RETURN internal.finalize_rest_error(v_is_head,
                api.problem_response(405, 'Method Not Allowed',
                    format('%s is not allowed on %s. Allowed: %s', p_method, v_path, v_allow),
                    code => 'pgmi.method_not_allowed'),
                v_start_time,
                extensions.hstore('allow', v_allow));
        END IF;

        RAISE DEBUG 'rest_invoke: No route matched';
        RETURN internal.finalize_rest_error(v_is_head,
            api.problem_response(404, 'Not Found', 'No route matches ' || p_method || ' ' || p_url,
                                 code => 'pgmi.not_found'),
            v_start_time);
    END IF;

    RAISE DEBUG 'rest_invoke: Matched route %', v_route.route_name;

    -- Resolve the sessions first, then gate on what they resolved to, so an
    -- unknown, expired or revoked handle fails closed. Replay has set the
    -- logged context already.
    IF p_context_from_headers THEN
        PERFORM api.set_auth_context(p_headers);
    END IF;

    -- An authenticated route's representation is per-identity; a shared cache
    -- must not store it even with Vary present.
    v_auth_extra := CASE
        WHEN v_route.access <> 'public' THEN extensions.hstore('cache-control', 'private, no-store')
        ELSE ''::extensions.hstore
    END;

    -- The access decision, once, before the handler runs. It gates on a
    -- RESOLVED principal: a handle that names no live session, or the session
    -- of a disabled person, resolves to nobody. Every refusal is logged.
    v_denial := internal.current_access_denial(v_route.object_id, v_route.access);
    IF v_denial IS NOT NULL THEN
        RAISE DEBUG 'rest_invoke: access denied (%)', v_denial;
        PERFORM internal.log_access_denial(v_route.object_id, v_denial, p_method, v_path);
        RETURN internal.finalize_rest_error(v_is_head,
            internal.access_denied_response(v_denial),
            v_start_time,
            v_auth_extra
                -- RFC 9110 §15.5.2: a 401 MUST carry at least one challenge.
                || CASE WHEN v_denial = 'unauthenticated'
                        THEN extensions.hstore('www-authenticate', api.www_authenticate_challenge())
                        ELSE ''::extensions.hstore END);
    END IF;

    -- Typed path parameters, checked once here and after the access decision,
    -- so a stranger learns nothing and a handler never sees a malformed id.
    BEGIN
        SELECT * INTO v_params FROM internal.rest_path_params(v_path, v_route.address_regexp, v_route.path_params);
    EXCEPTION WHEN character_not_in_repertoire OR untranslatable_character THEN
        RETURN internal.finalize_rest_error(v_is_head, api.problem_response(
            400, 'Bad Request', 'A path parameter is not valid UTF-8 once decoded',
            code => 'pgmi.invalid_encoding'), v_start_time, v_auth_extra);
    END;
    IF v_params.invalid_params IS NOT NULL THEN
        RETURN internal.finalize_rest_error(v_is_head, api.problem_response(
            400, 'Bad Request',
            format('Invalid path parameter: %s',
                   (SELECT string_agg(e->>'name', ', ') FROM jsonb_array_elements(v_params.invalid_params) e)),
            code => 'pgmi.invalid_path_parameter',
            invalid_params => v_params.invalid_params
        ), v_start_time, v_auth_extra);
    END IF;

    -- The declared querySchema is checked before the isolation and read-only
    -- checks, so a malformed request is a 400 whatever transaction it arrived
    -- in. Parsed, not matched: parameter order and repetition do not matter.
    IF v_route.query_schema IS NOT NULL THEN
        BEGIN
            v_invalid_params := internal.query_schema_violations(v_route.query_schema::jsonb, api.query_params_multi(p_url));
        EXCEPTION WHEN character_not_in_repertoire OR untranslatable_character THEN
            RETURN internal.finalize_rest_error(v_is_head, api.problem_response(
                400, 'Bad Request', 'A query parameter is not valid UTF-8 once decoded',
                code => 'pgmi.invalid_encoding'), v_start_time, v_auth_extra);
        END;
        IF v_invalid_params IS NOT NULL THEN
            RETURN internal.finalize_rest_error(v_is_head, api.problem_response(
                400, 'Bad Request',
                format('Invalid query parameter: %s',
                       (SELECT string_agg(e->>'name', ', ') FROM jsonb_array_elements(v_invalid_params) e)),
                code => 'pgmi.invalid_query',
                invalid_params => v_invalid_params
            ), v_start_time, v_auth_extra);
        END IF;
    END IF;

    -- Enforce the route's transaction isolation floor. The gateway can only READ
    -- the level; the caller must open the transaction at the required level
    -- before the first statement (see lib/api/00-transaction-isolation.sql).
    v_iso_shortfall := internal.transaction_isolation_shortfall(v_route.min_transaction_isolation);
    IF v_iso_shortfall IS NOT NULL THEN
        RAISE DEBUG 'rest_invoke: isolation too weak (need %, have %)',
            v_route.min_transaction_isolation, v_iso_shortfall;
        RETURN internal.finalize_rest_error(v_is_head, api.problem_response(
            428, 'Precondition Required',
            format('Route requires %s isolation but current transaction uses %s.',
                   v_route.min_transaction_isolation, v_iso_shortfall),
            code => 'pgmi.transaction_isolation_too_weak',
            extensions => jsonb_build_object('requiredIsolation', v_route.min_transaction_isolation)
        ), v_start_time, v_auth_extra);
    END IF;

    -- Enforce the route's read-only declaration the same way: the gateway can
    -- only READ the access mode; the caller opens the transaction READ ONLY
    -- (resolve-then-open via api.rest_route_policy) or is rejected fail-closed.
    IF v_route.read_only AND NOT internal.transaction_is_read_only() THEN
        RAISE DEBUG 'rest_invoke: read-only route in a read-write transaction';
        RETURN internal.finalize_rest_error(v_is_head, api.problem_response(
            428, 'Precondition Required',
            'Route requires a READ ONLY transaction but the current transaction is read-write.',
            code => 'pgmi.transaction_read_only_required',
            extensions => jsonb_build_object('requiredReadOnly', true)
        ), v_start_time, v_auth_extra);
    END IF;

    -- Enforce the handler's declared accepts against the request Content-Type.
    -- Only when the request carries a Content-Type (a body). A handler with an
    -- inputSchema accepts application/json by default, any other */*.
    v_content_type := btrim(split_part(COALESCE(p_headers->'content-type', ''), ';', 1));
    IF v_content_type <> ''
       AND NOT api.accept_matches(array_to_string(v_route.accepts, ', '), ARRAY[v_content_type]) THEN
        RETURN internal.finalize_rest_error(v_is_head, api.problem_response(
            415,
            'Unsupported Media Type',
            format('Supported request content types: %s', array_to_string(v_route.accepts, ', ')),
            code => 'pgmi.unsupported_media_type'
        ), v_start_time, v_auth_extra);
    END IF;

    IF NOT api.accept_matches(p_headers->'accept', v_route.produces) THEN
        RETURN internal.finalize_rest_error(v_is_head, api.problem_response(
            406,
            'Not Acceptable',
            format('Supported content types: %s', array_to_string(v_route.produces, ', ')),
            code => 'pgmi.not_acceptable'
        ), v_start_time, v_auth_extra);
    END IF;

    -- The declared inputSchema is checked here, as it is for MCP tool
    -- arguments, for the methods whose body the spec publishes it for. Only
    -- the top level: required keys and property types. The rest is the
    -- handler's to check.
    IF v_route.input_json_schema IS NOT NULL AND v_match_method IN ('POST', 'PUT', 'PATCH') THEN
        BEGIN
            v_body := COALESCE(api.content_json(NULLIF(p_content, ''::bytea)), 'null'::jsonb);
        EXCEPTION WHEN invalid_text_representation OR character_not_in_repertoire OR untranslatable_character THEN
            RETURN internal.finalize_rest_error(v_is_head,
                api.problem_response(400, 'Bad Request', 'The request body is not valid JSON',
                                     code => 'pgmi.malformed_body'),
                v_start_time, v_auth_extra);
        END;
        v_body_violation := internal.json_schema_errors(internal.resolved_schema(v_route.input_json_schema::jsonb), v_body);
        IF v_body_violation IS NOT NULL THEN
            RETURN internal.finalize_rest_error(v_is_head,
                api.problem_response(400, 'Bad Request', 'Invalid request body: ' || v_body_violation,
                                     code => 'pgmi.invalid_body',
                                     invalid_params => internal.json_schema_invalid_params(
                                         internal.resolved_schema(v_route.input_json_schema::jsonb), v_body)),
                v_start_time, v_auth_extra);
        END IF;
    END IF;

    v_request := (p_method, p_url, p_headers, p_content, v_params.path_params)::api.rest_request;

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

        IF v_route.location_template IS NOT NULL AND (v_response).status_code = 201 THEN
            v_location := rtrim(api.openapi_server_url(), '/')
                       || internal.fill_location_template(v_route.location_template, api.content_json((v_response).content));
            IF v_location IS NULL THEN
                RAISE EXCEPTION 'handler % answered 201 without the values of its locationTemplate %',
                    v_route.route_name, v_route.location_template;
            END IF;
        END IF;

        v_response.headers := internal.finalize_response_headers(
            v_response, v_route.response_headers, v_execution_ms,
            extensions.hstore('x-route-id', v_route.object_id::text) || v_auth_extra
                || CASE WHEN v_location IS NOT NULL THEN extensions.hstore('location', v_location)
                        ELSE ''::extensions.hstore END);

        IF (v_response).status_code IS NULL OR (v_response).status_code NOT BETWEEN 100 AND 599 THEN
            RAISE EXCEPTION 'handler % answered status %', v_route.route_name, coalesce((v_response).status_code::text, 'NULL');
        END IF;

        -- An error answer changes nothing: raising rolls the handler's writes
        -- back to this block's savepoint, and v_response keeps the answer.
        IF (v_response).status_code >= 400 THEN
            RAISE EXCEPTION USING ERRCODE = 'PGRBK';
        END IF;

    EXCEPTION
    WHEN SQLSTATE 'PGRBK' THEN
        NULL;

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
        v_problem record;
    BEGIN
        RAISE DEBUG 'rest_invoke: Handler exception: %', SQLERRM;
        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;
        v_problem := internal.sqlstate_problem(v_sqlstate, v_match_method, v_route.read_only);

        -- Logged copy keeps SQLSTATE + truncated SQLERRM. Full SQLERRM may
        -- include attacker-supplied input or PII (handlers commonly raise
        -- "Invalid email: <user_input>"); truncating limits the blast radius
        -- if exchange-table grants ever loosen.
        v_response := api.problem_response(v_problem.status, v_problem.title,
            'sqlstate=' || v_sqlstate || ' detail=' || LEFT(SQLERRM, 200), code => v_problem.code);
        v_response.headers := extensions.hstore(ARRAY[
            'content-type', 'application/json; charset=utf-8',
            'content-length', COALESCE(octet_length((v_response).content), 0)::text,
            'x-execution-time-ms', v_execution_ms::text,
            'x-pgmi-catalog-version', api.catalog_version(),
            'x-error-sqlstate', v_sqlstate
        ]);
        -- autoLog=false must hold on the failure path too: v_request carries the
        -- headers and body, so a POST /login registered autoLog=false would
        -- otherwise persist plaintext credentials on any constraint violation.
        IF v_route.auto_log AND NOT internal.transaction_is_read_only() THEN
            INSERT INTO api.rest_exchange (handler_object_id, context, request, response, completed_at)
            VALUES (v_route.object_id, internal.request_log_context(),
                    internal.loggable(v_request), internal.loggable(v_response), now());
        END IF;

        -- x-error-sqlstate stays on the logged copy only: it is an internal
        -- diagnostic, and the sanitized body deliberately withholds the rest of
        -- the failure detail.
        RETURN internal.finalize_rest_error(v_is_head,
            api.problem_response(v_problem.status, v_problem.title, v_problem.detail, code => v_problem.code),
            v_start_time,
            v_auth_extra
                || CASE WHEN v_problem.status = 428
                        THEN extensions.hstore('x-pgmi-transaction-required', 'read-write')
                        ELSE ''::extensions.hstore END);
    END;
    END;

    -- Exchange logging is a write; a READ ONLY transaction cannot do it.
    -- Skipping beats failing every correctly-opened read-only request.
    IF v_route.auto_log AND NOT internal.transaction_is_read_only() THEN
        INSERT INTO api.rest_exchange (handler_object_id, context, request, response, completed_at)
        VALUES (v_route.object_id, internal.request_log_context(),
                internal.loggable(v_request), internal.loggable(v_response), now());
    END IF;

    -- RFC 9110 §9.3.2: HEAD is identical to GET except that the body is
    -- omitted. content-length keeps the value GET would have sent, which is
    -- what makes HEAD useful for probing size. The exchange above logs the
    -- full response deliberately.
    IF v_is_head THEN
        v_response := ((v_response).status_code, (v_response).headers, NULL::bytea)::api.http_response;
    END IF;

    RETURN v_response;
END;
$$;

COMMENT ON FUNCTION internal.rest_dispatch(text, text, extensions.hstore, bytea, boolean) IS
    'REST dispatch behind api.rest_invoke (context from the headers) and api.replay_exchange (the logged context, already set).';

CREATE OR REPLACE FUNCTION api.rest_invoke(
    p_method text,
    p_url text,
    p_headers extensions.hstore DEFAULT ''::extensions.hstore,
    p_content bytea DEFAULT NULL
) RETURNS api.http_response
LANGUAGE sql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
    SELECT internal.rest_dispatch(p_method, p_url, p_headers, p_content, true);
$$;

COMMENT ON FUNCTION api.rest_invoke(text, text, extensions.hstore, bytea) IS
    'REST gateway. Routes method+url to a registered handler, enforces auth and content negotiation, logs exchanges. SECURITY DEFINER.';

CREATE OR REPLACE FUNCTION api.rest_invoke(
    p_method text,
    p_url text,
    p_headers extensions.hstore,
    p_content jsonb
) RETURNS api.http_response
LANGUAGE sql
SET search_path = api, extensions, common, pg_temp
AS $$
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
LANGUAGE sql
SET search_path = api, extensions, common, pg_temp
AS $$
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
SET search_path = api, common, pg_temp
AS $$
    SELECT handler_object_id FROM api.rpc_route WHERE method_name = p_method_name;
$$;

COMMENT ON FUNCTION api.rpc_resolve(text) IS
    'Resolves an RPC method name to its handler UUID. Returns NULL if not registered.';

-- ============================================================================
-- RPC Gateway
-- ============================================================================

-- A JSON-RPC error envelope at an explicit HTTP status; data only when given.
CREATE OR REPLACE FUNCTION internal.rpc_error(p_status int, p_code int, p_message text, p_data jsonb, p_id jsonb)
RETURNS api.http_response
LANGUAGE sql IMMUTABLE AS $$
    SELECT api.json_response(p_status, jsonb_build_object(
        'jsonrpc', '2.0',
        'error', jsonb_strip_nulls(jsonb_build_object('code', p_code, 'message', p_message, 'data', p_data)),
        'id', p_id));
$$;

CREATE OR REPLACE FUNCTION internal.rpc_dispatch(
    p_route_id uuid,
    p_headers extensions.hstore,
    p_content bytea,
    p_context_from_headers boolean
) RETURNS api.http_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
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
    v_denial text;
    v_content_type text;
    v_schema jsonb;
    v_params jsonb;
    v_violation text;
    v_body jsonb;
BEGIN
    v_start_time := clock_timestamp();
    p_headers := internal.lowercase_header_keys(p_headers);
    RAISE DEBUG 'rpc_invoke: route_id=%', p_route_id;
    IF p_headers IS NULL THEN
        RETURN internal.finalize_error(
            internal.rpc_error(400, -32600, 'Invalid Request: header names repeat with different case', NULL, NULL), v_start_time);
    END IF;

    -- JSON-RPC 2.0: an unparseable body is -32700 Parse error, and the handler
    -- must not run, or a malformed request would look like a well-formed one
    -- with a null id.
    BEGIN
        v_body := api.content_json(p_content);
        IF v_body IS NULL AND length(p_content) = 0 THEN
            RAISE EXCEPTION 'empty body';
        END IF;
        v_json_id := CASE WHEN jsonb_typeof(v_body->'id') IN ('string', 'number', 'null') THEN v_body->'id' END;
    EXCEPTION WHEN OTHERS THEN
        RAISE DEBUG 'rpc_invoke: unparseable request body: %', SQLERRM;
        RETURN internal.finalize_error(
            api.jsonrpc_error(-32700, 'Parse error', NULL::jsonb), v_start_time);
    END;

    -- A batch, an empty body, a body without a string method or with an id
    -- that is not a string, number or null is no JSON-RPC 2.0 request:
    -- -32600 before the lookup, which a gateway resolving no method would
    -- otherwise answer -32601.
    IF jsonb_typeof(v_body) IS DISTINCT FROM 'object'
       OR v_body->'jsonrpc' IS DISTINCT FROM '"2.0"'::jsonb
       OR jsonb_typeof(v_body->'method') IS DISTINCT FROM 'string'
       OR (v_body ? 'id' AND jsonb_typeof(v_body->'id') NOT IN ('string', 'number', 'null')) THEN
        RETURN internal.finalize_error(internal.rpc_error(400, -32600,
            'Invalid Request: expected a JSON-RPC 2.0 request object', NULL, v_json_id), v_start_time);
    END IF;

    IF NOT (v_body ? 'id') THEN
        v_response := internal.finalize_error(ROW(202, ''::extensions.hstore, ''::bytea)::api.http_response, v_start_time);
        v_response.headers := v_response.headers - 'content-type'::text;
        RETURN v_response;
    END IF;

    SELECT h.handler_exec_sql, h.object_id, h.access,
           h.response_headers, h.output_json_schema, h.min_transaction_isolation, h.read_only,
           h.input_json_schema, h.accepts, h.produces,
           r.method_name, r.auto_log
    INTO v_handler
    FROM api.handler h
    JOIN api.rpc_route r ON r.handler_object_id = h.object_id
    WHERE h.object_id = p_route_id AND h.handler_type = 'rpc' AND h.deleted_at IS NULL;

    IF v_handler.handler_exec_sql IS NULL THEN
        RAISE DEBUG 'rpc_invoke: Method not found';
        RETURN internal.finalize_error(
            api.jsonrpc_error(-32601, 'Method not found', v_json_id), v_start_time);
    END IF;

    RAISE DEBUG 'rpc_invoke: Matched method %', v_handler.method_name;

    -- A request for another method is not this route's request: refused
    -- before any session resolves or the body runs.
    IF v_body->'method' IS DISTINCT FROM to_jsonb(v_handler.method_name) THEN
        RETURN internal.finalize_error(internal.rpc_error(400, -32600,
            format('Invalid Request: expected a JSON-RPC 2.0 request object for method %s', v_handler.method_name),
            NULL, v_json_id), v_start_time);
    END IF;

    -- Resolve the sessions first, then gate on what they resolved to, so an
    -- unknown, expired or revoked handle fails closed. Replay has set the
    -- logged context already.
    IF p_context_from_headers THEN
        PERFORM api.set_auth_context(p_headers);
    END IF;

    -- The access decision -- see rest_dispatch.
    v_denial := internal.current_access_denial(v_handler.object_id, v_handler.access);
    IF v_denial IS NOT NULL THEN
        RAISE DEBUG 'rpc_invoke: access denied (%)', v_denial;
        PERFORM internal.log_access_denial(v_handler.object_id, v_denial, 'RPC', v_handler.method_name);
        IF v_denial = 'unauthenticated' THEN
            RETURN internal.finalize_error(
                api.jsonrpc_error(-32001, 'Authentication required', v_json_id), v_start_time,
                extensions.hstore('www-authenticate', api.www_authenticate_challenge()));
        END IF;
        RETURN internal.finalize_error(internal.rpc_error(403, -32003,
            CASE v_denial WHEN 'organization_required' THEN 'This operation needs an active organization'
                          ELSE 'You do not have access to this operation' END,
            jsonb_build_object('code', CASE v_denial WHEN 'organization_required'
                                           THEN 'pgmi.organization_required' ELSE 'pgmi.forbidden' END),
            v_json_id), v_start_time);
    END IF;

    -- Enforce the route's transaction isolation floor (see rest_dispatch). The
    -- precise HTTP status (428) rides on the response while the JSON-RPC error
    -- stays in its correct class; the machine token is carried in error.data.code.
    v_iso_shortfall := internal.transaction_isolation_shortfall(v_handler.min_transaction_isolation);
    IF v_iso_shortfall IS NOT NULL THEN
        RAISE DEBUG 'rpc_invoke: isolation too weak (need %, have %)',
            v_handler.min_transaction_isolation, v_iso_shortfall;
        RETURN internal.finalize_error(internal.rpc_error(428, -32600,
            format('Route requires %s isolation but current transaction uses %s.',
                   v_handler.min_transaction_isolation, v_iso_shortfall),
            jsonb_build_object('code', 'pgmi.transaction_isolation_too_weak',
                               'requiredIsolation', v_handler.min_transaction_isolation),
            v_json_id), v_start_time);
    END IF;

    -- Enforce the route's read-only declaration (see rest_dispatch).
    IF v_handler.read_only AND NOT internal.transaction_is_read_only() THEN
        RAISE DEBUG 'rpc_invoke: read-only route in a read-write transaction';
        RETURN internal.finalize_error(internal.rpc_error(428, -32600,
            'Route requires a READ ONLY transaction but the current transaction is read-write.',
            jsonb_build_object('code', 'pgmi.transaction_read_only_required', 'requiredReadOnly', true),
            v_json_id), v_start_time);
    END IF;

    -- The declared accepts, produces and inputSchema, as rest_dispatch enforces
    -- them; the status rides on the response, the JSON-RPC code stays in its
    -- class. The inputSchema describes the params member.
    v_content_type := btrim(split_part(COALESCE(p_headers->'content-type', ''), ';', 1));
    IF v_content_type <> ''
       AND NOT api.accept_matches(array_to_string(v_handler.accepts, ', '), ARRAY[v_content_type]) THEN
        RETURN internal.finalize_error(internal.rpc_error(415, -32600,
            format('Supported request content types: %s', array_to_string(v_handler.accepts, ', ')),
            jsonb_build_object('code', 'pgmi.unsupported_media_type'), v_json_id), v_start_time);
    END IF;

    IF NOT api.accept_matches(p_headers->'accept', v_handler.produces) THEN
        RETURN internal.finalize_error(internal.rpc_error(406, -32600,
            format('Supported content types: %s', array_to_string(v_handler.produces, ', ')),
            jsonb_build_object('code', 'pgmi.not_acceptable'), v_json_id), v_start_time);
    END IF;

    IF v_handler.input_json_schema IS NOT NULL THEN
        v_schema := internal.resolved_schema(v_handler.input_json_schema::jsonb);
        v_params := COALESCE(api.content_json(p_content)->'params', 'null'::jsonb);
        v_violation := internal.json_schema_errors(v_schema, v_params);
        IF v_violation IS NOT NULL THEN
            RETURN internal.finalize_error(internal.rpc_error(400, -32602, 'Invalid params: ' || v_violation,
                jsonb_build_object('code', 'pgmi.invalid_body',
                                   'invalid-params', internal.json_schema_invalid_params(v_schema, v_params)),
                v_json_id), v_start_time);
        END IF;
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

        IF (v_response).status_code IS NULL OR (v_response).status_code NOT BETWEEN 100 AND 599 THEN
            RAISE EXCEPTION 'handler % answered status %', v_handler.method_name, coalesce((v_response).status_code::text, 'NULL');
        END IF;

        -- An error answer changes nothing (see rest_dispatch).
        IF (v_response).status_code >= 400 THEN
            RAISE EXCEPTION USING ERRCODE = 'PGRBK';
        END IF;

    EXCEPTION
    WHEN SQLSTATE 'PGRBK' THEN
        NULL;

    -- Propagate the retryable class untouched — see rest_dispatch for why catching
    -- it both hides the retry signal and can commit a lost write.
    WHEN serialization_failure OR deadlock_detected THEN
        RAISE;

    WHEN OTHERS THEN
    DECLARE
        v_sqlstate text := SQLSTATE;
        v_problem record;
    BEGIN
        RAISE DEBUG 'rpc_invoke: Handler exception: %', SQLERRM;
        v_execution_ms := extract(epoch FROM (clock_timestamp() - v_start_time)) * 1000;
        -- The precise status rides on the HTTP response; the JSON-RPC code stays
        -- in its class: -32602 for client-caused errors, -32603 for the rest.
        v_problem := internal.sqlstate_problem(v_sqlstate, NULL, v_handler.read_only);

        -- Logged copy keeps SQLSTATE + truncated SQLERRM (see rest_dispatch for
        -- the truncation rationale).
        v_response := internal.rpc_error(v_problem.status, v_problem.rpc_code,
            'sqlstate=' || v_sqlstate || ' detail=' || LEFT(SQLERRM, 200), NULL, v_json_id);
        v_response.headers := extensions.hstore(ARRAY[
            'content-type', 'application/json; charset=utf-8',
            'content-length', COALESCE(octet_length((v_response).content), 0)::text,
            'x-execution-time-ms', v_execution_ms::text,
            'x-pgmi-catalog-version', api.catalog_version(),
            'x-error-sqlstate', v_sqlstate
        ]);
        -- autoLog=false must hold on the failure path too (see rest_dispatch).
        IF v_handler.auto_log AND NOT internal.transaction_is_read_only() THEN
            INSERT INTO api.rpc_exchange (handler_object_id, context, request, response, completed_at)
            VALUES (v_handler.object_id, internal.request_log_context(),
                    internal.loggable(v_request), internal.loggable(v_response), now());
        END IF;

        -- x-error-sqlstate stays on the logged copy only.
        RETURN internal.finalize_error(internal.rpc_error(v_problem.status, v_problem.rpc_code,
            CASE WHEN v_problem.rpc_code = -32603 THEN 'Internal error' ELSE v_problem.detail END,
            CASE WHEN v_problem.code IS NOT NULL THEN jsonb_build_object('code', v_problem.code) END,
            v_json_id), v_start_time);
    END;
    END;

    -- Exchange logging is a write; skip in a READ ONLY transaction (see rest_dispatch).
    IF v_handler.auto_log AND NOT internal.transaction_is_read_only() THEN
        INSERT INTO api.rpc_exchange (handler_object_id, context, request, response, completed_at)
        VALUES (v_handler.object_id, internal.request_log_context(),
                internal.loggable(v_request), internal.loggable(v_response), now());
    END IF;

    RETURN v_response;
END;
$$;

COMMENT ON FUNCTION internal.rpc_dispatch(uuid, extensions.hstore, bytea, boolean) IS
    'RPC dispatch behind api.rpc_invoke (context from the headers) and api.replay_exchange (the logged context, already set).';

CREATE OR REPLACE FUNCTION api.rpc_invoke(
    p_route_id uuid,
    p_headers extensions.hstore DEFAULT ''::extensions.hstore,
    p_content bytea DEFAULT NULL
) RETURNS api.http_response
LANGUAGE sql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
    SELECT internal.rpc_dispatch(p_route_id, p_headers, p_content, true);
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
-- methods (the dispatcher ELSE branch). A protected name asked for without a
-- resolved identity gets the same -32602, so it cannot be probed for.

DROP FUNCTION IF EXISTS internal.mcp_argument_errors(jsonb, jsonb);

-- A schema node with its references followed, up to 8 hops: "#/$defs/<name>"
-- is the root's definition, "#/components/schemas/<name>" the registered
-- component. Any other node is itself; a reference that names nothing is NULL.
CREATE OR REPLACE FUNCTION internal.schema_node(p_schema jsonb, p_root jsonb)
RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
DECLARE
    v_node jsonb := p_schema;
    v_ref  text;
BEGIN
    FOR i IN 1..8 LOOP
        v_ref := CASE WHEN jsonb_typeof(v_node) = 'object' THEN v_node->>'$ref' END;
        IF v_ref LIKE '#/$defs/%' THEN
            v_node := p_root->'$defs'->substr(v_ref, 9);
        ELSIF v_ref LIKE '#/components/schemas/%' THEN
            v_node := (SELECT c.schema::jsonb FROM api.schema_component c WHERE c.name = substr(v_ref, 22));
        ELSE
            EXIT;
        END IF;
    END LOOP;
    RETURN v_node;
END;
$$;

-- The problems a declared JSON Schema finds in a value, checked by both the
-- MCP tool gateway (arguments, structured results) and the REST gateway
-- (request bodies): the declared type (a name or a list of names), enum,
-- required keys, additionalProperties: false, bounds, sizes, uniqueItems and
-- the boolean schema false, and the same again through properties, array
-- items and #/$defs or #/components/schemas references at any depth.
-- Registration refuses every other keyword, so a published schema is an
-- enforced one; format and pattern stay hints for the body. Each problem
-- names its path (a.b, tags[0]); a root of the wrong type names none. No
-- rows when there are none.
DROP FUNCTION IF EXISTS internal.json_schema_violations(jsonb, jsonb);

CREATE OR REPLACE FUNCTION internal.json_schema_violations(
    p_schema jsonb, p_value jsonb, p_root jsonb DEFAULT NULL, p_path text DEFAULT NULL)
RETURNS TABLE (name text, reason text, message text)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    v_root   jsonb := COALESCE(p_root, p_schema);
    v_schema jsonb := internal.schema_node(p_schema, COALESCE(p_root, p_schema));
    v_types  text[];
    v_key    text;
    v_item   jsonb;
    v_index  bigint;
    v_name   text := COALESCE(p_path, 'the body');
    v_number numeric;
    v_length bigint;
BEGIN
    IF v_schema = 'false'::jsonb THEN
        RETURN QUERY SELECT p_path, 'invalid'::text, format('%s is not allowed', v_name);
        RETURN;
    END IF;
    IF jsonb_typeof(v_schema) IS DISTINCT FROM 'object' THEN
        RETURN;
    END IF;

    v_types := CASE jsonb_typeof(v_schema->'type')
        WHEN 'string' THEN ARRAY[v_schema->>'type']
        WHEN 'array'  THEN ARRAY(SELECT jsonb_array_elements_text(v_schema->'type'))
    END;
    IF v_types IS NOT NULL AND NOT EXISTS (
        SELECT FROM unnest(v_types) t
        WHERE CASE t
            WHEN 'integer' THEN jsonb_typeof(p_value) = 'number' AND (p_value #>> '{}')::numeric = trunc((p_value #>> '{}')::numeric)
            WHEN 'number'  THEN jsonb_typeof(p_value) = 'number'
            ELSE jsonb_typeof(p_value) = t
        END) THEN
        RETURN QUERY SELECT p_path, 'invalid'::text,
            CASE WHEN p_path IS NULL THEN format('the body must be a JSON %s', array_to_string(v_types, ' or '))
                 ELSE format('%s must be %s', p_path, array_to_string(v_types, ' or ')) END;
        RETURN;
    END IF;

    IF jsonb_typeof(v_schema->'enum') = 'array'
       AND NOT EXISTS (SELECT FROM jsonb_array_elements(v_schema->'enum') e WHERE e = p_value) THEN
        RETURN QUERY SELECT p_path, 'invalid'::text,
            format('%s must be one of %s', COALESCE(p_path, 'the body'),
                   (SELECT string_agg(e::text, ', ') FROM jsonb_array_elements(v_schema->'enum') e));
    END IF;

    IF v_schema ? 'const' AND v_schema->'const' IS DISTINCT FROM p_value THEN
        RETURN QUERY SELECT p_path, 'invalid'::text, format('%s must be %s', v_name, v_schema->'const');
    END IF;

    IF jsonb_typeof(p_value) = 'number' THEN
        v_number := (p_value #>> '{}')::numeric;
        RETURN QUERY
        SELECT p_path, 'out_of_range'::text, format('%s must be %s %s', v_name, b.rule, v_schema->>b.keyword)
        FROM (VALUES ('minimum', 'at least'), ('maximum', 'at most'),
                     ('exclusiveMinimum', 'greater than'), ('exclusiveMaximum', 'less than')) b(keyword, rule)
        WHERE jsonb_typeof(v_schema->b.keyword) = 'number'
          AND CASE b.keyword
                WHEN 'minimum'          THEN v_number <  (v_schema->>b.keyword)::numeric
                WHEN 'maximum'          THEN v_number >  (v_schema->>b.keyword)::numeric
                WHEN 'exclusiveMinimum' THEN v_number <= (v_schema->>b.keyword)::numeric
                ELSE                         v_number >= (v_schema->>b.keyword)::numeric
              END;
    ELSIF jsonb_typeof(p_value) IN ('string', 'array') THEN
        v_length := CASE jsonb_typeof(p_value) WHEN 'string' THEN char_length(p_value #>> '{}')
                                               ELSE jsonb_array_length(p_value) END;
        RETURN QUERY
        SELECT p_path, 'out_of_range'::text,
               format('%s must have %s %s %s', v_name, b.rule, v_schema->>b.keyword,
                      CASE jsonb_typeof(p_value) WHEN 'string' THEN 'characters' ELSE 'items' END)
        FROM (VALUES ('minLength', 'string', 'at least'), ('maxLength', 'string', 'at most'),
                     ('minItems', 'array', 'at least'), ('maxItems', 'array', 'at most')) b(keyword, kind, rule)
        WHERE b.kind = jsonb_typeof(p_value)
          AND jsonb_typeof(v_schema->b.keyword) = 'number'
          AND CASE WHEN b.rule = 'at least' THEN v_length < (v_schema->>b.keyword)::numeric
                   ELSE v_length > (v_schema->>b.keyword)::numeric END;
        IF jsonb_typeof(p_value) = 'array' AND v_schema->'uniqueItems' = 'true'::jsonb
           AND (SELECT count(DISTINCT e) FROM jsonb_array_elements(p_value) e) < v_length THEN
            RETURN QUERY SELECT p_path, 'invalid'::text, format('%s must not repeat an item', v_name);
        END IF;
    END IF;

    IF jsonb_typeof(p_value) = 'object' THEN
        RETURN QUERY
        SELECT concat_ws('.', p_path, k), 'required'::text, format('%s is required', concat_ws('.', p_path, k))
        FROM jsonb_array_elements_text(
            CASE WHEN jsonb_typeof(v_schema->'required') = 'array' THEN v_schema->'required' ELSE '[]' END) k
        WHERE NOT p_value ? k;

        FOR v_key IN SELECT jsonb_object_keys(p_value) ORDER BY 1 LOOP
            IF jsonb_typeof(v_schema->'properties') = 'object' AND v_schema->'properties' ? v_key THEN
                RETURN QUERY SELECT * FROM internal.json_schema_violations(
                    v_schema->'properties'->v_key, p_value->v_key, v_root, concat_ws('.', p_path, v_key));
            ELSIF v_schema->'additionalProperties' = 'false'::jsonb THEN
                RETURN QUERY SELECT concat_ws('.', p_path, v_key), 'unknown'::text,
                    format('%s is not allowed', concat_ws('.', p_path, v_key));
            END IF;
        END LOOP;
    ELSIF jsonb_typeof(p_value) = 'array' AND jsonb_typeof(v_schema->'items') IN ('object', 'boolean') THEN
        FOR v_item, v_index IN SELECT e, n - 1 FROM jsonb_array_elements(p_value) WITH ORDINALITY a(e, n) LOOP
            RETURN QUERY SELECT * FROM internal.json_schema_violations(
                v_schema->'items', v_item, v_root, COALESCE(p_path, '') || '[' || v_index || ']');
        END LOOP;
    END IF;
END;
$$;

-- A schema whose root is a reference is checked as what it names, or the
-- check would pass anything.
CREATE OR REPLACE FUNCTION internal.resolved_schema(p_schema jsonb)
RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT internal.schema_node(p_schema, p_schema)
$$;

CREATE OR REPLACE FUNCTION internal.json_schema_errors(p_schema jsonb, p_arguments jsonb)
RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT string_agg(message, '; ' ORDER BY message)
    FROM internal.json_schema_violations(p_schema, p_arguments)
$$;

-- The same problems as RFC 9457 invalid-params. A root of the wrong type names
-- no parameter, so it yields none; the detail says it.
CREATE OR REPLACE FUNCTION internal.json_schema_invalid_params(p_schema jsonb, p_value jsonb)
RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_agg(api.invalid_param(name, reason) ORDER BY name)
    FROM internal.json_schema_violations(p_schema, p_value)
    WHERE name IS NOT NULL
$$;

-- Whether a schema node declares a JSON type: its "type" is that name or a
-- list naming it; without a type, "string" when its enum holds a string.
CREATE OR REPLACE FUNCTION internal.mcp_schema_takes(p_schema jsonb, p_type text)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE(CASE jsonb_typeof(p_schema->'type')
               WHEN 'string' THEN p_schema->>'type' = p_type
               WHEN 'array'  THEN p_schema->'type' ? p_type
               ELSE p_type = 'string' AND jsonb_path_exists(p_schema, '$.enum[*] ? (@.type() == "string")')
           END, false)
$$;

-- A tool's arguments read leniently against its inputSchema, where the intent
-- is unambiguous: a lone string where only an array of strings is declared
-- becomes a one-item array, and an integral number (10.0) where only an
-- integer is declared becomes that integer. Through properties, array items
-- and #/$defs references; everything else stays as sent, for the schema
-- check and the tool to refuse.
CREATE OR REPLACE FUNCTION internal.mcp_lenient_arguments(p_value jsonb, p_schema jsonb, p_root jsonb DEFAULT NULL)
RETURNS jsonb
LANGUAGE plpgsql STABLE AS $$
DECLARE
    v_root   jsonb := COALESCE(p_root, p_schema);
    v_schema jsonb := internal.schema_node(p_schema, v_root);
    v_items  jsonb;
    v_number numeric;
BEGIN
    IF p_value IS NULL OR jsonb_typeof(v_schema) IS DISTINCT FROM 'object' THEN
        RETURN p_value;
    END IF;
    v_items := internal.schema_node(v_schema->'items', v_root);
    CASE jsonb_typeof(p_value)
    WHEN 'number' THEN
        v_number := (p_value #>> '{}')::numeric;
        IF internal.mcp_schema_takes(v_schema, 'integer') AND NOT internal.mcp_schema_takes(v_schema, 'number')
           AND v_number = trunc(v_number) THEN
            RETURN to_jsonb(trunc(v_number));
        END IF;
    WHEN 'string' THEN
        IF internal.mcp_schema_takes(v_schema, 'array') AND NOT internal.mcp_schema_takes(v_schema, 'string')
           AND internal.mcp_schema_takes(v_items, 'string') THEN
            RETURN jsonb_build_array(p_value);
        END IF;
    WHEN 'array' THEN
        IF jsonb_typeof(v_items) = 'object' THEN
            RETURN (SELECT COALESCE(jsonb_agg(internal.mcp_lenient_arguments(a.e, v_items, v_root) ORDER BY a.n), '[]'::jsonb)
                    FROM jsonb_array_elements(p_value) WITH ORDINALITY a(e, n));
        END IF;
    WHEN 'object' THEN
        IF jsonb_typeof(v_schema->'properties') = 'object' THEN
            RETURN (SELECT COALESCE(jsonb_object_agg(o.k, CASE WHEN v_schema->'properties' ? o.k
                                                             THEN internal.mcp_lenient_arguments(o.v, v_schema->'properties'->o.k, v_root)
                                                             ELSE o.v END), '{}'::jsonb)
                    FROM jsonb_each(p_value) o(k, v));
        END IF;
    ELSE
        NULL;
    END CASE;
    RETURN p_value;
END;
$$;

COMMENT ON FUNCTION internal.mcp_lenient_arguments(jsonb, jsonb, jsonb) IS
    'A tool''s arguments read leniently against its inputSchema (properties, array items, #/$defs): a lone string where only an array of strings is declared becomes a one-item array, an integral number where only an integer is declared becomes that integer; everything else as sent. internal.mcp_dispatch applies it before the schema check.';

-- A handler's envelope as the client must see it. The response id is the
-- request's, whatever the handler passed (JSON-RPC 2.0 section 5). A NULL
-- envelope would read as a notification and never be answered. A tools/call
-- result needs a content array (CallToolResult), and anything that is neither
-- a result object nor an error object is no answer at all. Success results
-- are rebuilt through api.mcp_success, so they carry resultType and serverInfo
-- however the handler built them.
DROP FUNCTION IF EXISTS internal.mcp_checked_envelope(text, jsonb, jsonb);

CREATE OR REPLACE FUNCTION internal.mcp_checked_envelope(
    p_mcp_type text, p_envelope jsonb, p_request_id jsonb, p_name_or_uri text)
RETURNS api.mcp_response
LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN jsonb_typeof(p_envelope->'error') = 'object'
            THEN ROW(jsonb_build_object('jsonrpc', '2.0', 'id', p_request_id, 'error', p_envelope->'error'))::api.mcp_response
        WHEN p_mcp_type = 'resource' AND p_envelope->'result'->'contents' = '[]'::jsonb
            THEN api.mcp_resource_not_found(p_name_or_uri, p_request_id)
        WHEN p_mcp_type = 'resource' AND jsonb_typeof(p_envelope->'result') = 'object'
             AND COALESCE(p_envelope->'result'->>'resultType', 'complete') = 'complete'
            THEN api.mcp_success(api.mcp_cache_policy() || (p_envelope->'result'), p_request_id)
        WHEN jsonb_typeof(p_envelope->'result') = 'object'
             AND (p_mcp_type <> 'tool' OR jsonb_typeof(p_envelope->'result'->'content') = 'array')
            THEN api.mcp_success(p_envelope->'result', p_request_id)
        WHEN p_mcp_type = 'tool'
            THEN api.mcp_tool_error(CASE WHEN p_envelope IS NULL THEN 'Tool handler returned no result'
                                         ELSE 'Tool handler returned a malformed result' END, p_request_id)
        ELSE api.mcp_error(-32603, CASE WHEN p_envelope IS NULL THEN 'Handler returned no result'
                                        ELSE 'Handler returned a malformed result' END, p_request_id)
    END
$$;

-- The handler an MCP name or URI routes to, shared by the dispatcher and
-- api.mcp_request_policy so they cannot disagree. A tool or prompt matches by
-- name; a resource by URI, the template with the most literal characters
-- first (a placeholder's name never counts), with the name as a stable
-- tiebreak.
CREATE OR REPLACE FUNCTION internal.mcp_route_lookup(p_mcp_type text, p_key text)
RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT r.handler_object_id
    FROM api.mcp_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
    WHERE r.mcp_type = p_mcp_type
      AND CASE WHEN p_mcp_type = 'resource' THEN p_key ~ r.uri_regexp ELSE r.mcp_name = p_key END
    ORDER BY length(regexp_replace(r.uri_template, '\{[^}]+\}', '', 'g')) DESC NULLS LAST, r.mcp_name
    LIMIT 1;
$$;

-- Shared invocation path for the three MCP entry points. Route lookup and
-- request shape differ per type; everything security-relevant — auth context,
-- auth gate, isolation floor, exchange logging, error sanitization — lives
-- here exactly once. Runs SECURITY INVOKER inside the SECURITY DEFINER
-- wrappers below, inheriting their search_path and owner privileges.
DROP FUNCTION IF EXISTS internal.mcp_dispatch(text, text, jsonb, jsonb, jsonb);

CREATE OR REPLACE FUNCTION internal.mcp_dispatch(
    p_mcp_type text,
    p_name_or_uri text,
    p_arguments jsonb,
    p_context jsonb,
    p_request_id jsonb,
    p_resolve_caller boolean DEFAULT true
) RETURNS api.mcp_response
LANGUAGE plpgsql AS $$
DECLARE
    v_request api.mcp_request;
    v_response api.mcp_response;
    v_handler record;
    v_iso_shortfall text;
    v_denial text;
    v_invalid text;
    v_refusal jsonb;
BEGIN
    RAISE DEBUG 'mcp_dispatch: % %', p_mcp_type, p_name_or_uri;

    IF p_name_or_uri IS NULL OR p_name_or_uri = '' THEN
        RETURN api.mcp_error(-32602,
            format('Invalid params: %s', CASE WHEN p_mcp_type = 'resource' THEN 'params.uri (string) is required'
                                             ELSE 'params.name (string) is required' END),
            p_request_id);
    END IF;
    IF p_mcp_type <> 'resource' AND jsonb_typeof(p_arguments) IS DISTINCT FROM 'object' THEN
        RETURN api.mcp_error(-32602, 'Invalid params: params.arguments must be an object', p_request_id);
    END IF;

    -- Replay has applied the logged context; the logged request holds no
    -- handle to resolve.
    IF p_resolve_caller THEN
        v_refusal := internal.resolve_mcp_caller(p_context);
        IF v_refusal IS NOT NULL THEN
            RETURN internal.mcp_caller_refusal(v_refusal, p_request_id);
        END IF;
    END IF;

    SELECT h.handler_exec_sql, h.object_id, h.access, r.mcp_name, h.min_transaction_isolation, h.read_only,
           r.input_schema, r.arguments, h.output_json_schema::jsonb AS output_schema
    INTO v_handler
    FROM api.handler h
    JOIN api.mcp_route r ON r.handler_object_id = h.object_id
    WHERE h.object_id = internal.mcp_route_lookup(p_mcp_type, p_name_or_uri);

    -- An anonymous caller gets one answer whether the name is unknown or
    -- protected: the listings hide what it cannot call, and a distinct code
    -- would confirm that a hidden name exists. The message still says what to try.
    IF v_handler.handler_exec_sql IS NULL THEN
        RAISE DEBUG 'mcp_dispatch: % not found', p_mcp_type;
        RETURN api.mcp_error(-32602,
            initcap(p_mcp_type) || ' not found: ' || p_name_or_uri
                || CASE WHEN api.current_principal_id() IS NULL THEN ' (or it requires authentication)' ELSE '' END,
            p_request_id,
            CASE WHEN p_mcp_type = 'resource' THEN jsonb_build_object('uri', p_name_or_uri) END);
    END IF;

    v_denial := internal.current_access_denial(v_handler.object_id, v_handler.access);
    IF v_denial IS NOT NULL THEN
        RAISE DEBUG 'mcp_dispatch: access denied (%)', v_denial;
        PERFORM internal.log_access_denial(v_handler.object_id, v_denial, 'MCP', v_handler.mcp_name);
        RETURN CASE v_denial
            WHEN 'unauthenticated' THEN
                api.mcp_error(-32602,
                    initcap(p_mcp_type) || ' not found: ' || p_name_or_uri || ' (or it requires authentication)',
                    p_request_id,
                    CASE WHEN p_mcp_type = 'resource' THEN jsonb_build_object('uri', p_name_or_uri) END)
            WHEN 'organization_required' THEN
                api.mcp_error(-31003, 'This operation needs an active organization', p_request_id,
                              jsonb_build_object('code', 'pgmi.organization_required'))
            ELSE
                api.mcp_error(-31003, 'You do not have access to this operation', p_request_id,
                              jsonb_build_object('code', 'pgmi.forbidden'))
        END;
    END IF;

    RAISE DEBUG 'mcp_dispatch: Matched %', v_handler.mcp_name;

    -- Enforce the route's transaction isolation floor (see rest_dispatch).
    v_iso_shortfall := internal.transaction_isolation_shortfall(v_handler.min_transaction_isolation);
    IF v_iso_shortfall IS NOT NULL THEN
        RETURN api.mcp_error(
            -32603,
            format('Route requires %s isolation but current transaction uses %s.',
                   v_handler.min_transaction_isolation, v_iso_shortfall),
            p_request_id,
            jsonb_build_object('code', 'pgmi.transaction_isolation_too_weak',
                               'requiredIsolation', v_handler.min_transaction_isolation)
        );
    END IF;

    -- Tool arguments are checked against the declared inputSchema before the
    -- handler runs, at every depth: type, enum, const, required,
    -- additionalProperties false, and the size and range keywords. format and
    -- pattern are hints the body checks.
    -- The spec says servers MUST validate tool inputs; the failure is an
    -- isError result the model can read and act on, not a protocol error.
    IF p_mcp_type = 'tool' THEN
        p_arguments := internal.mcp_lenient_arguments(p_arguments, v_handler.input_schema::jsonb);
        v_invalid := internal.json_schema_errors(v_handler.input_schema::jsonb, p_arguments);
        IF v_invalid IS NOT NULL THEN
            RETURN api.mcp_tool_error('Invalid arguments: ' || v_invalid, p_request_id);
        END IF;
    ELSIF p_mcp_type = 'prompt' THEN
        SELECT string_agg(a->>'name', ', ' ORDER BY a->>'name') INTO v_invalid
        FROM jsonb_array_elements(COALESCE(v_handler.arguments, '[]'::jsonb)) a
        WHERE COALESCE((a->>'required')::boolean, false) AND NOT p_arguments ? (a->>'name');
        IF v_invalid IS NOT NULL THEN
            RETURN api.mcp_error(-32602, 'Invalid params: missing required argument(s): ' || v_invalid, p_request_id);
        END IF;
        IF EXISTS (SELECT FROM jsonb_each(p_arguments) a WHERE jsonb_typeof(a.value) <> 'string') THEN
            RETURN api.mcp_error(-32602, 'Invalid params: prompt arguments must be strings', p_request_id);
        END IF;
    END IF;

    v_request := CASE WHEN p_mcp_type = 'resource'
        THEN (NULL, p_name_or_uri, p_context, p_request_id)::api.mcp_request
        ELSE (p_arguments, NULL, p_context, p_request_id)::api.mcp_request
    END;

    BEGIN
        RAISE DEBUG 'mcp_dispatch: Invoking handler %', v_handler.object_id;
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        v_response := internal.mcp_checked_envelope(p_mcp_type, (v_response).envelope, p_request_id, p_name_or_uri);

        -- A tool's success MUST conform to its outputSchema (MCP, Tools ->
        -- Output Schema). The log keeps why; the model gets the sanitized result.
        IF p_mcp_type = 'tool' AND v_handler.output_schema IS NOT NULL
           AND (v_response).envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb THEN
            v_invalid := internal.json_schema_errors(v_handler.output_schema,
                                                     (v_response).envelope->'result'->'structuredContent');
            IF v_invalid IS NOT NULL THEN
                v_response := api.mcp_tool_error('outputSchema: ' || v_invalid, p_request_id);
            END IF;
        END IF;

        -- An error answer changes nothing (see rest_dispatch): a protocol
        -- error, a tool's isError result, or a result the checks above rejected.
        IF (v_response).envelope ? 'error'
           OR (v_response).envelope->'result'->'isError' = 'true'::jsonb THEN
            RAISE EXCEPTION USING ERRCODE = 'PGRBK';
        END IF;

    EXCEPTION
    WHEN SQLSTATE 'PGRBK' THEN
        NULL;

    -- Propagate the retryable class untouched — see rest_dispatch for why catching
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
        -- keeps SQLSTATE + truncated SQLERRM (see rest_dispatch for the
        -- truncation rationale); the client gets a sanitized message.
        v_response := CASE WHEN p_mcp_type = 'tool'
            THEN api.mcp_tool_error('sqlstate=' || SQLSTATE || ' detail=' || LEFT(SQLERRM, 200), p_request_id)
            ELSE api.mcp_error(-32603, 'sqlstate=' || SQLSTATE || ' detail=' || LEFT(SQLERRM, 200), p_request_id)
        END;
        IF NOT internal.transaction_is_read_only() THEN
            INSERT INTO api.mcp_exchange (handler_object_id, context, mcp_type, mcp_name, request, response)
            VALUES (v_handler.object_id, internal.request_log_context(), p_mcp_type, v_handler.mcp_name,
                    internal.loggable(v_request), v_response);
        END IF;

        -- Bad input and constraint violations are the caller's to fix, so the
        -- model gets the same class message REST sends; SQLERRM never leaves
        -- the database, a handler's own RAISE text included.
        RETURN CASE
            WHEN p_mcp_type <> 'tool' THEN api.mcp_error(-32603, 'Internal error', p_request_id)
            WHEN (internal.sqlstate_problem(SQLSTATE, NULL, false)).status = 500
                THEN api.mcp_tool_error('Tool execution failed', p_request_id)
            ELSE api.mcp_tool_error((internal.sqlstate_problem(SQLSTATE, NULL, false)).detail, p_request_id)
        END;
    END;

    -- Exchange logging is a write; skip in a READ ONLY transaction (see rest_dispatch).
    IF NOT internal.transaction_is_read_only() THEN
        INSERT INTO api.mcp_exchange (handler_object_id, context, mcp_type, mcp_name, request, response)
        VALUES (v_handler.object_id, internal.request_log_context(), p_mcp_type, v_handler.mcp_name,
                internal.loggable(v_request), v_response);
    END IF;

    IF v_invalid IS NOT NULL THEN
        RETURN api.mcp_tool_error('Tool execution failed', p_request_id);
    END IF;
    RETURN v_response;
END;
$$;

COMMENT ON FUNCTION internal.mcp_dispatch(text, text, jsonb, jsonb, jsonb, boolean) IS
    'Shared MCP invocation path: route lookup, auth context, auth gate, isolation floor, handler EXECUTE, exchange logging, sanitized errors. Called by the api.mcp_* SECURITY DEFINER wrappers, and by api.replay_exchange with p_resolve_caller false (the logged context is applied already).';

DROP FUNCTION IF EXISTS api.mcp_call_tool(text, jsonb, jsonb, text);

CREATE OR REPLACE FUNCTION api.mcp_call_tool(
    p_name text,
    p_arguments jsonb,
    p_context jsonb DEFAULT NULL,
    p_request_id jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE sql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
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
SET search_path = api, internal, extensions, common, pg_temp
AS $$
    SELECT internal.mcp_dispatch('resource', p_uri, NULL, p_context, p_request_id);
$$;

COMMENT ON FUNCTION api.mcp_read_resource(text, jsonb, jsonb) IS
    'MCP resources/read. Matches URI against registered templates; the one with the most literal characters wins.';

DROP FUNCTION IF EXISTS api.mcp_get_prompt(text, jsonb, jsonb, text);

CREATE OR REPLACE FUNCTION api.mcp_get_prompt(
    p_name text,
    p_arguments jsonb,
    p_context jsonb DEFAULT NULL,
    p_request_id jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE sql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
    SELECT internal.mcp_dispatch('prompt', p_name, p_arguments, p_context, p_request_id);
$$;

COMMENT ON FUNCTION api.mcp_get_prompt(text, jsonb, jsonb, jsonb) IS
    'MCP prompts/get. Resolves prompt by name, applies auth context, returns expanded messages.';

-- ============================================================================
-- MCP Discovery Functions
-- ============================================================================
-- NO PAGINATION: each function returns the entire list in one call, ordered
-- bytewise (COLLATE "C") by name or URI, so two calls agree on the order. No
-- nextCursor is sent.
--
-- NO listChanged: the dispatcher adds ttlMs and cacheScope
-- (api.mcp_cache_policy()) and a client lists again when they run out.
--
-- TAGS placement: pgmi surfaces tags inside the spec-defined `_meta` object
-- (an extension slot reserved by MCP for server-specific data). Top-level
-- `tags` would be a spec violation under strict clients.
--
-- ACCESS FILTERING: each listing shows only what internal.current_access_denial
-- lets the caller call, the same decision mcp_dispatch makes. MCP allows either
-- hide-then-reject or expose-and-return-isError; pgmi hides, the idiomatic MCP UX.

-- An object without its own null members. jsonb_strip_nulls would also strip
-- the nulls inside a member, and a schema's "default": null or "const": null
-- is part of its meaning.
CREATE OR REPLACE FUNCTION internal.jsonb_without_null_members(p_object jsonb)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE(jsonb_object_agg(key, value), '{}'::jsonb)
    FROM jsonb_each(p_object)
    WHERE jsonb_typeof(value) <> 'null'
$$;


CREATE OR REPLACE FUNCTION api.mcp_list_tools(p_tags text[] DEFAULT NULL)
RETURNS jsonb
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = api, common, pg_temp
AS $$
BEGIN
    RETURN (
        WITH norm AS (
            -- NULL or empty array both mean "no tag filter"
            SELECT CASE WHEN p_tags IS NULL OR cardinality(p_tags) = 0
                        THEN NULL::text[]
                        ELSE p_tags
                   END AS tag_filter
        )
        SELECT jsonb_build_object('tools', COALESCE(jsonb_agg(
            internal.jsonb_without_null_members(
                jsonb_build_object(
                    'name', r.mcp_name,
                    'title', h.title,
                    'description', h.description,
                    'inputSchema', r.input_schema,
                    'outputSchema', h.output_json_schema::jsonb,
                    'annotations', r.annotations,
                    '_meta', jsonb_build_object('pgmi/effect', r.effect)
                             || CASE WHEN r.tags = '{}' THEN '{}'::jsonb
                                     ELSE jsonb_build_object('tags', to_jsonb(r.tags)) END
                )
            )
            ORDER BY r.mcp_name COLLATE "C"
        ), '[]'::jsonb))
        FROM api.mcp_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        CROSS JOIN norm
        WHERE r.mcp_type = 'tool'
          AND (norm.tag_filter IS NULL OR r.tags && norm.tag_filter)
          AND internal.current_access_denial(h.object_id, h.access) IS NULL
    );
END
$$;

COMMENT ON FUNCTION api.mcp_list_tools(text[]) IS
    'MCP tool discovery. Returns {"tools":[...]} with name, title, description, inputSchema, outputSchema, annotations (derived from the effect), _meta["pgmi/effect"] and _meta.tags (pgmi extensions). Lists only the tools internal.current_access_denial lets the caller call. Pass p_tags to filter by tag overlap (NULL or empty = no filter).';

CREATE OR REPLACE FUNCTION api.mcp_list_resources()
RETURNS jsonb
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = api, common, pg_temp
AS $$
BEGIN
    RETURN (
        -- resources/list returns concrete Resource objects only (required `uri`).
        -- Templated resources (RFC 6570 {placeholder}) are returned by the separate
        -- resources/templates/list method (api.mcp_list_resource_templates).
        SELECT jsonb_build_object('resources', COALESCE(jsonb_agg(
            internal.jsonb_without_null_members(
                jsonb_build_object(
                    'name', r.mcp_name,
                    'title', h.title,
                    'description', h.description,
                    'uri', r.uri_template,
                    'mimeType', r.mime_type
                )
            )
            ORDER BY r.uri_template COLLATE "C"
        ), '[]'::jsonb))
        FROM api.mcp_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.mcp_type = 'resource'
          AND r.uri_template !~ '\{[^}]+\}'
          AND internal.current_access_denial(h.object_id, h.access) IS NULL
    );
END
$$;

COMMENT ON FUNCTION api.mcp_list_resources() IS
    'MCP resources/list discovery. Emits only concrete Resource objects (static uri). Templated resources are served by resources/templates/list. Lists only the resources the caller may read.';

CREATE OR REPLACE FUNCTION api.mcp_list_resource_templates()
RETURNS jsonb
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = api, common, pg_temp
AS $$
BEGIN
    RETURN (
        -- resources/templates/list returns ResourceTemplate objects (required
        -- `uriTemplate`) for resources whose uri carries an RFC 6570 placeholder.
        SELECT jsonb_build_object('resourceTemplates', COALESCE(jsonb_agg(
            internal.jsonb_without_null_members(
                jsonb_build_object(
                    'name', r.mcp_name,
                    'title', h.title,
                    'description', h.description,
                    'uriTemplate', r.uri_template,
                    'mimeType', r.mime_type
                )
            )
            ORDER BY r.uri_template COLLATE "C"
        ), '[]'::jsonb))
        FROM api.mcp_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.mcp_type = 'resource'
          AND r.uri_template ~ '\{[^}]+\}'
          AND internal.current_access_denial(h.object_id, h.access) IS NULL
    );
END
$$;

COMMENT ON FUNCTION api.mcp_list_resource_templates() IS
    'MCP resources/templates/list discovery. Emits ResourceTemplate objects (uriTemplate) for resources with RFC 6570 placeholders. Lists only the resources the caller may read.';

CREATE OR REPLACE FUNCTION api.mcp_list_prompts()
RETURNS jsonb
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = api, common, pg_temp
AS $$
BEGIN
    RETURN (
        SELECT jsonb_build_object('prompts', COALESCE(jsonb_agg(
            internal.jsonb_without_null_members(
                jsonb_build_object(
                    'name', r.mcp_name,
                    'title', h.title,
                    'description', h.description,
                    'arguments', r.arguments
                )
            )
            ORDER BY r.mcp_name COLLATE "C"
        ), '[]'::jsonb))
        FROM api.mcp_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.mcp_type = 'prompt'
          AND internal.current_access_denial(h.object_id, h.access) IS NULL
    );
END
$$;

COMMENT ON FUNCTION api.mcp_list_prompts() IS
    'MCP prompt discovery. Strips NULL fields for spec compliance (clients reject "title": null). Lists only the prompts the caller may get.';

-- ============================================================================
-- Route Policy Resolution (resolve-then-open)
-- ============================================================================
-- The client gateway — gateways/python/gateway.py, or the reverse proxy a deployment
-- supplies — calls one of these BEFORE opening the dispatch transaction, then
-- opens with the returned characteristics:
--
--     BEGIN TRANSACTION ISOLATION LEVEL <transaction_isolation>
--         [READ ONLY [DEFERRABLE]];
--
-- isolation = max(route floor, client-requested) — a route "just works" for a
-- caller that sends nothing, a caller may escalate, and a downgrade below the
-- floor is structurally impossible. NULL isolation means "open at the
-- connection default". The DB-side shortfall checks in the gateways above stay
-- as the fail-closed invariant for proxies that skip this lookup (428 /
-- pgmi.transaction_isolation_too_weak / pgmi.transaction_read_only_required).
--
-- The lookup runs OUTSIDE the dispatch transaction (autocommit or its own
-- short transaction) — that is the point: the policy must be known before
-- BEGIN. One extra round trip; it cannot go stale mid-request because the
-- registry only changes at deploy time (see api.catalog_version() for the
-- cache-invalidation signal if a proxy wants to cache the catalog).

-- The policy also names the handler and the x-* annotations on its
-- inputSchema properties, so the gateway's request and response interceptors
-- select themselves from what the database declares, in the call it already
-- makes (gateways/PROTOCOL.md, "Extension seams").
CREATE OR REPLACE FUNCTION internal.input_annotations(p_schema jsonb)
RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_object_agg(p.key, a.annotations)
    FROM jsonb_each(CASE WHEN jsonb_typeof(internal.resolved_schema(p_schema)->'properties') = 'object'
                         THEN internal.resolved_schema(p_schema)->'properties' ELSE '{}' END) p
    CROSS JOIN LATERAL (
        SELECT jsonb_object_agg(k.key, k.value) AS annotations
        FROM jsonb_each(CASE WHEN jsonb_typeof(p.value) = 'object' THEN p.value ELSE '{}' END) k
        WHERE k.key LIKE 'x-%'
    ) a
    WHERE a.annotations IS NOT NULL;
$$;

COMMENT ON FUNCTION internal.input_annotations(jsonb) IS
    'The x-* keywords of each top-level inputSchema property, as {property: {keyword: value}}; NULL when there are none. Returned by the policy functions for the gateway''s interceptors.';

DROP FUNCTION IF EXISTS api.rest_route_policy(text, text, text);

CREATE OR REPLACE FUNCTION api.rest_route_policy(
    p_method text,
    p_url text,
    p_requested_isolation text DEFAULT NULL
) RETURNS TABLE(
    transaction_isolation text,
    transaction_read_only boolean,
    transaction_deferrable boolean,
    handler_name text,
    input_annotations jsonb
)
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
    SELECT p.*, h.handler_function_name, internal.input_annotations(h.input_json_schema)
    FROM api.handler h
    CROSS JOIN LATERAL internal.resolve_transaction_policy(
        h.min_transaction_isolation, h.read_only, p_requested_isolation) p
    -- HEAD is routed as GET, as rest_dispatch does: resolving a policy for a
    -- route the dispatch would not pick is worse than no policy.
    WHERE h.object_id = internal.rest_route_lookup(
        CASE WHEN upper(trim(p_method)) = 'HEAD' THEN 'GET' ELSE upper(trim(p_method)) END,
        api.canonical_path(api.url_path(trim(p_url))));
$$;

COMMENT ON FUNCTION api.rest_route_policy(text, text, text) IS
    'Resolves the transaction characteristics to open with before calling api.rest_invoke: isolation = max(route floor, p_requested_isolation), read-only and DEFERRABLE from the route policy; and the handler name and inputSchema annotations the gateway''s interceptors select on. Route matching is the same internal.rest_route_lookup that rest_invoke uses, HEAD included. Zero rows when no route matches — open at the requested level (or default) and let rest_invoke answer 404.';

DROP FUNCTION IF EXISTS api.rpc_route_policy(text, text);

CREATE OR REPLACE FUNCTION api.rpc_route_policy(
    p_method_name text,
    p_requested_isolation text DEFAULT NULL
) RETURNS TABLE(
    transaction_isolation text,
    transaction_read_only boolean,
    transaction_deferrable boolean,
    handler_name text,
    input_annotations jsonb
)
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
    SELECT p.*, h.handler_function_name, internal.input_annotations(h.input_json_schema)
    FROM api.rpc_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
    CROSS JOIN LATERAL internal.resolve_transaction_policy(
        h.min_transaction_isolation, h.read_only, p_requested_isolation) p
    WHERE r.method_name = p_method_name;
$$;

COMMENT ON FUNCTION api.rpc_route_policy(text, text) IS
    'Resolves the transaction characteristics to open with before calling api.rpc_invoke, keyed by JSON-RPC method name, with the handler name and inputSchema annotations as api.rest_route_policy returns them. Zero rows when the method is not registered.';

-- ============================================================================
-- Replay
-- ============================================================================
-- Re-runs a logged REST, RPC or MCP exchange as its original caller: the logged
-- context is applied (no credential is re-presented), the request goes through
-- the same dispatch, access decision and transaction policy as a live one, and
-- everything it did, its own exchange row and the applied context included, is
-- rolled back before the response is returned. Owner-only: it is on no role's
-- grant list, so the admin login (a member of the owner) and the
-- platform.operate endpoint reach it, the gateway does not.
CREATE OR REPLACE FUNCTION api.replay_exchange(p_exchange_id uuid)
RETURNS api.http_response
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $$
DECLARE
    v_rest api.rest_exchange;
    v_rpc api.rpc_exchange;
    v_mcp api.mcp_exchange;
    v_response api.http_response;
BEGIN
    SELECT * INTO v_rest FROM api.rest_exchange WHERE object_id = p_exchange_id;
    IF NOT FOUND THEN
        SELECT * INTO v_rpc FROM api.rpc_exchange WHERE object_id = p_exchange_id;
        IF NOT FOUND THEN
            SELECT * INTO v_mcp FROM api.mcp_exchange WHERE object_id = p_exchange_id;
            IF NOT FOUND THEN
                RAISE EXCEPTION 'No exchange %', p_exchange_id
                    USING ERRCODE = 'no_data_found';
            END IF;
        END IF;
    END IF;

    BEGIN
        PERFORM internal.apply_request_log_context(COALESCE(v_rest.context, v_rpc.context, v_mcp.context, '{}'::jsonb));
        IF v_rest.object_id IS NOT NULL THEN
            v_response := internal.rest_dispatch((v_rest.request).method, (v_rest.request).url,
                (v_rest.request).headers, (v_rest.request).content, false);
        ELSIF v_rpc.object_id IS NOT NULL THEN
            v_response := internal.rpc_dispatch((v_rpc.request).route_id,
                (v_rpc.request).headers, (v_rpc.request).content, false);
        ELSE
            v_response := api.json_response(200, (internal.mcp_dispatch(v_mcp.mcp_type,
                COALESCE((v_mcp.request).uri, v_mcp.mcp_name), (v_mcp.request).arguments,
                (v_mcp.request).context, (v_mcp.request).request_id, false)).envelope);
        END IF;
        RAISE EXCEPTION USING ERRCODE = 'PGRPL';
    EXCEPTION WHEN SQLSTATE 'PGRPL' THEN
        NULL;
    END;
    RETURN v_response;
END;
$$;

COMMENT ON FUNCTION api.replay_exchange(uuid) IS
    'Re-runs a logged REST, RPC or MCP exchange in its logged context (internal.apply_request_log_context) and rolls back everything it did; returns the response (MCP: the envelope as a 200). P0002 when no such exchange. Owner only.';
