-- ============================================================================
-- Test: identity resolution through the gateway cannot be hijacked
-- ============================================================================
-- Two ways a request used to resolve to someone else's account:
--   1. A new identity presenting an existing account's email was linked to it,
--      verified or not -- whoever could claim the address signed in as its
--      owner.
--   2. The subject was cut at its second pipe, so every user of an Auth0-style
--      enterprise connection (samlp|<connection>|<user>) became one identity.
-- ============================================================================

DO $$
DECLARE
    v_victim_id uuid;
    v_response api.http_response;
    v_alice_id text;
    v_bob_id text;
BEGIN
    RAISE DEBUG '→ Testing that a matching email does not hand over an account';

    v_victim_id := membership.upsert_user('google', 'victim-1', 'victim@example.com', 'Victim', true);

    v_response := api.rest_invoke('GET', '/me',
        'x-user-id=>"github|attacker-9", x-user-email=>"victim@example.com"'::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 401 THEN
        RAISE EXCEPTION 'TEST FAILED: an identity presenting another account''s email must not sign in as it, got % %',
            (v_response).status_code, convert_from((v_response).content, 'UTF8');
    END IF;
    IF EXISTS (SELECT 1 FROM membership.user_identity
               WHERE idp_provider = 'github' AND idp_subject_id = 'attacker-9') THEN
        RAISE EXCEPTION 'TEST FAILED: the attacker identity was linked to an account';
    END IF;
    RAISE DEBUG '  ✓ matching email without verification → 401, nothing linked';

    RAISE DEBUG '→ Testing that users of one enterprise connection stay distinct';

    v_response := api.rest_invoke('GET', '/me',
        'x-user-id=>"samlp|acme-sso|alice", x-user-email=>"alice@acme-sso.example"'::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: samlp alice GET /me expected 200, got %', (v_response).status_code;
    END IF;
    v_alice_id := api.content_json((v_response).content)->>'userId';

    v_response := api.rest_invoke('GET', '/me',
        'x-user-id=>"samlp|acme-sso|bob", x-user-email=>"bob@acme-sso.example"'::extensions.hstore, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: samlp bob GET /me expected 200, got %', (v_response).status_code;
    END IF;
    v_bob_id := api.content_json((v_response).content)->>'userId';

    IF v_alice_id IS NULL OR v_bob_id IS NULL OR v_alice_id = v_bob_id THEN
        RAISE EXCEPTION 'TEST FAILED: two users of one connection resolved to the same account (% / %)', v_alice_id, v_bob_id;
    END IF;
    IF api.content_json((v_response).content)->>'email' IS DISTINCT FROM 'bob@acme-sso.example' THEN
        RAISE EXCEPTION 'TEST FAILED: bob resolved to another user''s profile: %', convert_from((v_response).content, 'UTF8');
    END IF;
    RAISE DEBUG '  ✓ samlp|acme-sso|alice and samlp|acme-sso|bob are distinct users';
END $$;
