-- ============================================================================
-- API Test Fixture: people and an organization, written through the kernels
-- ============================================================================
--   Alice (google|alice-api-test) - owns "Acme Corp"
--   Bob   (github|bob-api-test)   - org.member of Acme Corp
--
-- Session variables set by this fixture:
--   test.alice_subject   ->  'google|alice-api-test'
--   test.bob_subject     ->  'github|bob-api-test'
--   test.acme_org_id     ->  UUID of Acme Corp
-- Two OIDC issuers for the sign-in tests: https://idp.example.com and
-- https://other-idp.example.com.
-- ============================================================================

INSERT INTO membership.identity_issuer (id, kind)
VALUES ('google', 'development'), ('github', 'development'), ('samlp', 'development'),
       ('https://idp.example.com', 'oidc'), ('https://other-idp.example.com', 'oidc')
ON CONFLICT (id) DO NOTHING;

DO $$
DECLARE
    v_alice uuid := pg_temp.test_login('google', 'alice-api-test', 'alice@acme.com', 'Alice Chen', true);
    v_bob   uuid := pg_temp.test_login('github', 'bob-api-test', 'bob@acme.com', 'Bob Park', true);
    v_acme  uuid := (membership.fn_create_organization_with_owner('Acme Corp', 'acme-corp', v_alice)).object_id;
BEGIN
    PERFORM membership.fn_add_member(v_acme, v_bob, 'org.member');

    PERFORM set_config('test.alice_subject', 'google|alice-api-test', true);
    PERFORM set_config('test.bob_subject', 'github|bob-api-test', true);
    PERFORM set_config('test.acme_org_id', v_acme::text, true);
END $$;
