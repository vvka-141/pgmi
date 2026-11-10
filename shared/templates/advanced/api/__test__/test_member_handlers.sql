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
    IF jsonb_array_length(v_body->'items') IS DISTINCT FROM 2
       OR (v_body->'items' @> jsonb_build_array(jsonb_build_object(
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
    v_body := pg_temp.expect_status('POST /invitations again', api.rest_invoke('POST', '/invitations', v_alice,
        '{"email": "carol@acme.com", "roleId": "org.viewer"}'::jsonb), 200);
    IF v_body->>'invitationId' IS DISTINCT FROM v_invitation::text THEN
        RAISE EXCEPTION 'TEST FAILED: a repeat invite answers the open invitation, got %', v_body;
    END IF;
    v_body := pg_temp.expect_status('POST /invitations with another role', api.rest_invoke('POST', '/invitations', v_alice,
        '{"email": "carol@acme.com", "roleId": "org.admin"}'::jsonb), 200);
    IF v_body->>'invitationId' IS DISTINCT FROM v_invitation::text OR v_body->>'roleId' IS DISTINCT FROM 'org.admin' THEN
        RAISE EXCEPTION 'TEST FAILED: a repeat invite with another role changes the open invitation''s role, got %', v_body;
    END IF;
    IF (api.openapi_document()->'paths'->'/invitations'->'post'->'responses' ?& ARRAY['200', '201']) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: OpenAPI lists 200 and 201 for POST /invitations';
    END IF;
    IF (SELECT array_agg(pg_temp.expect_status(t.label, api.rest_invoke('POST', '/invitations', v_alice, t.body), 422)->'invalid-params'
                         ORDER BY t.ord)
        FROM (VALUES (1, 'invite a member', '{"email": "bob@acme.com", "roleId": "org.viewer"}'::jsonb),
                     (2, 'invite as owner', '{"email": "dan@acme.com", "roleId": "org.owner"}'),
                     (3, 'invite a bad address', '{"email": "not an address", "roleId": "org.viewer"}')) t(ord, label, body))
       IS DISTINCT FROM ARRAY['[{"name": "email", "reason": "duplicate"}]', '[{"name": "roleId", "reason": "not_offered"}]',
                              '[{"name": "email", "reason": "invalid"}]']::jsonb[] THEN
        RAISE EXCEPTION 'TEST FAILED: each refused invitation names its field';
    END IF;

    PERFORM pg_temp.expect_status('DELETE /invitations/{id}',
        api.rest_invoke('DELETE', '/invitations/' || v_invitation, v_alice, NULL::bytea), 200);
    PERFORM pg_temp.test_as_kernel();
    IF NOT EXISTS (SELECT FROM membership.invitation WHERE object_id = v_invitation AND revoked_at IS NOT NULL) THEN
        RAISE EXCEPTION 'TEST FAILED: DELETE /invitations/{id} left the invitation pending';
    END IF;
    PERFORM pg_temp.test_as_owner();
    PERFORM pg_temp.expect_status('revoke a revoked invitation',
        api.rest_invoke('DELETE', '/invitations/' || v_invitation, v_alice, NULL::bytea), 404);

    PERFORM pg_temp.expect_status('PUT a new role',
        api.rest_invoke('PUT', '/members/' || v_bob_member || '/roles/org.admin', v_alice, NULL::bytea), 201);
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

-- ----------------------------------------------------------------------------
-- The organization's lists are paged: limit, offset, and the page envelope
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_acme  uuid := current_setting('test.acme_org_id')::uuid;
    v_alice extensions.hstore := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);
    v_page  jsonb;
BEGIN
    IF pg_temp.expect_status('GET /members?offset=2147483648', api.rest_invoke('GET', '/members?offset=2147483648', v_alice, NULL::bytea), 400)
           ->>'code' IS DISTINCT FROM 'pgmi.invalid_query' THEN
        RAISE EXCEPTION 'TEST FAILED: an offset past int4 must be 400 pgmi.invalid_query, never the first page';
    END IF;
    v_page := pg_temp.expect_status('GET /members?limit=1', api.rest_invoke('GET', '/members?limit=1', v_alice, NULL::bytea), 200);
    IF jsonb_array_length(v_page->'items') IS DISTINCT FROM 1 OR coalesce((v_page->>'total')::int, 0) < 1
       OR v_page->'hasMore' IS DISTINCT FROM to_jsonb(coalesce((v_page->>'total')::int, 0) > 1)
       OR (v_page->>'limit')::int IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'TEST FAILED: GET /members?limit=1 answers one member in a page envelope, got %', v_page;
    END IF;
    v_page := pg_temp.expect_status('GET /agents?limit=1', api.rest_invoke('GET', '/agents?limit=1', v_alice, NULL::bytea), 200);
    IF v_page->'agentsEnabled' IS DISTINCT FROM 'true'::jsonb OR (v_page ?& ARRAY['items', 'total', 'limit', 'offset', 'hasMore']) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: GET /agents answers a page envelope with agentsEnabled, got %', v_page;
    END IF;
    v_page := pg_temp.expect_status('GET /connections?offset=5', api.rest_invoke('GET', '/connections?offset=5', v_alice, NULL::bytea), 200);
    IF v_page->'items' IS DISTINCT FROM '[]'::jsonb OR (v_page->>'offset')::int IS DISTINCT FROM 5
       OR v_page->'total' IS DISTINCT FROM pg_temp.expect_status('GET /connections',
              api.rest_invoke('GET', '/connections', v_alice, NULL::bytea), 200)->'total' THEN
        RAISE EXCEPTION 'TEST FAILED: GET /connections pages by offset and keeps the total past the end, got %', v_page;
    END IF;
    v_page := pg_temp.expect_status('GET /members?offset=1000', api.rest_invoke('GET', '/members?offset=1000', v_alice, NULL::bytea), 200);
    IF v_page->'items' IS DISTINCT FROM '[]'::jsonb OR coalesce((v_page->>'total')::int, 0) < 1 THEN
        RAISE EXCEPTION 'TEST FAILED: GET /members past the end keeps the total, got %', v_page;
    END IF;
    PERFORM pg_temp.expect_status('GET /members?limit=x', api.rest_invoke('GET', '/members?limit=x', v_alice, NULL::bytea), 400);
END $$;
