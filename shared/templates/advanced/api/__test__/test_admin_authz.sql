-- ============================================================================
-- Test: /admin/* Authorization (platform-admin gate)
-- ============================================================================
-- /admin/* endpoints expose cross-tenant operational data (every tenant's
-- request headers, bodies, replay SQL) and a destructive purge. They must be
-- reachable only by a platform operator, never by an ordinary member.
--
-- Reuses the fixture (Bob is a plain member). Provisions one admin user here.
-- ============================================================================

DO $$
DECLARE
    v_bob_subject   TEXT := current_setting('test.bob_subject');
    v_admin_subject TEXT := 'google|admin-api-test';
    v_admin_id      UUID;
    v_headers       extensions.hstore;
    v_response      api.http_response;
BEGIN
    RAISE DEBUG '→ Testing /admin/* authorization';

    v_admin_id := pg_temp.test_login('google', 'admin-api-test', 'admin@acme.com', 'Admin User', true);
    PERFORM pg_temp.test_operator(v_admin_id);

    -- ════════════════════════════════════════════════════════════════════
    -- Authenticated non-admin (Bob) → 403 on every admin surface
    -- ════════════════════════════════════════════════════════════════════
    v_headers := pg_temp.test_session_headers(v_bob_subject);

    v_response := api.rest_invoke('GET', '/admin/dashboard', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 403 THEN
        RAISE EXCEPTION 'TEST FAILED: non-admin GET /admin/dashboard expected 403, got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/admin/exchanges', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 403 THEN
        RAISE EXCEPTION 'TEST FAILED: non-admin GET /admin/exchanges expected 403, got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('POST', '/admin/maintenance/purge-exchanges', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 403 THEN
        RAISE EXCEPTION 'TEST FAILED: non-admin POST purge-exchanges expected 403, got %', (v_response).status_code;
    END IF;

    RAISE DEBUG '  ✓ Authenticated non-admin → 403 on dashboard, exchanges, purge';

    -- ════════════════════════════════════════════════════════════════════
    -- Unauthenticated → 401 (gateway gate, before the body runs)
    -- ════════════════════════════════════════════════════════════════════
    v_response := api.rest_invoke('GET', '/admin/dashboard', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: unauthenticated /admin/dashboard expected 401, got %', (v_response).status_code;
    END IF;

    RAISE DEBUG '  ✓ Unauthenticated → 401';

    -- ════════════════════════════════════════════════════════════════════
    -- Admin perspective
    -- ════════════════════════════════════════════════════════════════════
    -- A platform operator acts as one only after a second factor.
    v_headers := pg_temp.test_session_headers(v_admin_subject);
    v_response := api.rest_invoke('GET', '/admin/dashboard', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 403 THEN
        RAISE EXCEPTION 'TEST FAILED: operator without MFA GET /admin/dashboard expected 403, got %', (v_response).status_code;
    END IF;
    RAISE DEBUG '  ✓ Operator without MFA → 403';

    v_headers := pg_temp.test_session_headers(v_admin_subject, NULL, ARRAY['pwd', 'mfa']);

    -- purge is POST-only: a destructive GET must not reach the handler. The
    -- resource exists, so RFC 9110 §15.5.6 makes this 405 + Allow, not 404.
    v_response := api.rest_invoke('GET', '/admin/maintenance/purge-exchanges', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 405 THEN
        RAISE EXCEPTION 'TEST FAILED: GET purge-exchanges expected 405 (POST-only), got %', (v_response).status_code;
    END IF;
    IF ((v_response).headers->'allow') IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: 405 must carry an Allow header (RFC 9110 15.5.6)';
    END IF;
    IF (((v_response).headers->'allow') !~ 'POST') IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: Allow must advertise POST, got %', (v_response).headers->'allow';
    END IF;

    -- admin sees the dashboard
    v_response := api.rest_invoke('GET', '/admin/dashboard', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: admin GET /admin/dashboard expected 200, got %', (v_response).status_code;
    END IF;

    -- admin can purge via POST
    v_response := api.rest_invoke('POST', '/admin/maintenance/purge-exchanges', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: admin POST purge-exchanges expected 200, got %', (v_response).status_code;
    END IF;

    RAISE DEBUG '  ✓ Admin → 200 on dashboard + purge; destructive GET → 404';

    RAISE DEBUG '✓ /admin/* authorization tests passed';
END $$;
