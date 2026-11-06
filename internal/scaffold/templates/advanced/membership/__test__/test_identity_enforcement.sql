-- ============================================================================
-- Test: identity enforcement
-- ============================================================================
-- api.current_principal_id() and api.current_organization_id() are what
-- core.apply_org_rls hangs off, so a gap in either is a gap in every
-- organization-scoped table at once.

DO $$
DECLARE
    v_alice uuid := current_setting('test.alice_id')::uuid;
    v_corp  uuid := current_setting('test.corp_id')::uuid;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    RAISE DEBUG '-> Identity: disabling a person ends their access';

    PERFORM pg_temp.test_act_as('test|alice-001', v_corp);
    IF api.current_principal_id() IS DISTINCT FROM v_alice OR api.current_organization_id() IS DISTINCT FROM v_corp THEN
        RAISE EXCEPTION 'TEST FAILED: an enabled member must resolve with her organization';
    END IF;

    PERFORM membership.fn_disable_principal(v_alice, 'compromised account');
    IF api.current_principal_id() IS NOT NULL OR api.current_organization_id() IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a disabled person still resolves (%)', api.current_principal_id();
    END IF;

    PERFORM membership.fn_enable_principal(v_alice);
    IF api.current_principal_id() IS DISTINCT FROM v_alice THEN
        RAISE EXCEPTION 'TEST FAILED: enabling must restore the person';
    END IF;

    PERFORM pg_temp.test_act_as(NULL);

    RAISE DEBUG '  + disabled people resolve to no one';
    PERFORM pg_temp.test_as_owner();
END $$;

DO $$
DECLARE
    v_corp   uuid := current_setting('test.corp_id')::uuid;
    v_member uuid := current_setting('test.alice_member_id')::uuid;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    RAISE DEBUG '-> Identity: suspended organizations and ended memberships leave the anchor';

    PERFORM membership.fn_update_organization(v_corp, 'Test Corp', 'suspended');
    PERFORM pg_temp.test_act_as('test|alice-001', v_corp);
    IF api.current_organization_id() IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a suspended organization is still in the anchor';
    END IF;
    PERFORM pg_temp.test_act_as(NULL);
    PERFORM membership.fn_update_organization(v_corp, 'Test Corp', 'active');

    IF (membership.fn_end_membership(v_member, 'removed')).end_reason IS DISTINCT FROM 'removed' THEN
        RAISE EXCEPTION 'TEST FAILED: the membership must end';
    END IF;
    PERFORM pg_temp.test_act_as('test|alice-001', v_corp);
    IF api.current_organization_id() IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an ended membership is still in the anchor';
    END IF;
    PERFORM pg_temp.test_act_as(NULL);

    RAISE DEBUG '  + only current memberships of active organizations open rows';
    PERFORM pg_temp.test_as_owner();
END $$;

DO $$
DECLARE
    v_bob     uuid := current_setting('test.bob_id')::uuid;
    v_mallory uuid;
BEGIN
    RAISE DEBUG '-> Identity: platform operators follow the parameter';

    PERFORM pg_temp.sync_platform_operators('[{"email": "bob@example.test"}, {"email": "ops@example.test", "displayName": "Ops"}]');
    PERFORM pg_temp.test_as_kernel();
    IF (membership.fn_is_platform_operator(v_bob)) IS NOT TRUE
       OR NOT EXISTS (SELECT FROM membership.vw_user_principal WHERE email = 'ops@example.test' AND is_platform_operator AND is_email_verified) THEN
        RAISE EXCEPTION 'TEST FAILED: listed people must become platform operators';
    END IF;

    PERFORM pg_temp.test_as_owner();
    PERFORM pg_temp.sync_platform_operators('[{"email": "ops@example.test"}]');
    PERFORM pg_temp.test_as_kernel();
    IF membership.fn_is_platform_operator(v_bob) THEN
        RAISE EXCEPTION 'TEST FAILED: a person dropped from the list must lose the status';
    END IF;

    PERFORM pg_temp.test_as_owner();
    v_mallory := pg_temp.test_login('test', 'mallory-001', 'boss@example.test', 'Mallory', false);
    PERFORM pg_temp.sync_platform_operators('[{"email": "boss@example.test"}]');
    PERFORM pg_temp.test_as_kernel();
    IF membership.fn_is_platform_operator(v_mallory) THEN
        RAISE EXCEPTION 'TEST FAILED: an unverified principal became platform operator';
    END IF;
    IF NOT EXISTS (SELECT FROM membership.vw_user_principal
                   WHERE email = 'boss@example.test' AND is_platform_operator AND is_email_verified
                     AND principal_object_id IS DISTINCT FROM v_mallory) THEN
        RAISE EXCEPTION 'TEST FAILED: an operator listed by an address held only unverified must be a new verified person';
    END IF;

    RAISE NOTICE '  + identity: disabled people, ended memberships and dropped operators lose access';
    PERFORM pg_temp.test_as_owner();
END $$;

DO $$
DECLARE
    v_corp  uuid := current_setting('test.corp_id')::uuid;
    v_owner uuid := current_setting('test.owner_id')::uuid;
BEGIN
    RAISE DEBUG '-> Identity: an unverified address on a member is not that address''s person';

    PERFORM pg_temp.test_login('test', 'owner-001', 'bob@example.test', 'Owner', false);
    PERFORM pg_temp.test_as_kernel();
    IF (SELECT is_email_verified FROM membership.vw_member
        WHERE organization_object_id = v_corp AND principal_object_id = v_owner) IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'TEST FAILED: the member list must flag an unverified address';
    END IF;
    IF membership.fn_invitation_problem(v_corp, 'bob@example.test', 'org.member') IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a member holding bob''s address unverified blocks bob''s invitation (%)',
            membership.fn_invitation_problem(v_corp, 'bob@example.test', 'org.member');
    END IF;

    RAISE DEBUG '  + an unverified address neither names a member nor blocks an invitation';
    PERFORM pg_temp.test_as_owner();
END $$;
