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
    PERFORM pg_temp.expect_status('non-admin GET /admin/dashboard', v_response, 403);

    v_response := api.rest_invoke('GET', '/admin/exchanges', v_headers, NULL::bytea);
    PERFORM pg_temp.expect_status('non-admin GET /admin/exchanges', v_response, 403);

    v_response := api.rest_invoke('POST', '/admin/maintenance/purge-exchanges', v_headers, NULL::bytea);
    PERFORM pg_temp.expect_status('non-admin POST purge-exchanges', v_response, 403);

    RAISE DEBUG '  ✓ Authenticated non-admin → 403 on dashboard, exchanges, purge';

    -- ════════════════════════════════════════════════════════════════════
    -- Unauthenticated → 401 (gateway gate, before the body runs)
    -- ════════════════════════════════════════════════════════════════════
    v_response := api.rest_invoke('GET', '/admin/dashboard', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('unauthenticated /admin/dashboard', v_response, 401);

    RAISE DEBUG '  ✓ Unauthenticated → 401';

    -- ════════════════════════════════════════════════════════════════════
    -- Admin perspective
    -- ════════════════════════════════════════════════════════════════════
    -- A platform operator acts as one only after a second factor.
    v_headers := pg_temp.test_session_headers(v_admin_subject);
    v_response := api.rest_invoke('GET', '/admin/dashboard', v_headers, NULL::bytea);
    PERFORM pg_temp.expect_status('operator without MFA GET /admin/dashboard', v_response, 403);
    RAISE DEBUG '  ✓ Operator without MFA → 403';

    v_headers := pg_temp.test_session_headers(v_admin_subject, NULL, ARRAY['pwd', 'mfa']);

    -- purge is POST-only: a destructive GET must not reach the handler. The
    -- resource exists, so RFC 9110 §15.5.6 makes this 405 + Allow, not 404.
    v_response := api.rest_invoke('GET', '/admin/maintenance/purge-exchanges', v_headers, NULL::bytea);
    PERFORM pg_temp.expect_status('GET purge-exchanges expected 405 (POST-only)', v_response, 405);
    IF ((v_response).headers->'allow') IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: 405 must carry an Allow header (RFC 9110 15.5.6)';
    END IF;
    IF (((v_response).headers->'allow') !~ 'POST') IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: Allow must advertise POST, got %', (v_response).headers->'allow';
    END IF;

    -- admin sees the dashboard
    v_response := api.rest_invoke('GET', '/admin/dashboard', v_headers, NULL::bytea);
    PERFORM pg_temp.expect_status('admin GET /admin/dashboard', v_response, 200);

    -- admin can purge via POST
    v_response := api.rest_invoke('POST', '/admin/maintenance/purge-exchanges', v_headers, NULL::bytea);
    PERFORM pg_temp.expect_status('admin POST purge-exchanges', v_response, 200);

    RAISE DEBUG '  ✓ Admin → 200 on dashboard + purge; destructive GET → 404';

    RAISE DEBUG '✓ /admin/* authorization tests passed';
END $$;
