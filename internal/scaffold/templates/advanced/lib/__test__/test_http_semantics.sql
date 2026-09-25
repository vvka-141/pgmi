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
            'uri', '^/parse-probe$',
            'httpMethod', '^POST$',
            'name', 'parse_probe',
            'requiresAuth', false
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
            'uri', '^/method-probe$',
            'httpMethod', '^POST$',
            'name', 'method_probe_405',
            'requiresAuth', false
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
    IF ((v_response).headers->'allow') !~ 'POST' THEN
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
            'uri', '^/head-probe$',
            'httpMethod', '^GET$',
            'name', 'head_probe',
            'requiresAuth', false
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
    IF COALESCE(((v_response).headers->'content-length')::int, 0) = 0 THEN
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
            'uri', '^/guarded$',
            'httpMethod', '^GET$',
            'name', 'guarded_probe',
            'requiresAuth', true
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

    -- Identity travels in x-user-id, not Authorization, so RFC 9111 §3.5's
    -- shared-cache prohibition never engages. Without Vary a caching
    -- intermediary may replay one user''s response to another.
    IF ((v_response).headers->'vary') IS NULL
       OR ((v_response).headers->'vary') !~ 'x-user-id' THEN
        RAISE EXCEPTION 'TEST FAILED: responses must Vary on x-user-id, got %',
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
            'requiresAuth', false
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
            'uri', '^/gateway-probe$',
            'httpMethod', '^GET$',
            'name', 'gateway_probe',
            'produces', jsonb_build_array('application/json'),
            'requiresAuth', false
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
            'uri', '^/decode-probe$',
            'httpMethod', '^GET$',
            'name', 'decode_probe',
            'requiresAuth', false
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
    IF COALESCE(v_vary, '') NOT LIKE '%accept-language%' OR COALESCE(v_vary, '') NOT LIKE '%x-user-id%' THEN
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
    v_subject  text := 'test|http-semantics-options';
    v_headers  extensions.hstore := extensions.hstore('x-user-id', 'test|http-semantics-options');
BEGIN
    RAISE NOTICE '→ Testing OPTIONS preflight and the anonymous 405';

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'ffffffff-5004-4000-8000-000000000001',
            'uri', '^/private-probe$',
            'httpMethod', '^GET$',
            'name', 'private_probe',
            'requiresAuth', true
        ),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('ok', true)); END; $body$
    );

    PERFORM set_config('auth.idp_subject', '', true);
    v_response := api.rest_invoke('OPTIONS', '/private-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 204 THEN
        RAISE EXCEPTION 'TEST FAILED: OPTIONS on a matched path must be 204 without credentials, got %',
            (v_response).status_code;
    END IF;
    IF coalesce((v_response).headers->'allow', '') !~ 'GET' OR coalesce((v_response).headers->'allow', '') !~ 'OPTIONS' THEN
        RAISE EXCEPTION 'TEST FAILED: OPTIONS must advertise GET and OPTIONS, got %',
            coalesce((v_response).headers->'allow', '<none>');
    END IF;

    v_response := api.rest_invoke('OPTIONS', '/no-such-resource', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'TEST FAILED: OPTIONS on an unmatched path must stay 404, got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('POST', '/private-probe', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: an anonymous wrong method on an authenticated resource must be 401, got %',
            (v_response).status_code;
    END IF;
    IF (v_response).headers ? 'allow' THEN
        RAISE EXCEPTION 'TEST FAILED: the anonymous answer must not list methods, got Allow: %',
            (v_response).headers->'allow';
    END IF;

    PERFORM membership.upsert_user('test', 'http-semantics-options', 'http-semantics-options@example.com');
    PERFORM set_config('auth.idp_subject', v_subject, true);
    v_response := api.rest_invoke('POST', '/private-probe', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 405 THEN
        RAISE EXCEPTION 'TEST FAILED: an authenticated wrong method must be 405, got %', (v_response).status_code;
    END IF;
    IF coalesce((v_response).headers->'allow', '') !~ 'GET' THEN
        RAISE EXCEPTION 'TEST FAILED: the authenticated 405 must list GET, got %',
            coalesce((v_response).headers->'allow', '<none>');
    END IF;
    PERFORM set_config('auth.idp_subject', '', true);

    RAISE NOTICE '  ✓ OPTIONS answers pre-auth; an anonymous 405 reveals nothing';
END $$;
