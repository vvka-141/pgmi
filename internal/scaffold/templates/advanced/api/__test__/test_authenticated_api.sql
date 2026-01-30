-- ============================================================================
-- Test: Authenticated REST API From Different User Perspectives
-- ============================================================================
-- Uses the pre-provisioned fixture (Alice owns Acme Corp, Bob is a member)
-- to verify that each user sees exactly what they should through the API.
--
-- Pattern: set auth context → call endpoint → assert response.
-- Copy this test and adapt it for your own authenticated endpoints.
-- ============================================================================

DO $$
DECLARE
    v_alice_subject TEXT := current_setting('test.alice_subject');
    v_bob_subject TEXT := current_setting('test.bob_subject');
    v_acme_org_id UUID := current_setting('test.acme_org_id')::UUID;
    v_headers extensions.hstore;
    v_response api.http_response;
    v_body jsonb;
    v_org_ids jsonb;
BEGIN
    RAISE DEBUG '→ Testing authenticated API (multi-user perspectives)';

    -- ════════════════════════════════════════════════════════════════════
    -- Alice's perspective (owner of Acme Corp + personal org)
    -- ════════════════════════════════════════════════════════════════════

    PERFORM set_config('auth.idp_subject', v_alice_subject, true);
    v_headers := ('x-user-id=>' || v_alice_subject)::extensions.hstore;

    -- GET /me — Alice sees her own profile
    v_response := api.rest_invoke('GET', '/me', v_headers, NULL::bytea);

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: Alice GET /me expected 200, got %', (v_response).status_code;
    END IF;

    v_body := api.content_json((v_response).content);

    IF v_body->>'email' != 'alice@acme.com' THEN
        RAISE EXCEPTION 'TEST FAILED: Alice email mismatch: %', v_body->>'email';
    END IF;

    RAISE DEBUG '  ✓ Alice GET /me → correct profile';

    -- GET /organizations — Alice sees personal org + Acme Corp (2 orgs)
    v_response := api.rest_invoke('GET', '/organizations', v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);

    IF jsonb_array_length(v_body->'organizations') != 2 THEN
        RAISE EXCEPTION 'TEST FAILED: Alice should see 2 orgs, got %',
            jsonb_array_length(v_body->'organizations');
    END IF;

    RAISE DEBUG '  ✓ Alice GET /organizations → 2 orgs (personal + Acme Corp)';

    -- ════════════════════════════════════════════════════════════════════
    -- Bob's perspective (member of Acme Corp + personal org)
    -- ════════════════════════════════════════════════════════════════════

    PERFORM set_config('auth.idp_subject', v_bob_subject, true);
    v_headers := ('x-user-id=>' || v_bob_subject)::extensions.hstore;

    -- GET /me — Bob sees his own profile (not Alice's)
    v_response := api.rest_invoke('GET', '/me', v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);

    IF v_body->>'email' != 'bob@acme.com' THEN
        RAISE EXCEPTION 'TEST FAILED: Bob email mismatch: %', v_body->>'email';
    END IF;

    RAISE DEBUG '  ✓ Bob GET /me → correct profile (not Alice)';

    -- GET /organizations — Bob also sees 2 orgs (personal + Acme Corp)
    v_response := api.rest_invoke('GET', '/organizations', v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);

    IF jsonb_array_length(v_body->'organizations') != 2 THEN
        RAISE EXCEPTION 'TEST FAILED: Bob should see 2 orgs, got %',
            jsonb_array_length(v_body->'organizations');
    END IF;

    RAISE DEBUG '  ✓ Bob GET /organizations → 2 orgs (personal + Acme Corp)';

    -- ════════════════════════════════════════════════════════════════════
    -- Unauthenticated — 401 on protected endpoints
    -- ════════════════════════════════════════════════════════════════════

    v_response := api.rest_invoke('GET', '/me', ''::extensions.hstore, NULL::bytea);

    IF (v_response).status_code != 401 THEN
        RAISE EXCEPTION 'TEST FAILED: unauthenticated GET /me expected 401, got %',
            (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/organizations', ''::extensions.hstore, NULL::bytea);

    IF (v_response).status_code != 401 THEN
        RAISE EXCEPTION 'TEST FAILED: unauthenticated GET /organizations expected 401, got %',
            (v_response).status_code;
    END IF;

    RAISE DEBUG '  ✓ Unauthenticated requests → 401';

    RAISE DEBUG '✓ Authenticated REST API tests passed';
END $$;
