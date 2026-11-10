-- ============================================================================
-- Membership test fixture, written through the kernels:
--   owner (test|owner-001) - verified, owns Test Corp
--   alice (test|alice-001) - verified, org.member of Test Corp
--   bob   (test|bob-001)   - verified, no memberships
--   una   (test|una-001)   - email NOT verified, no memberships
-- Session variables: test.owner_id, test.alice_id, test.bob_id, test.una_id,
-- test.corp_id, test.alice_member_id
-- ============================================================================

INSERT INTO membership.identity_issuer (id, kind) VALUES ('test', 'development'), ('other-idp', 'development'), ('https://idp.example.com', 'oidc')
ON CONFLICT (id) DO NOTHING;

DO $$
DECLARE
    v_owner uuid := pg_temp.test_login('test', 'owner-001', 'owner@example.test', 'Owner', true);
    v_alice uuid := pg_temp.test_login('test', 'alice-001', 'alice@example.test', 'Alice', true);
    v_bob   uuid := pg_temp.test_login('test', 'bob-001', 'bob@example.test', 'Bob', true);
    v_una   uuid := pg_temp.test_login('test', 'una-001', 'una@example.test', 'Una', false);
    v_corp  uuid := (membership.fn_create_organization_with_owner('Test Corp', 'test-corp', v_owner)).object_id;
BEGIN
    PERFORM set_config('test.owner_id', v_owner::text, true);
    PERFORM set_config('test.alice_id', v_alice::text, true);
    PERFORM set_config('test.bob_id', v_bob::text, true);
    PERFORM set_config('test.una_id', v_una::text, true);
    PERFORM set_config('test.corp_id', v_corp::text, true);
    PERFORM set_config('test.alice_member_id',
        (membership.fn_add_member(v_corp, v_alice, 'org.member')).object_id::text, true);
END $$;
