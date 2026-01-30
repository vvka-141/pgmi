-- ============================================================================
-- Test: Organization invitation flow (pending → active)
-- ============================================================================

DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
    v_bob_id UUID := current_setting('test.bob_id')::UUID;
    v_org_id UUID;
    v_member membership.organization_member;
BEGIN
    RAISE DEBUG '→ Testing invitation flow';

    SELECT object_id INTO STRICT v_org_id
    FROM membership.organization
    WHERE owner_user_id = v_alice_id AND is_personal = true;

    INSERT INTO membership.organization_member (organization_id, user_id, role, status)
    VALUES (v_org_id, v_bob_id, 'reader', 'pending');

    SELECT * INTO STRICT v_member
    FROM membership.organization_member
    WHERE organization_id = v_org_id AND user_id = v_bob_id;

    IF v_member.status != 'pending' THEN
        RAISE EXCEPTION 'TEST FAILED: expected pending, got %', v_member.status;
    END IF;
    RAISE DEBUG '  ✓ Invitation created as pending';

    UPDATE membership.organization_member
    SET status = 'active', joined_at = now()
    WHERE organization_id = v_org_id AND user_id = v_bob_id;

    SELECT * INTO STRICT v_member
    FROM membership.organization_member
    WHERE organization_id = v_org_id AND user_id = v_bob_id;

    IF v_member.status != 'active' THEN
        RAISE EXCEPTION 'TEST FAILED: expected active, got %', v_member.status;
    END IF;
    IF v_member.joined_at IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: joined_at should be set';
    END IF;
    RAISE DEBUG '  ✓ Invitation accepted (pending → active)';

    RAISE DEBUG '✓ Invitation flow tests passed';
END $$;
