-- ============================================================================
-- Test: invitations are accepted only by a verified matching email
-- ============================================================================

DO $$
DECLARE
    v_corp  uuid := current_setting('test.corp_id')::uuid;
    v_bob   uuid := current_setting('test.bob_id')::uuid;
    v_una   uuid := current_setting('test.una_id')::uuid;
    v_alice uuid := current_setting('test.alice_id')::uuid;
    v_inv   membership.invitation;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    RAISE DEBUG '-> Invitations: acceptance';

    v_inv := membership.fn_invite(v_corp, 'Una@Example.test', 'org.viewer');
    IF v_inv.object_id IS NULL OR v_inv.email IS DISTINCT FROM 'una@example.test' THEN
        RAISE EXCEPTION 'TEST FAILED: an invitation to a new address must be made, normalized';
    END IF;
    IF (membership.fn_accept_invitation(v_inv.object_id, v_una)).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an unverified email accepted an invitation';
    END IF;
    IF (membership.fn_accept_invitation(v_inv.object_id, v_bob)).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a person with another email accepted an invitation';
    END IF;

    v_inv := membership.fn_invite(v_corp, 'bob@example.test', 'org.viewer');
    IF (membership.fn_accept_invitation(v_inv.object_id, v_bob)).accepted_at IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the verified invitee must accept';
    END IF;
    IF NOT EXISTS (SELECT FROM membership.vw_member
                   WHERE organization_object_id = v_corp AND principal_object_id = v_bob AND role_ids = ARRAY['org.viewer']) THEN
        RAISE EXCEPTION 'TEST FAILED: acceptance must open a membership with the intended role';
    END IF;
    IF (membership.fn_accept_invitation(v_inv.object_id, v_bob)).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an invitation was accepted twice';
    END IF;

    IF membership.fn_invitation_problem(v_corp, 'alice@example.test', 'org.viewer') IS DISTINCT FROM 'membership.invitation.already_member'
       OR membership.fn_invitation_problem(v_corp, 'someone@example.test', 'org.owner') IS DISTINCT FROM 'membership.invitation.role_not_offered'
       OR membership.fn_invitation_problem(v_corp, 'not an email', 'org.viewer') IS DISTINCT FROM 'membership.invitation.email_invalid' THEN
        RAISE EXCEPTION 'TEST FAILED: invitation problems';
    END IF;

    v_inv := membership.fn_invite(v_corp, 'late@example.test', 'org.viewer');
    UPDATE membership.invitation SET created_at = now() - interval '30 days', expires_at = now() - interval '1 day'
    WHERE object_id = v_inv.object_id;
    IF (membership.fn_accept_invitation(v_inv.object_id,
            pg_temp.test_login('test', 'late-1', 'late@example.test', NULL, true))).object_id IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an expired invitation was accepted';
    END IF;

    RAISE DEBUG '  + only the verified invited address accepts, once, before expiry';
    PERFORM pg_temp.test_as_owner();
END $$;

DO $$
DECLARE
    v_org    membership.organization;
    v_person uuid;
    v_count  int;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    RAISE DEBUG '-> Invitations: an organization created for its owner';

    v_org := membership.fn_create_organization('Future Projects', 'future-projects', 'founder@example.test');
    IF v_org.status IS DISTINCT FROM 'pending'
       OR EXISTS (SELECT FROM membership.user_principal WHERE email = 'founder@example.test') THEN
        RAISE EXCEPTION 'TEST FAILED: the organization must be pending and its owner not created';
    END IF;

    v_person := pg_temp.test_login('test', 'founder-1', 'founder@example.test', NULL, true);
    SELECT count(*) INTO v_count FROM membership.fn_accept_pending_invitations(v_person);
    IF v_count IS DISTINCT FROM 1
       OR (SELECT status FROM membership.organization WHERE object_id = v_org.object_id) IS DISTINCT FROM 'active'
       OR (SELECT principal_object_id FROM membership.vw_organization_owner WHERE organization_object_id = v_org.object_id)
          IS DISTINCT FROM v_person THEN
        RAISE EXCEPTION 'TEST FAILED: accepting the owner invitation must activate the organization with its owner';
    END IF;

    RAISE NOTICE '  + invitations: only the verified invited address accepts; the owner''s acceptance activates';
    PERFORM pg_temp.test_as_owner();
END $$;
