-- ============================================================================
-- Test: User provisioning and default organization creation
-- ============================================================================

DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
    v_user membership."user";
    v_org membership.organization;
    v_member_count BIGINT;
BEGIN
    RAISE DEBUG '→ Testing user upsert';

    SELECT * INTO STRICT v_user FROM membership."user" WHERE object_id = v_alice_id;

    IF v_user.email != 'alice@example.com' THEN
        RAISE EXCEPTION 'TEST FAILED: expected alice email, got %', v_user.email;
    END IF;
    IF NOT v_user.email_verified THEN
        RAISE EXCEPTION 'TEST FAILED: alice should be email_verified';
    END IF;
    RAISE DEBUG '  ✓ User created with correct attributes';

    SELECT * INTO STRICT v_org
    FROM membership.organization
    WHERE owner_user_id = v_alice_id AND is_personal = true;

    IF v_org.name != 'Personal' THEN
        RAISE EXCEPTION 'TEST FAILED: expected Personal org, got %', v_org.name;
    END IF;
    RAISE DEBUG '  ✓ Personal organization created';

    SELECT count(*) INTO v_member_count
    FROM membership.organization_member
    WHERE organization_id = v_org.object_id AND user_id = v_alice_id AND status = 'active';

    IF v_member_count != 1 THEN
        RAISE EXCEPTION 'TEST FAILED: expected 1 active membership, got %', v_member_count;
    END IF;
    RAISE DEBUG '  ✓ User is active admin member of personal org';

    PERFORM api.upsert_user('google', 'alice-001', 'alice@example.com', 'Alice Updated', true);
    SELECT * INTO STRICT v_user FROM membership."user" WHERE object_id = v_alice_id;
    IF v_user.display_name != 'Alice Updated' THEN
        RAISE EXCEPTION 'TEST FAILED: display_name not updated on re-upsert';
    END IF;
    RAISE DEBUG '  ✓ Re-upsert updates display_name';

    RAISE DEBUG '✓ User upsert tests passed';
END $$;
