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

    -- View-layer: bob appears in vw_pending_invitations, not vw_active_memberships
    IF NOT EXISTS (
        SELECT 1 FROM membership.vw_pending_invitations
        WHERE user_id = v_bob_id AND organization_id = v_org_id
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: pending bob not in vw_pending_invitations';
    END IF;
    IF EXISTS (
        SELECT 1 FROM membership.vw_active_memberships
        WHERE user_id = v_bob_id AND organization_id = v_org_id
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: pending bob should NOT be in vw_active_memberships';
    END IF;
    RAISE DEBUG '  ✓ vw_pending_invitations shows bob; vw_active_memberships does not';

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

    -- View-layer: after accept, bob moves to vw_active_memberships
    IF NOT EXISTS (
        SELECT 1 FROM membership.vw_active_memberships
        WHERE user_id = v_bob_id AND organization_id = v_org_id AND role = 'reader'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: accepted bob not in vw_active_memberships';
    END IF;
    IF EXISTS (
        SELECT 1 FROM membership.vw_pending_invitations
        WHERE user_id = v_bob_id AND organization_id = v_org_id
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: accepted bob should NOT remain in vw_pending_invitations';
    END IF;
    RAISE DEBUG '  ✓ After accept: bob in vw_active_memberships, gone from vw_pending_invitations';

    RAISE DEBUG '✓ Invitation flow tests passed';
END $$;
