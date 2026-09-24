-- ============================================================================
-- Test: Account linking only via an email verified on both sides
-- ============================================================================

DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
    v_linked_id UUID;
    v_identity_count BIGINT;
    v_unverified_id UUID;
    v_unverified_user_id UUID;
    v_case record;
BEGIN
    RAISE DEBUG '→ Testing account linking';

    v_linked_id := membership.upsert_user('azure-ad', 'alice-azure-001', 'alice@example.com', 'Alice Azure', true);
    IF v_linked_id IS DISTINCT FROM v_alice_id THEN
        RAISE EXCEPTION 'TEST FAILED: verified email on both sides should link to existing user %, got %', v_alice_id, v_linked_id;
    END IF;
    RAISE DEBUG '  ✓ Linked azure-ad identity: email verified by both sides';

    SELECT count(*) INTO v_identity_count
    FROM membership.user_identity WHERE user_object_id = v_alice_id;
    IF v_identity_count IS DISTINCT FROM 2 THEN
        RAISE EXCEPTION 'TEST FAILED: expected 2 identities, got %', v_identity_count;
    END IF;
    RAISE DEBUG '  ✓ User has 2 linked identities';

    -- View-layer: vw_user_identities shows both providers
    IF (SELECT count(*) FROM membership.vw_user_identities
        WHERE user_object_id = v_alice_id) IS DISTINCT FROM 2 THEN
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

        IF jsonb_array_length(v_claims.identities) IS DISTINCT FROM 2 THEN
            RAISE EXCEPTION 'TEST FAILED: vw_user_claims should have 2 identities, got %', jsonb_array_length(v_claims.identities);
        END IF;
        IF coalesce(array_length(v_claims.member_org_ids, 1), 0) < 1 THEN
            RAISE EXCEPTION 'TEST FAILED: vw_user_claims should show at least 1 org membership';
        END IF;
        RAISE DEBUG '  ✓ vw_user_claims aggregates identities (%) and org memberships (%)',
            jsonb_array_length(v_claims.identities), array_length(v_claims.member_org_ids, 1);
    END;

    -- Linking on an email that either side has not verified is an account
    -- takeover: whoever can present the address signs in as its owner.
    v_unverified_id := membership.upsert_user('local', 'unverified-001', 'unverified@example.com', 'Unverified', false);
    FOR v_case IN
        SELECT * FROM (VALUES
            ('github',  'attacker-1', 'alice@example.com',      false, 'unverified identity onto a verified account'),
            ('github',  'attacker-2', 'unverified@example.com', true,  'verified identity onto an unverified account'),
            ('google',  'attacker-3', 'unverified@example.com', false, 'unverified identity onto an unverified account')
        ) AS t(provider, subject, email, verified, label)
    LOOP
        BEGIN
            v_unverified_user_id := membership.upsert_user(v_case.provider, v_case.subject, v_case.email, NULL, v_case.verified);
            RAISE EXCEPTION 'TEST FAILED: linked % (got user %)', v_case.label, v_unverified_user_id;
        EXCEPTION WHEN SQLSTATE 'P0409' THEN
            NULL;
        END;
        IF EXISTS (SELECT 1 FROM membership.user_identity
                   WHERE idp_provider = v_case.provider AND idp_subject_id = v_case.subject) THEN
            RAISE EXCEPTION 'TEST FAILED: refused link still stored identity %|%', v_case.provider, v_case.subject;
        END IF;
    END LOOP;
    RAISE DEBUG '  ✓ Refuses to link unless the email is verified on both sides';

    RAISE DEBUG '✓ Account linking tests passed';
END $$;
