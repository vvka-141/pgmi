-- ============================================================================
-- Test: Single default (personal) organization constraint
-- ============================================================================

DO $$
DECLARE
    v_alice_id UUID := current_setting('test.alice_id')::UUID;
    v_personal_count BIGINT;
    v_default_org membership.organization;
BEGIN
    RAISE DEBUG '→ Testing default organization';

    SELECT count(*) INTO v_personal_count
    FROM membership.organization
    WHERE owner_user_id = v_alice_id AND is_personal = true;

    IF v_personal_count != 1 THEN
        RAISE EXCEPTION 'TEST FAILED: expected exactly 1 personal org, got %', v_personal_count;
    END IF;
    RAISE DEBUG '  ✓ Exactly one personal organization';

    v_default_org := membership.get_user_default_organization(v_alice_id);
    IF v_default_org.object_id IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: get_user_default_organization returned null';
    END IF;
    IF NOT v_default_org.is_personal THEN
        RAISE EXCEPTION 'TEST FAILED: default org should be personal';
    END IF;
    RAISE DEBUG '  ✓ get_user_default_organization returns personal org';

    IF NOT membership.can_create_organization(v_alice_id) THEN
        RAISE EXCEPTION 'TEST FAILED: alice should be able to create orgs (has <5)';
    END IF;
    RAISE DEBUG '  ✓ can_create_organization returns true (under limit)';

    -- View-layer: personal org visible in vw_active_organizations
    IF NOT EXISTS (
        SELECT 1 FROM membership.vw_active_organizations
        WHERE owner_user_id = v_alice_id AND is_personal = true
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: personal org not visible in vw_active_organizations';
    END IF;
    RAISE DEBUG '  ✓ vw_active_organizations shows personal org';

    -- View-layer: alice has active membership visible via vw_active_memberships
    IF NOT EXISTS (
        SELECT 1 FROM membership.vw_active_memberships
        WHERE user_id = v_alice_id AND is_personal = true AND role = 'admin'
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: alice not visible as admin in vw_active_memberships';
    END IF;
    RAISE DEBUG '  ✓ vw_active_memberships shows alice as admin of personal org';

    RAISE DEBUG '✓ Default organization tests passed';
END $$;
