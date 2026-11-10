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
    -- Alice's perspective (owner of Acme Corp)
    -- ════════════════════════════════════════════════════════════════════

    v_headers := pg_temp.test_session_headers(v_alice_subject);

    -- GET /me — Alice sees her own profile
    v_response := api.rest_invoke('GET', '/me', v_headers, NULL::bytea);

    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: Alice GET /me expected 200, got %', (v_response).status_code;
    END IF;

    v_body := api.content_json((v_response).content);

    IF v_body->>'email' IS DISTINCT FROM 'alice@acme.com' THEN
        RAISE EXCEPTION 'TEST FAILED: Alice email mismatch: %', v_body->>'email';
    END IF;

    RAISE DEBUG '  ✓ Alice GET /me → correct profile';

    -- GET /organizations — Alice sees Acme Corp only: signing in creates none
    v_response := api.rest_invoke('GET', '/organizations', v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);

    IF v_body->'organizations'->0->>'id' IS DISTINCT FROM v_acme_org_id::text
       OR jsonb_array_length(v_body->'organizations') IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'TEST FAILED: Alice should see Acme Corp only, got %', v_body;
    END IF;

    RAISE DEBUG '  ✓ Alice GET /organizations → Acme Corp';

    -- ════════════════════════════════════════════════════════════════════
    -- Bob's perspective (member of Acme Corp)
    -- ════════════════════════════════════════════════════════════════════

    v_headers := pg_temp.test_session_headers(v_bob_subject);

    -- GET /me — Bob sees his own profile (not Alice's)
    v_response := api.rest_invoke('GET', '/me', v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);

    IF v_body->>'email' IS DISTINCT FROM 'bob@acme.com' THEN
        RAISE EXCEPTION 'TEST FAILED: Bob email mismatch: %', v_body->>'email';
    END IF;

    RAISE DEBUG '  ✓ Bob GET /me → correct profile (not Alice)';

    -- GET /organizations — Bob sees Acme Corp, where he is a member
    v_response := api.rest_invoke('GET', '/organizations', v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);

    IF v_body->'organizations'->0->>'id' IS DISTINCT FROM v_acme_org_id::text
       OR jsonb_array_length(v_body->'organizations') IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'TEST FAILED: Bob should see Acme Corp only, got %', v_body;
    END IF;

    RAISE DEBUG '  ✓ Bob GET /organizations → Acme Corp';

    -- ════════════════════════════════════════════════════════════════════
    -- Unauthenticated — 401 on protected endpoints
    -- ════════════════════════════════════════════════════════════════════

    v_response := api.rest_invoke('GET', '/me', ''::extensions.hstore, NULL::bytea);

    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: unauthenticated GET /me expected 401, got %',
            (v_response).status_code;
    END IF;

    v_response := api.rest_invoke('GET', '/organizations', ''::extensions.hstore, NULL::bytea);

    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: unauthenticated GET /organizations expected 401, got %',
            (v_response).status_code;
    END IF;

    RAISE DEBUG '  ✓ Unauthenticated requests → 401';

    RAISE DEBUG '✓ Authenticated REST API tests passed';
END $$;
