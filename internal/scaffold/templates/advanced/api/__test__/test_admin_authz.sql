-- ============================================================================
-- Test: /admin/* Authorization (platform-admin gate)
-- ============================================================================
-- /admin/* endpoints expose cross-tenant operational data (every tenant's
-- request headers, bodies, replay SQL) and a destructive purge. They must be
-- reachable only by a platform admin — a user holding the system-wide
-- 'superuser' role — never by an ordinary authenticated tenant user.
--
-- Reuses the fixture (Bob is a plain member). Provisions one admin user here.
-- ============================================================================

DO $$
DECLARE
    v_bob_subject   TEXT := current_setting('test.bob_subject');
    v_admin_subject TEXT := 'google|admin-api-test';
    v_admin_id      UUID;
    v_superuser     UUID;
    v_headers       extensions.hstore;
    v_response      api.http_response;
BEGIN
    RAISE DEBUG '→ Testing /admin/* authorization';

    -- Provision a platform admin: a user holding the system-wide superuser role
    v_admin_id := membership.upsert_user('google', 'admin-api-test', 'admin@acme.com', 'Admin User', true);
    SELECT object_id INTO v_superuser FROM membership.role WHERE name = 'superuser';
    INSERT INTO membership.user_role (user_object_id, role_object_id)
    VALUES (v_admin_id, v_superuser)
    ON CONFLICT DO NOTHING;

    -- ════════════════════════════════════════════════════════════════════
    -- Authenticated non-admin (Bob) → 403 on every admin surface
    -- ════════════════════════════════════════════════════════════════════
    PERFORM set_config('auth.idp_subject', v_bob_subject, true);
    v_headers := ('x-user-id=>' || v_bob_subject)::extensions.hstore;

    v_response := api.rest_invoke('GET', '/admin/dashboard', v_headers, NULL::bytea);
    IF (v_response).status_code != 403 THEN
        RAISE EXCEPTION 'TEST FAILED: non-admin GET /admin/dashboard expected 403, got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/admin/exchanges', v_headers, NULL::bytea);
    IF (v_response).status_code != 403 THEN
        RAISE EXCEPTION 'TEST FAILED: non-admin GET /admin/exchanges expected 403, got %', (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('POST', '/admin/maintenance/purge-exchanges', v_headers, NULL::bytea);
    IF (v_response).status_code != 403 THEN
        RAISE EXCEPTION 'TEST FAILED: non-admin POST purge-exchanges expected 403, got %', (v_response).status_code;
    END IF;

    RAISE DEBUG '  ✓ Authenticated non-admin → 403 on dashboard, exchanges, purge';

    -- ════════════════════════════════════════════════════════════════════
    -- Unauthenticated → 401 (gateway gate, before the body runs)
    -- ════════════════════════════════════════════════════════════════════
    v_response := api.rest_invoke('GET', '/admin/dashboard', ''::extensions.hstore, NULL::bytea);
    IF (v_response).status_code != 401 THEN
        RAISE EXCEPTION 'TEST FAILED: unauthenticated /admin/dashboard expected 401, got %', (v_response).status_code;
    END IF;

    RAISE DEBUG '  ✓ Unauthenticated → 401';

    -- ════════════════════════════════════════════════════════════════════
    -- Admin perspective
    -- ════════════════════════════════════════════════════════════════════
    PERFORM set_config('auth.idp_subject', v_admin_subject, true);
    v_headers := ('x-user-id=>' || v_admin_subject)::extensions.hstore;

    -- purge is POST-only: a destructive GET must not match any route → 404
    v_response := api.rest_invoke('GET', '/admin/maintenance/purge-exchanges', v_headers, NULL::bytea);
    IF (v_response).status_code != 404 THEN
        RAISE EXCEPTION 'TEST FAILED: GET purge-exchanges expected 404 (POST-only), got %', (v_response).status_code;
    END IF;

    -- admin sees the dashboard
    v_response := api.rest_invoke('GET', '/admin/dashboard', v_headers, NULL::bytea);
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: admin GET /admin/dashboard expected 200, got %', (v_response).status_code;
    END IF;

    -- admin can purge via POST
    v_response := api.rest_invoke('POST', '/admin/maintenance/purge-exchanges', v_headers, NULL::bytea);
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: admin POST purge-exchanges expected 200, got %', (v_response).status_code;
    END IF;

    RAISE DEBUG '  ✓ Admin → 200 on dashboard + purge; destructive GET → 404';

    RAISE DEBUG '✓ /admin/* authorization tests passed';
END $$;
