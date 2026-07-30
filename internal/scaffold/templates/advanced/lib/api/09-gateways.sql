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
--
-- What requiresAuth guarantees, precisely: the request resolves to an ACTIVE
-- membership."user" row via api.current_user_id() — which also rejects an
-- api-key identity whose key is disabled or expired. It does NOT authenticate
-- anyone; that is still the upstream gateway's job. In particular
-- internal.provision_current_user JIT-creates a user when the request carries
-- BOTH a well-formed x-user-id and an x-user-email, so on a read-write request
-- an unknown-but-emailed identity resolves by being created. That is the
-- intended trust model, not a gap in the gate — it is why these headers must
-- never survive from the client.

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
    -- A READ ONLY transaction cannot upsert. Skip JIT provisioning rather than
    -- fail every read-only request: a user already provisioned by a prior
    -- read-write request resolves normally; a never-seen identity simply does
    -- not resolve (auth-required routes answer 401 until a read-write request
    -- provisions it).
    IF v_subject IS NULL OR p_email IS NULL OR length(p_email) > 4096
       OR internal.transaction_is_read_only() THEN
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
    WITH merged AS (
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
            ), ''::extensions.hstore)
            || extensions.hstore(ARRAY[
                'content-length', COALESCE(octet_length((p_response).content), 0)::text,
                'x-execution-time-ms', p_execution_ms::text,
                'x-pgmi-catalog-version', api.catalog_version()
            ])
            -- The API selects representations on accept and the version headers
            -- and scopes bodies to x-user-id. Identity travels in x-user-id and
            -- not Authorization, so RFC 9111 §3.5's shared-cache prohibition
            -- never engages: without Vary, a heuristically-caching intermediary
            -- may store one user's GET /me and replay it to another. Only
            -- PostgreSQL knows this, so the fronting proxy cannot add it.
            || extensions.hstore('vary', 'accept, x-api-version, accept-version, x-user-id')
            || COALESCE(p_extra, ''::extensions.hstore) AS h
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

-- Error responses need the same treatment as successful ones. Two headers were
-- being lost on every early return: x-pgmi-catalog-version, which exists so a
-- client learns its cached route table went stale -- and a stale route table
-- manifests as exactly the 404 that omitted it -- and Vary, without which the
-- 401/406 variants are cacheable against the wrong request.
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
    v_match_method text;
    v_is_head boolean;
    v_allow text;
    v_auth_extra extensions.hstore;
BEGIN
    v_start_time := clock_timestamp();
    RAISE DEBUG 'rest_invoke: % %', p_method, p_url;

    IF length(p_method) > 16 THEN
        RETURN internal.finalize_error(
            api.problem_response(400, 'Bad Request', 'HTTP method too long'), v_start_time);
    END IF;
    IF length(p_url) > 8192 THEN
        RETURN internal.finalize_error(
            api.problem_response(414, 'URI Too Long', 'URL exceeds maximum length'), v_start_time);
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
    v_path := api.canonical_path(api.url_path(p_url));

    -- RFC 9110 §9.3.2: HEAD is GET without a body. The default method_regexp
    -- does not list it, so routing HEAD literally 404s every route that took
    -- the default. Match it as GET and strip the body on the way out.
    v_is_head := p_method = 'HEAD';
    v_match_method := CASE WHEN v_is_head THEN 'GET' ELSE p_method END;

    SELECT h.handler_exec_sql, h.object_id, h.response_headers, h.accepts, h.produces, h.requires_auth,
           h.output_json_schema, h.min_transaction_isolation, h.read_only,
           r.route_name, r.auto_log
    INTO v_route
    FROM api.rest_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
    WHERE v_path ~ r.address_regexp
      AND v_match_method ~ r.method_regexp
      AND v_version ~ r.version_regexp
    ORDER BY r.sequence_number DESC, r.route_name
    LIMIT 1;

    IF v_route.handler_exec_sql IS NULL THEN
        -- Path and method were matched in one predicate, so a method mismatch on
        -- an existing resource was indistinguishable from a missing one and both
        -- returned 404. RFC 9110 §15.5.6 reserves 405 for the former and makes
        -- the Allow header mandatory. The proxy in front cannot synthesize this:
        -- it has no route table.
        SELECT string_agg(DISTINCT upper(m.method), ', ' ORDER BY upper(m.method))
        INTO v_allow
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        CROSS JOIN LATERAL unnest(api.openapi_methods(r.method_regexp)) AS m(method)
        WHERE v_path ~ r.address_regexp
          AND v_version ~ r.version_regexp;

        IF v_allow IS NOT NULL THEN
            RAISE DEBUG 'rest_invoke: path matched but method % not allowed', p_method;
            -- HEAD is served wherever GET is, so advertise it alongside.
            IF v_allow ~ 'GET' AND v_allow !~ 'HEAD' THEN
                v_allow := v_allow || ', HEAD';
            END IF;
            RETURN internal.finalize_error(
                api.problem_response(405, 'Method Not Allowed',
                    format('%s is not allowed on %s. Allowed: %s', p_method, v_path, v_allow)),
                v_start_time,
                extensions.hstore('allow', v_allow));
        END IF;

        RAISE DEBUG 'rest_invoke: No route matched';
        RETURN internal.finalize_error(
            api.problem_response(404, 'Not Found', 'No route matches ' || p_method || ' ' || p_url),
            v_start_time);
    END IF;

    RAISE DEBUG 'rest_invoke: Matched route %', v_route.route_name;

    -- Resolve identity first, then gate on session state (not header presence)
    -- so a present-but-malformed x-user-id fails closed.
    PERFORM internal.setup_auth_session(p_headers);

    -- An authenticated route's representation is per-identity; a shared cache
    -- must not store it even with Vary present.
    v_auth_extra := CASE
        WHEN v_route.requires_auth THEN extensions.hstore('cache-control', 'private, no-store')
        ELSE ''::extensions.hstore
    END;

    -- Gate on a RESOLVED user, not on the header being well-formed. The GUC is
    -- set from header syntax alone, so 'google|nobody-at-all' satisfied it and
    -- reached the handler, and a deactivated user kept full access because
    -- only api.current_user_id() consults membership."user".is_active.
    IF v_route.requires_auth AND api.current_user_id() IS NULL THEN
        RAISE DEBUG 'rest_invoke: Auth required but no user resolved';
        -- RFC 9110 §15.5.2: a 401 MUST carry at least one challenge.
        RETURN internal.finalize_error(
            api.problem_response(401, 'Unauthorized', 'Authentication required'),
            v_start_time,
            extensions.hstore('www-authenticate', api.www_authenticate_challenge()));
    END IF;

    -- Enforce the route's transaction isolation floor. The gateway can only READ
    -- the level; the caller must open the transaction at the required level
    -- before the first statement (see lib/api/00-transaction-isolation.sql).
    v_iso_shortfall := internal.transaction_isolation_shortfall(v_route.min_transaction_isolation);
    IF v_iso_shortfall IS NOT NULL THEN
        RAISE DEBUG 'rest_invoke: isolation too weak (need %, have %)',
            v_route.min_transaction_isolation, v_iso_shortfall;
        RETURN internal.finalize_error(api.problem_response(
            428, 'Precondition Required',
            format('Route requires %s isolation but current transaction uses %s.',
                   v_route.min_transaction_isolation, v_iso_shortfall),
            code => 'pgmi.transaction_isolation_too_weak'
        ), v_start_time, v_auth_extra);
    END IF;

    -- Enforce the route's read-only declaration the same way: the gateway can
    -- only READ the access mode; the caller opens the transaction READ ONLY
    -- (resolve-then-open via api.rest_route_policy) or is rejected fail-closed.
    IF v_route.read_only AND NOT internal.transaction_is_read_only() THEN
        RAISE DEBUG 'rest_invoke: read-only route in a read-write transaction';
        RETURN internal.finalize_error(api.problem_response(
            428, 'Precondition Required',
            'Route requires a READ ONLY transaction but the current transaction is read-write.',
            code => 'pgmi.transaction_read_only_required'
        ), v_start_time, v_auth_extra);
    END IF;

    -- Enforce the handler's declared accepts against the request Content-Type.
    -- Only when the request carries a Content-Type (a body); the default
    -- accepts of {*/*} matches everything, so this only bites handlers that
    -- explicitly narrow the types they accept.
    v_content_type := btrim(split_part(COALESCE(p_headers->'content-type', ''), ';', 1));
    IF v_content_type <> ''
       AND NOT api.accept_matches(array_to_string(v_route.accepts, ', '), ARRAY[v_content_type]) THEN
        RETURN internal.finalize_error(api.problem_response(
            415,
            'Unsupported Media Type',
            format('Supported request content types: %s', array_to_string(v_route.accepts, ', '))
        ), v_start_time, v_auth_extra);
    END IF;

    IF NOT api.accept_matches(p_headers->'accept', v_route.produces) THEN
        RETURN internal.finalize_error(api.problem_response(
            406,
            'Not Acceptable',
            format('Supported content types: %s', array_to_string(v_route.produces, ', '))
        ), v_start_time, v_auth_extra);
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
            extensions.hstore('x-route-id', v_route.object_id::text) || v_auth_extra);

        -- Exchange logging is a write; a READ ONLY transaction cannot do it.
        -- Skipping beats failing every correctly-opened read-only request.
        IF v_route.auto_log AND NOT internal.transaction_is_read_only() THEN
            INSERT INTO api.rest_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_route.object_id, v_request, v_response, now());
        END IF;

        -- RFC 9110 §9.3.2: HEAD is identical to GET except that the body is
        -- omitted. content-length keeps the value GET would have sent, which is
        -- what makes HEAD useful for probing size. The exchange above logs the
        -- full response deliberately.
        IF v_is_head THEN
            v_response := ((v_response).status_code, (v_response).headers, NULL::bytea)::api.http_response;
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
            -- Class 22 is data exception: the client sent something PostgreSQL
            -- could not interpret. 22P02 alone covers a malformed uuid, integer
            -- or jsonb, which a body of 'not json' produces -- these were 500s,
            -- so 5xx alerting fired on user typos and retry middleware treated a
            -- permanently-bad request as retryable.
            WHEN '22P02' THEN v_status := 400; v_title := 'Bad Request';          v_client_detail := 'A submitted value is malformed';
            WHEN '22023' THEN v_status := 400; v_title := 'Bad Request';          v_client_detail := 'A submitted parameter value is invalid';
            WHEN '22001' THEN v_status := 422; v_title := 'Unprocessable Entity'; v_client_detail := 'A submitted value is too long';
            WHEN '22003' THEN v_status := 422; v_title := 'Unprocessable Entity'; v_client_detail := 'A submitted number is out of range';
            WHEN '22007' THEN v_status := 400; v_title := 'Bad Request';          v_client_detail := 'A submitted date or time is malformed';
            WHEN '22008' THEN v_status := 422; v_title := 'Unprocessable Entity'; v_client_detail := 'A submitted date or time is out of range';
            -- 25006 has two causes and they are not the same defect. A route
            -- that did NOT declare read_only, dispatched inside a READ ONLY
            -- transaction, is the mirror of the 428 above: the caller opened the
            -- wrong transaction and can fix it. A route that DID declare
            -- read_only and then wrote anyway broke its own contract -- that is
            -- a handler bug, and 500 is the honest answer.
            WHEN '25006' THEN
                IF v_route.read_only THEN
                    v_status := 500; v_title := 'Internal Server Error';
                    v_client_detail := 'An internal error occurred';
                ELSE
                    v_status := 428; v_title := 'Precondition Required';
                    v_client_detail := 'Route requires a read-write transaction but the current transaction is READ ONLY';
                END IF;
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
            'x-pgmi-catalog-version', api.catalog_version(),
            'x-error-sqlstate', v_sqlstate
        ]);
        -- autoLog=false must hold on the failure path too: v_request carries the
        -- headers and body, so a POST /login registered autoLog=false would
        -- otherwise persist plaintext credentials on any constraint violation.
        IF v_route.auto_log AND NOT internal.transaction_is_read_only() THEN
            INSERT INTO api.rest_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_route.object_id, v_request, v_response, now());
        END IF;

        -- Return sanitized error to client (hide internal details).
        -- x-error-sqlstate stays on the logged copy only: it is an internal
        -- diagnostic, and the sanitized body deliberately withholds the rest of
        -- the failure detail.
        RETURN internal.finalize_error(
            api.problem_response(v_status, v_title, v_client_detail),
            v_start_time,
            v_auth_extra
                || CASE WHEN v_status = 428
                        THEN extensions.hstore('x-pgmi-transaction-required', 'read-write')
                        ELSE ''::extensions.hstore END);
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

    -- JSON-RPC 2.0: an unparseable body is -32700 Parse error, and the handler
    -- must not run. Swallowing the parse failure and dispatching anyway made a
    -- malformed request indistinguishable from a well-formed one with a null id.
    BEGIN
        v_json_id := api.content_json(p_content)->'id';
    EXCEPTION WHEN OTHERS THEN
        RAISE DEBUG 'rpc_invoke: unparseable request body: %', SQLERRM;
        RETURN internal.finalize_error(
            api.jsonrpc_error(-32700, 'Parse error', NULL::jsonb), v_start_time);
    END;

    SELECT h.handler_exec_sql, h.object_id, h.requires_auth,
           h.response_headers, h.output_json_schema, h.min_transaction_isolation, h.read_only,
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

    -- Resolve identity first, then gate on session state (not header presence)
    -- so a present-but-malformed x-user-id fails closed.
    PERFORM internal.setup_auth_session(p_headers);

    -- Resolved user, not header shape -- see rest_invoke.
    IF v_handler.requires_auth AND api.current_user_id() IS NULL THEN
        RAISE DEBUG 'rpc_invoke: Auth required but no user resolved';
        RETURN internal.finalize_error(
            api.jsonrpc_error(-32001, 'Authentication required', v_json_id), v_start_time,
            extensions.hstore('www-authenticate', api.www_authenticate_challenge()));
    END IF;

    -- Enforce the route's transaction isolation floor (see rest_invoke). The
    -- precise HTTP status (428) rides on the response while the JSON-RPC error
    -- stays in its correct class; the machine token is carried in error.data.code.
    v_iso_shortfall := internal.transaction_isolation_shortfall(v_handler.min_transaction_isolation);
    IF v_iso_shortfall IS NOT NULL THEN
        RAISE DEBUG 'rpc_invoke: isolation too weak (need %, have %)',
            v_handler.min_transaction_isolation, v_iso_shortfall;
        RETURN internal.finalize_error(api.json_response(428, jsonb_build_object(
            'jsonrpc', '2.0',
            'error', jsonb_build_object(
                'code', -32600,
                'message', format('Route requires %s isolation but current transaction uses %s.',
                                  v_handler.min_transaction_isolation, v_iso_shortfall),
                'data', jsonb_build_object('code', 'pgmi.transaction_isolation_too_weak')
            ),
            'id', v_json_id
        )), v_start_time);
    END IF;

    -- Enforce the route's read-only declaration (see rest_invoke).
    IF v_handler.read_only AND NOT internal.transaction_is_read_only() THEN
        RAISE DEBUG 'rpc_invoke: read-only route in a read-write transaction';
        RETURN internal.finalize_error(api.json_response(428, jsonb_build_object(
            'jsonrpc', '2.0',
            'error', jsonb_build_object(
                'code', -32600,
                'message', 'Route requires a READ ONLY transaction but the current transaction is read-write.',
                'data', jsonb_build_object('code', 'pgmi.transaction_read_only_required')
            ),
            'id', v_json_id
        )), v_start_time);
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

        -- Exchange logging is a write; skip in a READ ONLY transaction (see rest_invoke).
        IF v_handler.auto_log AND NOT internal.transaction_is_read_only() THEN
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
            -- Class 22 (data exception) is invalid params, not an internal
            -- error: the client sent something PostgreSQL could not interpret.
            WHEN '22P02' THEN v_status := 400; v_rpc_code := -32602; v_client_msg := 'A submitted value is malformed';
            WHEN '22023' THEN v_status := 400; v_rpc_code := -32602; v_client_msg := 'A submitted parameter value is invalid';
            WHEN '22001' THEN v_status := 422; v_rpc_code := -32602; v_client_msg := 'A submitted value is too long';
            WHEN '22003' THEN v_status := 422; v_rpc_code := -32602; v_client_msg := 'A submitted number is out of range';
            WHEN '22007' THEN v_status := 400; v_rpc_code := -32602; v_client_msg := 'A submitted date or time is malformed';
            WHEN '22008' THEN v_status := 422; v_rpc_code := -32602; v_client_msg := 'A submitted date or time is out of range';
            -- See rest_invoke: a declared read_only route that writes is a
            -- handler bug (500); an undeclared one in a READ ONLY transaction is
            -- the caller's precondition to fix (428).
            WHEN '25006' THEN
                IF v_handler.read_only THEN
                    v_status := 500; v_rpc_code := -32603; v_client_msg := 'Internal error';
                ELSE
                    v_status := 428; v_rpc_code := -32600;
                    v_client_msg := 'Route requires a read-write transaction but the current transaction is READ ONLY';
                END IF;
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
            'x-pgmi-catalog-version', api.catalog_version(),
            'x-error-sqlstate', v_sqlstate
        ]);
        -- autoLog=false must hold on the failure path too (see rest_invoke).
        IF v_handler.auto_log AND NOT internal.transaction_is_read_only() THEN
            INSERT INTO api.rpc_exchange (handler_object_id, request, response, completed_at)
            VALUES (v_handler.object_id, v_request, v_response, now());
        END IF;

        -- Return sanitized error to client (hide internal details).
        -- x-error-sqlstate stays on the logged copy only.
        RETURN internal.finalize_error(api.json_response(v_status, jsonb_build_object(
            'jsonrpc', '2.0',
            'error', jsonb_build_object('code', v_rpc_code, 'message', v_client_msg),
            'id', v_json_id
        )), v_start_time);
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
        SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name, h.min_transaction_isolation, h.read_only
        INTO v_handler
        FROM api.handler h
        JOIN api.mcp_route r ON r.handler_object_id = h.object_id
        WHERE r.mcp_type = 'resource'
          AND h.deleted_at IS NULL
          AND p_name_or_uri ~ r.uri_regexp
        ORDER BY length(r.uri_template) DESC, r.mcp_name
        LIMIT 1;
    ELSE
        SELECT h.handler_exec_sql, h.object_id, h.requires_auth, r.mcp_name, h.min_transaction_isolation, h.read_only
        INTO v_handler
        FROM api.handler h
        JOIN api.mcp_route r ON r.handler_object_id = h.object_id
        WHERE r.mcp_name = p_name_or_uri AND r.mcp_type = p_mcp_type AND h.deleted_at IS NULL;
    END IF;

    IF v_handler.handler_exec_sql IS NULL THEN
        RAISE DEBUG 'mcp_dispatch: % not found', p_mcp_type;
        RETURN api.mcp_error(-32602, initcap(p_mcp_type) || ' not found: ' || p_name_or_uri, p_request_id);
    END IF;

    RAISE DEBUG 'mcp_dispatch: Matched %', v_handler.mcp_name;

    PERFORM internal.apply_mcp_auth_context(p_context);

    -- Resolved user, not header shape -- see rest_invoke.
    IF v_handler.requires_auth AND api.current_user_id() IS NULL THEN
        RAISE DEBUG 'mcp_dispatch: Auth required but no user resolved';
        RETURN api.mcp_error(-32001, 'Authentication required: no user resolved from context', p_request_id);
    END IF;

    -- Enforce the route's transaction isolation floor (see rest_invoke).
    v_iso_shortfall := internal.transaction_isolation_shortfall(v_handler.min_transaction_isolation);
    IF v_iso_shortfall IS NOT NULL THEN
        RETURN api.mcp_error(
            -32600,
            format('Route requires %s isolation but current transaction uses %s.',
                   v_handler.min_transaction_isolation, v_iso_shortfall),
            p_request_id,
            jsonb_build_object('code', 'pgmi.transaction_isolation_too_weak')
        );
    END IF;

    -- Enforce the route's read-only declaration (see rest_invoke).
    IF v_handler.read_only AND NOT internal.transaction_is_read_only() THEN
        RETURN api.mcp_error(
            -32600,
            'Route requires a READ ONLY transaction but the current transaction is read-write.',
            p_request_id,
            jsonb_build_object('code', 'pgmi.transaction_read_only_required')
        );
    END IF;

    v_request := CASE WHEN p_mcp_type = 'resource'
        THEN (NULL, p_name_or_uri, p_context, p_request_id)::api.mcp_request
        ELSE (p_arguments, NULL, p_context, p_request_id)::api.mcp_request
    END;

    BEGIN
        RAISE DEBUG 'mcp_dispatch: Invoking handler %', v_handler.object_id;
        EXECUTE v_handler.handler_exec_sql INTO v_response USING v_request;

        -- Exchange logging is a write; skip in a READ ONLY transaction (see rest_invoke).
        IF NOT internal.transaction_is_read_only() THEN
            INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
            VALUES (v_handler.object_id, p_mcp_type, v_handler.mcp_name, v_request, v_response);
        END IF;

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
        IF NOT internal.transaction_is_read_only() THEN
            INSERT INTO api.mcp_exchange (handler_object_id, mcp_type, mcp_name, request, response)
            VALUES (v_handler.object_id, p_mcp_type, v_handler.mcp_name, v_request, v_response);
        END IF;

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
--
-- LANGUAGE plpgsql IS LOAD-BEARING HERE, do not "simplify" these four
-- discovery functions to LANGUAGE sql. Each body is a single RETURN (SELECT),
-- so the conversion looks free, and it is not: their auth filter calls
-- api.current_user_id(), which lives in membership/05-current-user.sql and
-- therefore does not exist yet when lib/ deploys. A LANGUAGE sql body is
-- parsed and its dependencies resolved at CREATE time; plpgsql resolves names
-- at call time. Converting fails the first deploy with
-- `function api.current_user_id() does not exist (SQLSTATE 42883)`.

DROP FUNCTION IF EXISTS api.mcp_list_tools();
DROP FUNCTION IF EXISTS api.mcp_list_tools(text[]);

CREATE OR REPLACE FUNCTION api.mcp_list_tools(p_tags text[] DEFAULT NULL)
RETURNS jsonb
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = api, pg_temp
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
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        CROSS JOIN norm
        WHERE r.mcp_type = 'tool'
          AND (norm.tag_filter IS NULL OR r.tags && norm.tag_filter)
          -- Hide auth-required tools from callers whose identity does not resolve
          -- to an active user. Discovery must agree with the invocation gate --
          -- listing a tool that mcp_dispatch will then 401 is worse than hiding it.
          AND (NOT h.requires_auth
               OR (SELECT api.current_user_id()) IS NOT NULL)
    );
END
$$;

COMMENT ON FUNCTION api.mcp_list_tools(text[]) IS
    'MCP tool discovery. Returns {"tools":[...]} with name, title, description, inputSchema, outputSchema, and _meta.tags (pgmi extension). Hides tools requiring auth unless the session resolves to an active user. Pass p_tags to filter by tag overlap (NULL or empty = no filter).';

CREATE OR REPLACE FUNCTION api.mcp_list_resources()
RETURNS jsonb
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = api, pg_temp
AS $$
BEGIN
    RETURN (
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
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.mcp_type = 'resource'
          AND r.uri_template !~ '\{[^}]+\}'
          AND (NOT h.requires_auth
               OR (SELECT api.current_user_id()) IS NOT NULL)
    );
END
$$;

COMMENT ON FUNCTION api.mcp_list_resources() IS
    'MCP resources/list discovery. Emits only concrete Resource objects (static uri). Templated resources are served by resources/templates/list. Hides auth-required resources from unauthenticated sessions.';

CREATE OR REPLACE FUNCTION api.mcp_list_resource_templates()
RETURNS jsonb
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = api, pg_temp
AS $$
BEGIN
    RETURN (
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
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.mcp_type = 'resource'
          AND r.uri_template ~ '\{[^}]+\}'
          AND (NOT h.requires_auth
               OR (SELECT api.current_user_id()) IS NOT NULL)
    );
END
$$;

COMMENT ON FUNCTION api.mcp_list_resource_templates() IS
    'MCP resources/templates/list discovery. Emits ResourceTemplate objects (uriTemplate) for resources with RFC 6570 placeholders. Hides auth-required resources from unauthenticated sessions.';

CREATE OR REPLACE FUNCTION api.mcp_list_prompts()
RETURNS jsonb
LANGUAGE plpgsql STABLE
SECURITY DEFINER
SET search_path = api, pg_temp
AS $$
BEGIN
    RETURN (
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
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.mcp_type = 'prompt'
          AND (NOT h.requires_auth
               OR (SELECT api.current_user_id()) IS NOT NULL)
    );
END
$$;

COMMENT ON FUNCTION api.mcp_list_prompts() IS
    'MCP prompt discovery. Strips NULL fields for spec compliance (clients reject "title": null). Hides auth-required prompts from unauthenticated sessions.';

-- ============================================================================
-- Route Policy Resolution (resolve-then-open)
-- ============================================================================
-- The client gateway — tools/mcp-gateway.py, or the reverse proxy a deployment
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

CREATE OR REPLACE FUNCTION api.rest_route_policy(
    p_method text,
    p_url text,
    p_requested_isolation text DEFAULT NULL,
    p_version text DEFAULT ''
) RETURNS TABLE(
    transaction_isolation text,
    transaction_read_only boolean,
    transaction_deferrable boolean
)
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
    SELECT p.*
    FROM api.rest_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
    CROSS JOIN LATERAL internal.resolve_transaction_policy(
        h.min_transaction_isolation, h.read_only, p_requested_isolation) p
    WHERE api.canonical_path(api.url_path(trim(p_url))) ~ r.address_regexp
      -- Mirror rest_invoke exactly, HEAD normalization included: resolving a
      -- policy for a route the dispatch would not pick is worse than no policy.
      AND (CASE WHEN upper(trim(p_method)) = 'HEAD' THEN 'GET' ELSE upper(trim(p_method)) END)
          ~ r.method_regexp
      AND COALESCE(p_version, '') ~ r.version_regexp
    ORDER BY r.sequence_number DESC, r.route_name
    LIMIT 1;
$$;

COMMENT ON FUNCTION api.rest_route_policy(text, text, text, text) IS
    'Resolves the transaction characteristics to open with before calling api.rest_invoke: isolation = max(route floor, p_requested_isolation), read-only and DEFERRABLE from the route policy. Route matching mirrors rest_invoke, HEAD included. p_version MUST carry the same x-api-version/accept-version the request will present — pass api.rest_route_policy_for() the request headers instead of defaulting it. Zero rows when no route matches — open at the requested level (or default) and let rest_invoke answer 404.';

-- p_version defaulting to '' is a live footgun: a gateway that forwards method
-- and URL but not the version headers resolves a policy for one route and then
-- dispatches to another, opening the transaction with the wrong isolation or
-- access mode. This overload takes the same headers the caller will hand to
-- rest_invoke, so the two cannot disagree.
CREATE OR REPLACE FUNCTION api.rest_route_policy_for(
    p_method text,
    p_url text,
    p_headers extensions.hstore DEFAULT ''::extensions.hstore,
    p_requested_isolation text DEFAULT NULL
) RETURNS TABLE(
    transaction_isolation text,
    transaction_read_only boolean,
    transaction_deferrable boolean
)
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
    SELECT * FROM api.rest_route_policy(
        p_method,
        p_url,
        p_requested_isolation,
        COALESCE(
            COALESCE(p_headers, ''::extensions.hstore)->'x-api-version',
            COALESCE(p_headers, ''::extensions.hstore)->'accept-version',
            ''
        )
    );
$$;

COMMENT ON FUNCTION api.rest_route_policy_for(text, text, extensions.hstore, text) IS
    'api.rest_route_policy with the API version taken from the request headers exactly as rest_invoke takes it. Prefer this in a gateway: it cannot resolve a different route than the one dispatch will pick.';

CREATE OR REPLACE FUNCTION api.rpc_route_policy(
    p_method_name text,
    p_requested_isolation text DEFAULT NULL
) RETURNS TABLE(
    transaction_isolation text,
    transaction_read_only boolean,
    transaction_deferrable boolean
)
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
    SELECT p.*
    FROM api.rpc_route r
    JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
    CROSS JOIN LATERAL internal.resolve_transaction_policy(
        h.min_transaction_isolation, h.read_only, p_requested_isolation) p
    WHERE r.method_name = p_method_name;
$$;

COMMENT ON FUNCTION api.rpc_route_policy(text, text) IS
    'Resolves the transaction characteristics to open with before calling api.rpc_invoke, keyed by JSON-RPC method name. Zero rows when the method is not registered.';

-- One call for the whole MCP request: parses the JSON-RPC envelope, resolves
-- the tool/prompt/resource the same way internal.mcp_dispatch will, and always
-- returns exactly one row — non-dispatch methods (initialize, tools/list, ...)
-- and unknown names resolve to the no-policy row so the caller's code path is
-- uniform and the dispatcher still produces its proper error.
CREATE OR REPLACE FUNCTION api.mcp_request_policy(
    p_request jsonb,
    p_requested_isolation text DEFAULT NULL
) RETURNS TABLE(
    transaction_isolation text,
    transaction_read_only boolean,
    transaction_deferrable boolean
)
LANGUAGE sql STABLE
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $$
    WITH target AS (
        SELECT CASE p_request->>'method'
                   WHEN 'tools/call'     THEN 'tool'
                   WHEN 'prompts/get'    THEN 'prompt'
                   WHEN 'resources/read' THEN 'resource'
               END AS mcp_type,
               COALESCE(p_request->'params'->>'name', p_request->'params'->>'uri') AS key
    ),
    route AS (
        SELECT h.min_transaction_isolation, h.read_only
        FROM target t
        JOIN api.mcp_route r ON r.mcp_type::text = t.mcp_type
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE t.key IS NOT NULL
          AND ((t.mcp_type IN ('tool', 'prompt') AND r.mcp_name = t.key)
               OR (t.mcp_type = 'resource' AND t.key ~ r.uri_regexp))
        ORDER BY CASE WHEN t.mcp_type = 'resource' THEN length(r.uri_template) END DESC NULLS LAST,
                 r.mcp_name
        LIMIT 1
    ),
    policy AS (
        SELECT min_transaction_isolation, read_only FROM route
        UNION ALL
        SELECT NULL, false WHERE NOT EXISTS (SELECT 1 FROM route)
    )
    SELECT p.*
    FROM policy
    CROSS JOIN LATERAL internal.resolve_transaction_policy(
        policy.min_transaction_isolation, policy.read_only, p_requested_isolation) p;
$$;

COMMENT ON FUNCTION api.mcp_request_policy(jsonb, text) IS
    'Resolves the transaction characteristics for a raw MCP JSON-RPC request before api.mcp_handle_request. Dispatching methods (tools/call, prompts/get, resources/read) resolve their route''s policy with the same precedence as internal.mcp_dispatch; everything else returns the no-policy row. Always exactly one row.';

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
    RAISE NOTICE '  ✓ api.rest/rpc_route_policy(), api.mcp_request_policy() - resolve-then-open policy lookup';
END $$;

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT EXECUTE ON ALL ROUTINES IN SCHEMA api TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON ALL ROUTINES IN SCHEMA api TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION internal.setup_auth_session(extensions.hstore) TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION internal.set_auth_user_id(text) TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION internal.apply_mcp_auth_context(jsonb) TO %I', v_api_role);
    EXECUTE format('GRANT EXECUTE ON FUNCTION internal.provision_current_user(text) TO %I', v_api_role);
END $$;
