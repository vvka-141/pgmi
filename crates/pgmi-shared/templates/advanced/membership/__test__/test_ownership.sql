-- ============================================================================
-- Test: exactly one owner
-- ============================================================================
-- org.owner is held by one member at a time and moves only by a transfer, so
-- an organization never has two owners, and never none.

DO $$
DECLARE
    v_corp   uuid := current_setting('test.corp_id')::uuid;
    v_owner  uuid := current_setting('test.owner_id')::uuid;
    v_alice  uuid := current_setting('test.alice_id')::uuid;
    v_member uuid := current_setting('test.alice_member_id')::uuid;
    v_owner_member uuid;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    RAISE DEBUG '-> Ownership: one owner';

    SELECT membership_object_id INTO v_owner_member FROM membership.vw_organization_owner WHERE organization_object_id = v_corp;

    IF membership.fn_role_problem(v_member, 'org.owner') IS DISTINCT FROM 'membership.role.owner_moves_by_transfer'
       OR ((membership.fn_grant_role(v_member, 'org.owner')).assignment).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: org.owner must not be granted beside the current owner';
    END IF;
    IF (membership.fn_revoke_role(v_owner_member, 'org.owner')).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the owner''s role must not be revoked, leaving no owner';
    END IF;
    IF (membership.fn_end_membership(v_owner_member, 'left')).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the owner''s membership must not end';
    END IF;

    BEGIN
        INSERT INTO membership.role_assignment (organization_object_id, membership_object_id, role_id, valid_during)
        VALUES (v_corp, v_member, 'org.owner', tstzrange(clock_timestamp(), NULL, '[)'));
        RAISE EXCEPTION 'TEST FAILED: the table accepted a second current owner';
    EXCEPTION WHEN exclusion_violation THEN
        NULL;
    END;

    PERFORM pg_temp.test_act_as('test|alice-001', v_corp);
    IF (membership.fn_transfer_ownership(v_corp, v_alice, true)).object_id IS NOT NULL
       OR membership.fn_transfer_ownership_problem(v_corp, v_alice) IS DISTINCT FROM 'membership.ownership.not_owner' THEN
        RAISE EXCEPTION 'TEST FAILED: the kernel must refuse a transfer by a member who is not the owner: %',
            membership.fn_transfer_ownership_problem(v_corp, v_alice);
    END IF;

    PERFORM pg_temp.test_act_as('test|owner-001', v_corp);
    IF (membership.fn_transfer_ownership(v_corp, v_alice, true)).object_id IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: ownership must move to a current member: %',
            membership.fn_transfer_ownership_problem(v_corp, v_alice);
    END IF;
    PERFORM pg_temp.test_act_as(NULL);
    IF (SELECT array_agg(principal_object_id) FROM membership.vw_organization_owner WHERE organization_object_id = v_corp)
       IS DISTINCT FROM ARRAY[v_alice] THEN
        RAISE EXCEPTION 'TEST FAILED: after the transfer alice must be the one owner';
    END IF;
    IF NOT EXISTS (SELECT FROM membership.vw_member
                   WHERE organization_object_id = v_corp AND principal_object_id = v_owner AND 'org.admin' = ANY (role_ids)) THEN
        RAISE EXCEPTION 'TEST FAILED: the previous owner should keep org.admin';
    END IF;

    RAISE DEBUG '  + one owner at a time; ownership only moves';
    PERFORM pg_temp.test_as_owner();
END $$;

DO $$
DECLARE
    v_bob  uuid := current_setting('test.bob_id')::uuid;
    v_corp uuid := current_setting('test.corp_id')::uuid;
    v_org  membership.organization;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    RAISE DEBUG '-> Ownership: organizations';

    v_org := membership.fn_create_organization_with_owner('Bob Projects', 'bob-projects', v_bob);
    IF v_org.status IS DISTINCT FROM 'active'
       OR (SELECT principal_object_id FROM membership.vw_organization_owner WHERE organization_object_id = v_org.object_id)
          IS DISTINCT FROM v_bob THEN
        RAISE EXCEPTION 'TEST FAILED: the self-serve path must create an active organization owned by the caller';
    END IF;
    IF (membership.fn_create_organization_with_owner('Again', 'bob-projects', v_bob)).object_id IS NOT NULL
       OR membership.fn_organization_problem('Again', 'bob-projects') IS DISTINCT FROM 'membership.organization.slug_taken'
       OR membership.fn_organization_problem('Admin', 'admin') IS DISTINCT FROM 'membership.organization.slug_reserved' THEN
        RAISE EXCEPTION 'TEST FAILED: a taken or reserved slug must be refused';
    END IF;

    IF (membership.fn_update_organization(v_corp, 'Test Corp', 'pending')).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an organization must not go back to pending';
    END IF;
    v_org := membership.fn_update_organization(v_corp, 'Test Corp', 'archived');
    IF v_org.status IS DISTINCT FROM 'archived'
       OR EXISTS (SELECT FROM membership.vw_member WHERE organization_object_id = v_corp) THEN
        RAISE EXCEPTION 'TEST FAILED: archiving must end every membership: %', v_org;
    END IF;
    IF (membership.fn_update_organization(v_corp, 'Test Corp', 'active')).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: archived must be final';
    END IF;

    RAISE NOTICE '  + ownership: one owner, moved only by transfer; organizations only move forward';
    PERFORM pg_temp.test_as_owner();
END $$;
