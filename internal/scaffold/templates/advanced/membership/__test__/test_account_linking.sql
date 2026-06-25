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

    v_linked_id := membership.upsert_user('azure-ad', 'alice-azure-001', 'alice@example.com', 'Alice Azure', false);
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

    -- View-layer: vw_user_identities shows both providers
    IF (SELECT count(*) FROM membership.vw_user_identities
        WHERE user_object_id = v_alice_id) != 2 THEN
        RAISE EXCEPTION 'TEST FAILED: vw_user_identities should show 2 entries for alice';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM membership.vw_user_identities
        WHERE user_object_id = v_alice_id AND idp_provider = 'azure-ad'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: vw_user_identities missing azure-ad identity';
    END IF;
    RAISE DEBUG '  ✓ vw_user_identities shows both google and azure-ad';

    -- View-layer: vw_user_claims aggregates identities
    DECLARE
        v_claims record;
    BEGIN
        SELECT identities, member_org_ids, roles INTO v_claims
        FROM membership.vw_user_claims WHERE user_id = v_alice_id;

        IF jsonb_array_length(v_claims.identities) != 2 THEN
            RAISE EXCEPTION 'TEST FAILED: vw_user_claims should have 2 identities, got %', jsonb_array_length(v_claims.identities);
        END IF;
        IF array_length(v_claims.member_org_ids, 1) < 1 THEN
            RAISE EXCEPTION 'TEST FAILED: vw_user_claims should show at least 1 org membership';
        END IF;
        RAISE DEBUG '  ✓ vw_user_claims aggregates identities (%) and org memberships (%)',
            jsonb_array_length(v_claims.identities), array_length(v_claims.member_org_ids, 1);
    END;

    v_unverified_id := membership.upsert_user('local', 'unverified-001', 'unverified@example.com', 'Unverified', false);
    v_unverified_user_id := membership.upsert_user('google', 'unverified-google', 'unverified@example.com', 'Unverified Google', false);
    IF v_unverified_user_id != v_unverified_id THEN
        RAISE EXCEPTION 'TEST FAILED: same email should link to existing user';
    END IF;
    RAISE DEBUG '  ✓ Auto-links to existing user with same email (even unverified)';

    RAISE DEBUG '✓ Account linking tests passed';
END $$;
