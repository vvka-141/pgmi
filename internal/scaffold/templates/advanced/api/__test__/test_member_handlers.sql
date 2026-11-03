-- ============================================================================
-- Test: members, roles and invitations over REST
-- ============================================================================
-- Alice owns Acme Corp, Bob is an org.member (fixture). Who may call which
-- handler is test_access_matrix.sql; this file checks what each one does.
-- ============================================================================

CREATE FUNCTION pg_temp.expect_status(p_label text, p_response api.http_response, p_status int)
RETURNS jsonb LANGUAGE plpgsql AS $$
BEGIN
    IF (p_response).status_code IS DISTINCT FROM p_status THEN
        RAISE EXCEPTION 'TEST FAILED: % expected %, got %: %', p_label, p_status, (p_response).status_code,
            api.content_json((p_response).content);
    END IF;
    RETURN api.content_json((p_response).content);
END $$;

DO $$
DECLARE
    v_acme       uuid := current_setting('test.acme_org_id')::uuid;
    v_far_owner  uuid;
    v_far        uuid;
    v_far_member uuid;
    v_bob_member uuid;
    v_own_member uuid;
    v_alice      extensions.hstore;
    v_invitation uuid;
    v_body       jsonb;
BEGIN
    RAISE DEBUG '-> Member, role and invitation handlers';

    v_far_owner := pg_temp.test_login('google', 'far-owner', 'far-owner@example.test', NULL, true);
    v_far := (membership.fn_create_organization_with_owner('Far Away', 'far-away', v_far_owner)).object_id;
    PERFORM pg_temp.test_as_kernel();
    SELECT membership_object_id INTO v_far_member FROM membership.vw_member WHERE organization_object_id = v_far;
    SELECT membership_object_id INTO v_bob_member FROM membership.vw_member
    WHERE organization_object_id = v_acme AND email = 'bob@acme.com';
    SELECT membership_object_id INTO v_own_member FROM membership.vw_member
    WHERE organization_object_id = v_acme AND email = 'alice@acme.com';
    PERFORM pg_temp.test_as_owner();
    IF v_far_member IS NULL OR v_bob_member IS NULL OR v_own_member IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: fixture memberships missing';
    END IF;

    v_body := pg_temp.expect_status('member GET /members', api.rest_invoke('GET', '/members',
        pg_temp.test_session_headers(current_setting('test.bob_subject'), v_acme), NULL::bytea), 200);
    IF jsonb_array_length(v_body->'members') IS DISTINCT FROM 2
       OR (v_body->'members' @> jsonb_build_array(jsonb_build_object(
              'memberId', v_bob_member, 'roleIds', jsonb_build_array('org.member')))) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: GET /members lists Acme''s two members with their roles: %', v_body;
    END IF;

    v_alice := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);

    v_body := pg_temp.expect_status('POST /invitations', api.rest_invoke('POST', '/invitations', v_alice,
        '{"email": "Carol@Acme.com", "roleId": "org.viewer"}'::jsonb), 201);
    v_invitation := (v_body->>'invitationId')::uuid;
    PERFORM pg_temp.test_as_kernel();
    IF NOT EXISTS (SELECT FROM membership.invitation
                   WHERE object_id = v_invitation AND organization_object_id = v_acme
                     AND email = 'carol@acme.com' AND role_id = 'org.viewer' AND revoked_at IS NULL) THEN
        RAISE EXCEPTION 'TEST FAILED: POST /invitations wrote no pending invitation to Acme: %', v_body;
    END IF;
    PERFORM pg_temp.test_as_owner();
    PERFORM pg_temp.expect_status('invite a member', api.rest_invoke('POST', '/invitations', v_alice,
        '{"email": "bob@acme.com", "roleId": "org.viewer"}'::jsonb), 409);
    PERFORM pg_temp.expect_status('invite as owner', api.rest_invoke('POST', '/invitations', v_alice,
        '{"email": "dan@acme.com", "roleId": "org.owner"}'::jsonb), 422);
    PERFORM pg_temp.expect_status('invite a bad address', api.rest_invoke('POST', '/invitations', v_alice,
        '{"email": "not an address", "roleId": "org.viewer"}'::jsonb), 422);

    PERFORM pg_temp.expect_status('DELETE /invitations/{id}',
        api.rest_invoke('DELETE', '/invitations/' || v_invitation, v_alice, NULL::bytea), 200);
    PERFORM pg_temp.test_as_kernel();
    IF NOT EXISTS (SELECT FROM membership.invitation WHERE object_id = v_invitation AND revoked_at IS NOT NULL) THEN
        RAISE EXCEPTION 'TEST FAILED: DELETE /invitations/{id} left the invitation pending';
    END IF;
    PERFORM pg_temp.test_as_owner();
    PERFORM pg_temp.expect_status('revoke a revoked invitation',
        api.rest_invoke('DELETE', '/invitations/' || v_invitation, v_alice, NULL::bytea), 404);

    PERFORM pg_temp.expect_status('PUT role',
        api.rest_invoke('PUT', '/members/' || v_bob_member || '/roles/org.admin', v_alice, NULL::bytea), 200);
    PERFORM pg_temp.test_as_kernel();
    IF NOT EXISTS (SELECT FROM membership.vw_member
                   WHERE membership_object_id = v_bob_member AND 'org.admin' = ANY (role_ids)) THEN
        RAISE EXCEPTION 'TEST FAILED: PUT /members/{id}/roles/org.admin granted nothing';
    END IF;
    PERFORM pg_temp.test_as_owner();
    PERFORM pg_temp.expect_status('PUT the same role again',
        api.rest_invoke('PUT', '/members/' || v_bob_member || '/roles/org.admin', v_alice, NULL::bytea), 200);
    PERFORM pg_temp.expect_status('PUT org.owner',
        api.rest_invoke('PUT', '/members/' || v_bob_member || '/roles/org.owner', v_alice, NULL::bytea), 409);
    PERFORM pg_temp.expect_status('PUT an unknown role',
        api.rest_invoke('PUT', '/members/' || v_bob_member || '/roles/org.nope', v_alice, NULL::bytea), 404);
    PERFORM pg_temp.expect_status('PUT a role in another organization',
        api.rest_invoke('PUT', '/members/' || v_far_member || '/roles/org.admin', v_alice, NULL::bytea), 404);

    PERFORM pg_temp.expect_status('DELETE role',
        api.rest_invoke('DELETE', '/members/' || v_bob_member || '/roles/org.admin', v_alice, NULL::bytea), 200);
    PERFORM pg_temp.test_as_kernel();
    IF EXISTS (SELECT FROM membership.vw_member
               WHERE membership_object_id = v_bob_member AND 'org.admin' = ANY (role_ids)) THEN
        RAISE EXCEPTION 'TEST FAILED: DELETE /members/{id}/roles/org.admin left the role';
    END IF;
    PERFORM pg_temp.test_as_owner();
    PERFORM pg_temp.expect_status('DELETE a role not held',
        api.rest_invoke('DELETE', '/members/' || v_bob_member || '/roles/org.admin', v_alice, NULL::bytea), 404);

    PERFORM pg_temp.expect_status('remove the owner',
        api.rest_invoke('DELETE', '/members/' || v_own_member, v_alice, NULL::bytea), 409);
    PERFORM pg_temp.expect_status('remove a member of another organization',
        api.rest_invoke('DELETE', '/members/' || v_far_member, v_alice, NULL::bytea), 404);
    PERFORM pg_temp.test_as_kernel();
    IF NOT EXISTS (SELECT FROM membership.vw_member WHERE membership_object_id = v_far_member) THEN
        RAISE EXCEPTION 'TEST FAILED: a request in Acme ended a membership in another organization';
    END IF;
    PERFORM pg_temp.test_as_owner();
    PERFORM pg_temp.expect_status('remove a member',
        api.rest_invoke('DELETE', '/members/' || v_bob_member, v_alice, NULL::bytea), 200);
    PERFORM pg_temp.test_as_kernel();
    IF EXISTS (SELECT FROM membership.vw_member WHERE membership_object_id = v_bob_member) THEN
        RAISE EXCEPTION 'TEST FAILED: DELETE /members/{id} left the membership current';
    END IF;
    PERFORM pg_temp.test_as_owner();
    PERFORM pg_temp.expect_status('remove a bad id',
        api.rest_invoke('DELETE', '/members/not-a-uuid', v_alice, NULL::bytea), 400);

    RAISE NOTICE '  + members, roles and invitations over REST stay inside the request''s organization';
END $$;
