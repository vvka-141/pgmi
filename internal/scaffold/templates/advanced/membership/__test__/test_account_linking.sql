-- ============================================================================
-- Test: Account linking via verified email
-- ============================================================================

DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
    v_linked_id UUID;
    v_identity_count BIGINT;
    v_unverified_id UUID;
    v_unverified_user_id UUID;
BEGIN
    RAISE DEBUG '→ Testing account linking';

    v_linked_id := api.upsert_user('azure-ad', 'alice-azure-001', 'alice@example.com', 'Alice Azure', false);
    IF v_linked_id != v_alice_id THEN
        RAISE EXCEPTION 'TEST FAILED: auto-link should return existing user %, got %', v_alice_id, v_linked_id;
    END IF;
    RAISE DEBUG '  ✓ Auto-linked azure-ad identity to existing verified user';

    SELECT count(*) INTO v_identity_count
    FROM membership.user_identity WHERE user_object_id = v_alice_id;
    IF v_identity_count != 2 THEN
        RAISE EXCEPTION 'TEST FAILED: expected 2 identities, got %', v_identity_count;
    END IF;
    RAISE DEBUG '  ✓ User has 2 linked identities';

    v_unverified_id := api.upsert_user('local', 'unverified-001', 'unverified@example.com', 'Unverified', false);
    v_unverified_user_id := api.upsert_user('google', 'unverified-google', 'unverified@example.com', 'Unverified Google', false);
    IF v_unverified_user_id != v_unverified_id THEN
        RAISE EXCEPTION 'TEST FAILED: same email should link to existing user';
    END IF;
    RAISE DEBUG '  ✓ Auto-links to existing user with same email (even unverified)';

    RAISE DEBUG '✓ Account linking tests passed';
END $$;
