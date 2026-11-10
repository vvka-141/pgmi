-- ============================================================================
-- Test: HTTP semantics
-- ============================================================================
-- Status-code classes, mandatory headers, and negotiation. Each assertion here
-- corresponds to a normative requirement the gateway used to miss; the RFC
-- section is named so a future reader can check the claim rather than trust it.
-- ============================================================================

DO $$
DECLARE
    v_response api.http_response;
BEGIN
    RAISE NOTICE '→ Testing client errors are not 5xx';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-5001-4000-8000-000000000001',
            'path', '/parse-probe',
            'httpMethod', '^POST$',
            'name', 'parse_probe',
            'access', 'public'
        ),
        $body$
BEGIN
    -- api.content_json raises 22P02 on a body that is not JSON.
    RETURN api.json_response(200, api.content_json((request).content));
END;
$body$
    );

    v_response := api.rest_invoke(
        'POST', '/parse-probe', ''::extensions.hstore, convert_to('this is not json', 'UTF8'));

    -- Class 22 is a data exception: the client sent something unparseable.
    -- As a 500 it fired 5xx alerting on a typo and told retry middleware that a
    -- permanently-bad request was worth retrying.
    IF (v_response).status_code IS DISTINCT FROM 400 THEN
        RAISE EXCEPTION 'TEST FAILED: a malformed request body must be 4xx, got %',
            (v_response).status_code;
    END IF;

    RAISE NOTICE '  ✓ 22xxx data exceptions map to 4xx, not 500';
END $$;


DO $$
DECLARE
    v_response api.http_response;
BEGIN
    RAISE NOTICE '→ Testing 405, Allow, and HEAD';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-5002-4000-8000-000000000001',
            'path', '/method-probe',
            'httpMethod', '^POST$',
            'name', 'method_probe_405',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('ok', true));
END;
$body$
    );

    -- RFC 9110 §15.5.6: the resource exists, the method does not apply. Path and
    -- method used to be matched in one predicate, so this was an indistinguishable
    -- 404 and the proxy in front could not synthesize the difference.
    v_response := api.rest_invoke('GET', '/method-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 405 THEN
        RAISE EXCEPTION 'TEST FAILED: wrong method on an existing resource must be 405, got %',
            (v_response).status_code;
    END IF;
    IF ((v_response).headers->'allow') IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: RFC 9110 15.5.6 makes Allow mandatory on a 405';
    END IF;
    IF (((v_response).headers->'allow') !~ 'POST') IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: Allow must list the methods that do apply, got %',
            (v_response).headers->'allow';
    END IF;

    -- A path that matches nothing is still 404.
    v_response := api.rest_invoke('GET', '/no-such-resource', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'TEST FAILED: an unmatched path must stay 404, got %',
            (v_response).status_code;
    END IF;

    -- RFC 9110 §9.3.2: HEAD is GET without a body. The default method_regexp
    -- omits HEAD, so routing it literally 404d every route that took the default.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-5003-4000-8000-000000000001',
            'path', '/head-probe',
            'httpMethod', '^GET$',
            'name', 'head_probe',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('body', 'present'));
END;
$body$
    );

    v_response := api.rest_invoke('HEAD', '/head-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: HEAD must be served wherever GET is, got %',
            (v_response).status_code;
    END IF;
    IF (v_response).content IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: HEAD must omit the response body';
    END IF;
    -- content-length keeps the value GET would have sent; that is what makes
    -- HEAD useful for probing size.
    IF (COALESCE(((v_response).headers->'content-length')::int, 0) = 0) IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: HEAD must report the content-length GET would send, got %',
            COALESCE((v_response).headers->'content-length', '<none>');
    END IF;

    RAISE NOTICE '  ✓ 405 carries Allow, 404 stays 404, HEAD mirrors GET';
END $$;


DO $$
DECLARE
    v_response api.http_response;
BEGIN
    RAISE NOTICE '→ Testing mandatory and cache-correctness headers on errors';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-5004-4000-8000-000000000001',
            'path', '/guarded',
            'httpMethod', '^GET$',
            'name', 'guarded_probe',
            'access', 'authenticated'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, '{}'::jsonb);
END;
$body$
    );

    v_response := api.rest_invoke('GET', '/guarded', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: an unauthenticated request to a guarded route must be 401, got %',
            (v_response).status_code;
    END IF;

    -- RFC 9110 §15.5.2: a 401 MUST carry at least one challenge.
    IF ((v_response).headers->'www-authenticate') IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: RFC 9110 15.5.2 makes WWW-Authenticate mandatory on a 401';
    END IF;

    -- x-pgmi-catalog-version exists so a client learns its cached route table
    -- went stale -- and a stale route table shows up as exactly these errors.
    IF ((v_response).headers->'x-pgmi-catalog-version') IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: error responses must carry x-pgmi-catalog-version';
    END IF;

    -- Identity travels in the session cookie, not Authorization, so RFC 9111 §3.5's
    -- shared-cache prohibition never engages. Without Vary a caching
    -- intermediary may replay one user''s response to another. Clients never
    -- send x-login-session (the gateway derives it), so Vary names cookie.
    IF ((v_response).headers->'vary' ~ '\mcookie\M') IS NOT TRUE
       OR ((v_response).headers->'vary' ~ 'x-login-session') IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: responses must Vary on cookie, not x-login-session, got %',
            COALESCE((v_response).headers->'vary', '<none>');
    END IF;

    -- The sanitized body deliberately withholds failure detail; the SQLSTATE
    -- belongs on the logged copy, not the wire.
    IF ((v_response).headers->'x-error-sqlstate') IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: x-error-sqlstate is an internal diagnostic and must not reach the client';
    END IF;

    RAISE NOTICE '  ✓ errors carry the challenge, staleness and negotiation headers';
END $$;


DO $$
DECLARE
    v_headers extensions.hstore;
BEGIN
    RAISE NOTICE '→ Testing bodiless responses omit body framing';

    -- RFC 9110 §15.4.5: a 304 carries the representation metadata a 200 would
    -- have sent. content-length: 0 contradicts the 200 the client cached.
    v_headers := internal.finalize_response_headers(
        (304, extensions.hstore('content-type', 'application/json'), NULL::bytea)::api.http_response,
        '{}'::jsonb, 1.0, ''::extensions.hstore);

    IF v_headers ? 'content-length' THEN
        RAISE EXCEPTION 'TEST FAILED: a 304 must not carry content-length, got %',
            v_headers->'content-length';
    END IF;
    IF (v_headers->'content-type') IS DISTINCT FROM 'application/json' THEN
        RAISE EXCEPTION 'TEST FAILED: a 304 must keep the content-type the 200 sent, got %',
            COALESCE(v_headers->'content-type', '<none>');
    END IF;

    -- A 200 still frames its body.
    v_headers := internal.finalize_response_headers(
        (200, extensions.hstore('content-type', 'application/json'), convert_to('{}', 'UTF8'))::api.http_response,
        '{}'::jsonb, 1.0, ''::extensions.hstore);
    IF (v_headers->'content-length') IS DISTINCT FROM '2' THEN
        RAISE EXCEPTION 'TEST FAILED: a 200 must carry its content-length, got %',
            COALESCE(v_headers->'content-length', '<none>');
    END IF;

    RAISE NOTICE '  ✓ 204/304 omit content-length; 200 keeps it';
END $$;


DO $$
DECLARE
    v_response api.http_response;
BEGIN
    RAISE NOTICE '→ Testing Accept q-values';

    -- RFC 9110 §12.5.1: q=0 means "not acceptable". Ignoring the qvalue turned
    -- an explicit refusal into a 200 of exactly the type the client rejected.
    v_response := api.rest_invoke(
        'GET', '/head-probe', 'accept=>"application/json;q=0"'::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 406 THEN
        RAISE EXCEPTION 'TEST FAILED: accept with q=0 is a refusal and must be 406, got %',
            (v_response).status_code;
    END IF;

    v_response := api.rest_invoke(
        'GET', '/head-probe', 'accept=>"application/json;q=0.9"'::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: a positive q-value must still be accepted, got %',
            (v_response).status_code;
    END IF;

    RAISE NOTICE '  ✓ q=0 refuses, q>0 accepts';
END $$;


-- SQLSTATE 25006 has two causes and the gateway tells them apart: a route that
-- declared read_only and wrote anyway is a handler bug (500), while an
-- undeclared route dispatched inside a READ ONLY transaction is the caller's
-- precondition to fix (428). Neither branch is assertable from here -- reaching
-- the handler at all requires the transaction access mode the deploy cannot
-- change mid-run -- so both live in the Go suite, in
-- internal/scaffold/transaction_policy_test.go.

DO $$
DECLARE
    v_response api.http_response;
    v_route_id uuid;
BEGIN
    RAISE NOTICE '→ Testing RPC parse errors';

    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object(
            'id', 'ffffffff-5005-4000-8000-000000000001',
            'methodName', 'probe.echo',
            'description', 'RPC parse-error probe',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('jsonrpc', '2.0', 'result', 'ok', 'id', 1));
END;
$body$
    );

    v_route_id := api.rpc_resolve('probe.echo');

    -- JSON-RPC 2.0: an unparseable body is -32700 and the handler must not run.
    -- The parse failure used to be swallowed and the handler dispatched anyway.
    v_response := api.rpc_invoke(v_route_id, ''::extensions.hstore, convert_to('not json', 'UTF8'));

    IF (convert_from((v_response).content, 'UTF8')::jsonb #>> '{error,code}') IS DISTINCT FROM '-32700' THEN
        RAISE EXCEPTION 'TEST FAILED: an unparseable RPC body must return -32700, got %',
            convert_from((v_response).content, 'UTF8');
    END IF;

    RAISE NOTICE '  ✓ unparseable RPC bodies return -32700 without dispatching';
END $$;


DO $$
DECLARE
    v_response api.http_response;
    v_vary text;
BEGIN
    RAISE NOTICE '→ Testing malformed input and header handling at the gateway';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-5379-4000-8000-000000000001',
            'path', '/gateway-probe',
            'httpMethod', '^GET$',
            'name', 'gateway_probe',
            'produces', jsonb_build_array('application/json'),
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN ROW(200, extensions.hstore('vary', 'Accept-Language'), convert_to('{}', 'UTF8'))::api.http_response;
END;
$body$
    );
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-5379-4000-8000-000000000002',
            'path', '/decode-probe',
            'httpMethod', '^GET$',
            'name', 'decode_probe',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('v', api.url_decode('%FF')));
END;
$body$
    );

    -- RFC 9110 12.4.2: a q-value outside the grammar is the client's mistake,
    -- not a reason to abort the transaction without any HTTP response.
    v_response := api.rest_invoke('GET', '/gateway-probe',
        extensions.hstore('accept', 'application/json;q=.'), NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: a malformed Accept q-value must still be answered, got %', (v_response).status_code;
    END IF;

    -- Invalid percent-encoding (22021) is a data exception like any other in class 22.
    v_response := api.rest_invoke('GET', '/decode-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 400 THEN
        RAISE EXCEPTION 'TEST FAILED: invalid percent-encoding must be 400, got %', (v_response).status_code;
    END IF;

    -- RFC 9110 5.1: field names are case-insensitive. A canonical-case Accept
    -- used to be ignored, so an unacceptable type was served anyway.
    v_response := api.rest_invoke('GET', '/gateway-probe',
        extensions.hstore('Accept', 'text/html'), NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 406 THEN
        RAISE EXCEPTION 'TEST FAILED: a canonical-case Accept header must be honoured (406), got %', (v_response).status_code;
    END IF;

    -- RFC 9111 4.1: the handler's own Vary members must survive the gateway's.
    v_response := api.rest_invoke('GET', '/gateway-probe', ''::extensions.hstore, NULL::bytea);
    v_vary := (v_response).headers->'vary';
    IF COALESCE(v_vary, '') NOT LIKE '%accept-language%' OR COALESCE(v_vary, '') NOT LIKE '%cookie%' THEN
        RAISE EXCEPTION 'TEST FAILED: Vary must union handler and gateway members, got %', v_vary;
    END IF;

    RAISE NOTICE '  ✓ bad q-values answered, class 22 is 400, header names case-insensitive, Vary unioned';
END $$;

-- A CORS preflight carries no credentials and needs a 2xx, so OPTIONS on a
-- matched path answers 204 with Allow before identity is resolved. Every other
-- method answers only from the routes its caller may call: an anonymous
-- caller must not learn the methods of an authenticated resource from a 405.
DO $$
DECLARE
    v_response api.http_response;
    v_headers  extensions.hstore;
BEGIN
    RAISE NOTICE '→ Testing OPTIONS preflight and the public method set';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-5004-4000-8000-000000000001',
            'path', '/private-probe',
            'httpMethod', '^GET$',
            'name', 'private_probe_read',
            'access', 'public'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-5004-4000-8000-000000000002',
            'path', '/private-probe',
            'httpMethod', '^DELETE$',
            'name', 'private_probe_delete',
            'access', 'authenticated'
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );

    v_response := api.rest_invoke('OPTIONS', '/private-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 204 THEN
        RAISE EXCEPTION 'TEST FAILED: OPTIONS on a matched path must be 204 without credentials, got %',
            (v_response).status_code;
    END IF;
    IF (v_response).headers->'allow' IS DISTINCT FROM 'DELETE, GET, HEAD, OPTIONS' THEN
        RAISE EXCEPTION 'TEST FAILED: anonymous OPTIONS must list every method of the path, got %',
            coalesce((v_response).headers->'allow', '<none>');
    END IF;

    v_response := api.rest_invoke('OPTIONS', '/no-such-resource', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'TEST FAILED: OPTIONS on an unmatched path must stay 404, got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('PUT', '/private-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 405
       OR (v_response).headers->'allow' IS DISTINCT FROM 'DELETE, GET, HEAD, OPTIONS' THEN
        RAISE EXCEPTION 'TEST FAILED: an anonymous 405 must list the same full method set as OPTIONS, got % Allow: %',
            (v_response).status_code, coalesce((v_response).headers->'allow', '<none>');
    END IF;

    v_response := api.rest_invoke('DELETE', '/private-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: an anonymous call to the protected method must be 401, got %',
            (v_response).status_code;
    END IF;

    PERFORM pg_temp.test_login('test', 'http-semantics-options', 'http-semantics-options@example.test', NULL, true);
    v_headers := pg_temp.test_session_headers('test|http-semantics-options');
    v_response := api.rest_invoke('PUT', '/private-probe', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 405
       OR (v_response).headers->'allow' IS DISTINCT FROM 'DELETE, GET, HEAD, OPTIONS' THEN
        RAISE EXCEPTION 'TEST FAILED: an authenticated 405 must list the same method set, got % Allow: %',
            (v_response).status_code, coalesce((v_response).headers->'allow', '<none>');
    END IF;

    RAISE NOTICE '  ✓ OPTIONS answers pre-auth; 405 and OPTIONS list the full method set';
END $$;

-- Client mistakes in the URL are 4xx, never a 500 or another code: invalid
-- UTF-8 once decoded (a typed path parameter, a query checked against a
-- querySchema), an {n:int} past integer, and digits that are not ASCII.
DO $$
DECLARE
    v_response api.http_response;
    v_case     record;
    v_icu_ok   boolean;
BEGIN
    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'f6380000-0000-4000-8000-000000000001', 'path', '/url-probe/{n:int}', 'httpMethod', '^GET$',
        'name', 'url_probe_int', 'access', 'public', 'outputSchema', jsonb_build_object('type', 'object')),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'f6380000-0000-4000-8000-000000000002', 'path', '/url-probe-id/{id:uuid}', 'httpMethod', '^GET$',
        'name', 'url_probe_uuid', 'access', 'public', 'outputSchema', jsonb_build_object('type', 'object')),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'f6380000-0000-4000-8000-000000000003', 'path', '/url-probe-query', 'httpMethod', '^GET$',
        'name', 'url_probe_query', 'access', 'public', 'outputSchema', jsonb_build_object('type', 'object'),
        'querySchema', jsonb_build_object('type', 'object',
            'properties', jsonb_build_object('x', jsonb_build_object('type', 'string')))),
        $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);

    FOR v_case IN
        SELECT * FROM (VALUES
            ('/url-probe-id/%FF',           400, 'pgmi.invalid_encoding'),
            ('/url-probe-query?x=%FF',      400, 'pgmi.invalid_encoding'),
            ('/url-probe/99999999999999',   400, 'pgmi.invalid_path_parameter'),
            ('/url-probe/%FF',              400, 'pgmi.invalid_encoding'),
            ('/url-probe/12',               200, NULL)
        ) AS t(url, status, code)
    LOOP
        v_response := api.rest_invoke('GET', v_case.url);
        IF (v_response).status_code IS DISTINCT FROM v_case.status
           OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM v_case.code THEN
            RAISE EXCEPTION 'TEST FAILED: GET % should answer % %, got % %', v_case.url, v_case.status, v_case.code,
                (v_response).status_code, convert_from((v_response).content, 'UTF8');
        END IF;
    END LOOP;

    IF common.try_cast('١٢', NULL::integer) IS NOT NULL OR common.try_cast('١٢', NULL::bigint) IS NOT NULL
       OR common.try_cast('١٢', 7) IS DISTINCT FROM 7 THEN
        RAISE EXCEPTION 'TEST FAILED: non-ASCII digits are not an integer for try_cast';
    END IF;
    -- Under an ICU collation \d takes Arabic-Indic digits; the default
    -- database collation may not show it.
    IF EXISTS (SELECT FROM pg_collation WHERE collname = 'und-x-icu') THEN
        EXECUTE $q$SELECT common.try_cast('١٢' COLLATE "und-x-icu", NULL::integer) IS NULL
                      AND common.try_cast('١٢' COLLATE "und-x-icu", NULL::bigint) IS NULL$q$ INTO v_icu_ok;
        IF v_icu_ok IS NOT TRUE THEN
            RAISE EXCEPTION 'TEST FAILED: under ICU, non-ASCII digits are not an integer for try_cast';
        END IF;
    END IF;
    RAISE NOTICE '  ✓ URL mistakes are 400s: invalid UTF-8, an overflowing int, non-ASCII digits';
END $$;
