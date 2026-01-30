-- ============================================================================
-- API Test Fixture: Pre-Provisioned Users and Organizations
-- ============================================================================
-- Creates a realistic scenario for testing authenticated API endpoints:
--
--   Alice (owner)  ─┬─ Personal org (auto-created)
--                   └─ "Acme Corp" org (team org, Alice is admin)
--                          └── Bob (contributor member)
--
--   Bob (member)   ─── Personal org (auto-created)
--
-- Session variables set by this fixture:
--   test.alice_subject   →  'google|alice-api-test'
--   test.bob_subject     →  'github|bob-api-test'
--   test.acme_org_id     →  UUID of Acme Corp
-- ============================================================================

DO $$
DECLARE
    v_alice_id UUID;
    v_bob_id UUID;
    v_acme_id UUID;
BEGIN
    RAISE DEBUG '→ Setting up API test fixtures';

    v_alice_id := membership.upsert_user('google', 'alice-api-test', 'alice@acme.com', 'Alice Chen', true);
    v_bob_id := membership.upsert_user('github', 'bob-api-test', 'bob@acme.com', 'Bob Park', true);

    INSERT INTO membership.organization (name, slug, owner_user_id, is_personal)
    VALUES ('Acme Corp', 'acme-corp', v_alice_id, false)
    RETURNING object_id INTO v_acme_id;

    INSERT INTO membership.organization_member (organization_id, user_id, role, status, joined_at)
    VALUES
        (v_acme_id, v_alice_id, 'admin', 'active', now()),
        (v_acme_id, v_bob_id, 'contributor', 'active', now());

    PERFORM set_config('test.alice_subject', 'google|alice-api-test', true);
    PERFORM set_config('test.bob_subject', 'github|bob-api-test', true);
    PERFORM set_config('test.acme_org_id', v_acme_id::TEXT, true);

    RAISE DEBUG '  ✓ Alice: % (google|alice-api-test) — owns Acme Corp', v_alice_id;
    RAISE DEBUG '  ✓ Bob:   % (github|bob-api-test)   — contributor at Acme Corp', v_bob_id;
    RAISE DEBUG '  ✓ Acme Corp: %', v_acme_id;
END $$;
