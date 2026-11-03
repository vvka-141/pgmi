-- ============================================================================
-- Test: the access explorer over REST
-- ============================================================================
-- Alice owns Acme Corp, Bob is an org.member (fixture). Alice also belongs to
-- a second organization, so a leak of its rows into Acme's answers shows.
-- Who may call the explorer is test_access_matrix.sql; this file checks what
-- it answers.
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
    v_nil      text := '00000000-0000-4000-8000-000000000000';
    v_acme     uuid := current_setting('test.acme_org_id')::uuid;
    v_alice    uuid := pg_temp.test_person(current_setting('test.alice_subject'));
    v_bob      uuid := pg_temp.test_person(current_setting('test.bob_subject'));
    v_far      uuid;
    v_stranger uuid;
    v_headers  extensions.hstore;
    v_body     jsonb;
BEGIN
    RAISE DEBUG '-> Access explorer';
    PERFORM pg_temp.test_act_as(NULL);

    v_stranger := pg_temp.test_login('google', 'explorer-far-owner', 'explorer-far@example.test', NULL, true);
    v_far := (membership.fn_create_organization_with_owner('Explorer Far', 'explorer-far', v_stranger)).object_id;
    PERFORM membership.fn_add_member(v_far, v_alice, 'org.admin');
    v_headers := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme);

    v_body := pg_temp.expect_status('GET /access/handlers',
        api.rest_invoke('GET', '/access/handlers', v_headers, NULL::bytea), 200);
    IF (v_body->'handlers' @> jsonb_build_array(jsonb_build_object(
               'handler', 'list_members', 'kind', 'rest', 'method', 'GET', 'path', '/members', 'access', 'permission',
               'permissions', jsonb_build_array('members.read'),
               'roleIds', jsonb_build_array('agent.contributor', 'agent.delegate', 'agent.reader',
                                         'org.admin', 'org.member', 'org.owner', 'org.viewer'),
               'platformOperation', false))) IS NOT TRUE
       OR (v_body->'handlers' @> jsonb_build_array(jsonb_build_object(
               'handler', 'admin_dashboard', 'permissions', jsonb_build_array('platform.operate'),
               'roleIds', '[]'::jsonb, 'platformOperation', true))) IS NOT TRUE
       OR (v_body->'handlers' @> jsonb_build_array(jsonb_build_object('handler', 'get_current_user', 'access', 'authenticated'))) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: GET /access/handlers lists each operation with its permissions and roles: %', v_body;
    END IF;
    v_body := pg_temp.expect_status('GET /access/handlers?role=org.viewer',
        api.rest_invoke('GET', '/access/handlers?role=org.viewer', v_headers, NULL::bytea), 200);
    IF EXISTS (SELECT FROM jsonb_array_elements(v_body->'handlers') h WHERE NOT h->'roleIds' ? 'org.viewer')
       OR (v_body->'handlers' @> jsonb_build_array(jsonb_build_object('handler', 'list_members'))) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: ?role= keeps only the operations that role opens: %', v_body;
    END IF;

    v_body := pg_temp.expect_status('GET /access/paths',
        api.rest_invoke('GET', '/access/paths', v_headers, NULL::bytea), 200);
    IF EXISTS (SELECT FROM jsonb_array_elements(v_body->'paths') p
               WHERE (p->>'principalId')::uuid NOT IN (v_alice, v_bob))
       OR (v_body->'paths' @> jsonb_build_array(jsonb_build_object(
               'principalId', v_bob, 'roleId', 'org.member', 'permission', 'members.read', 'handler', 'list_members'))) IS NOT TRUE
       OR v_body->'paths' @> jsonb_build_array(jsonb_build_object('principalId', v_bob, 'handler', 'remove_member'))
       OR v_body->'paths' @> jsonb_build_array(jsonb_build_object('principalId', v_alice, 'roleId', 'org.admin'))
       OR (v_body->'paths' @> jsonb_build_array(jsonb_build_object(
               'principalId', v_alice, 'roleId', 'org.owner', 'handler', 'remove_member'))) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: GET /access/paths lists the paths of Acme''s members only: %', v_body;
    END IF;
    v_body := pg_temp.expect_status('GET /access/paths?principalId=bob&limit=1',
        api.rest_invoke('GET', format('/access/paths?principalId=%s&limit=1', v_bob), v_headers, NULL::bytea), 200);
    IF jsonb_array_length(v_body->'paths') IS DISTINCT FROM 1 OR (v_body#>>'{paths,0,principalId}')::uuid IS DISTINCT FROM v_bob THEN
        RAISE EXCEPTION 'TEST FAILED: ?principalId= and ?limit= narrow the paths: %', v_body;
    END IF;
    PERFORM pg_temp.expect_status('GET /access/paths?limit=0',
        api.rest_invoke('GET', '/access/paths?limit=0', v_headers, NULL::bytea), 400);
    PERFORM pg_temp.expect_status('GET /access/paths?principalId=x',
        api.rest_invoke('GET', '/access/paths?principalId=x', v_headers, NULL::bytea), 400);

    v_body := pg_temp.expect_status('simulate bob GET /members',
        api.rest_invoke('GET', format('/access/simulate?principalId=%s&method=GET&path=/members', v_bob), v_headers, NULL::bytea), 200);
    IF v_body IS DISTINCT FROM jsonb_build_object(
           'allowed', true, 'reason', NULL, 'handler', 'list_members', 'access', 'permission',
           'requiredPermissions', jsonb_build_array('members.read'),
           'grantedVia', jsonb_build_array(jsonb_build_object('via', 'role', 'role', 'org.member', 'permission', 'members.read'))) THEN
        RAISE EXCEPTION 'TEST FAILED: simulating Bob GET /members names the granting role: %', v_body;
    END IF;
    v_body := pg_temp.expect_status('simulate bob DELETE /members/{id}',
        api.rest_invoke('GET', format('/access/simulate?principalId=%s&method=delete&path=/members/%s', v_bob, v_nil),
                        v_headers, NULL::bytea), 200);
    IF v_body->'allowed' IS DISTINCT FROM 'false' OR v_body->>'reason' IS DISTINCT FROM 'permission_missing'
       OR v_body->>'handler' IS DISTINCT FROM 'remove_member' OR v_body->'grantedVia' IS DISTINCT FROM '[]' THEN
        RAISE EXCEPTION 'TEST FAILED: simulating Bob DELETE /members/{id} is refused for a missing permission: %', v_body;
    END IF;
    v_body := pg_temp.expect_status('simulate a missing route',
        api.rest_invoke('GET', format('/access/simulate?principalId=%s&method=GET&path=/nowhere', v_bob), v_headers, NULL::bytea), 200);
    IF v_body->>'reason' IS DISTINCT FROM 'no_route' OR v_body->'allowed' IS DISTINCT FROM 'false' THEN
        RAISE EXCEPTION 'TEST FAILED: simulating a path no route serves answers no_route: %', v_body;
    END IF;
    PERFORM pg_temp.expect_status('simulate a person of another organization',
        api.rest_invoke('GET', format('/access/simulate?principalId=%s&method=GET&path=/members', v_stranger), v_headers, NULL::bytea), 404);
    PERFORM pg_temp.expect_status('simulate a malformed principalId',
        api.rest_invoke('GET', '/access/simulate?principalId=x&method=GET&path=/members', v_headers, NULL::bytea), 400);
    PERFORM pg_temp.expect_status('simulate a relative path',
        api.rest_invoke('GET', format('/access/simulate?principalId=%s&method=GET&path=members', v_bob), v_headers, NULL::bytea), 400);

    PERFORM pg_temp.test_act_as(NULL);
    IF (SELECT role_ids FROM membership.vw_permission WHERE permission_id = 'access.explore')
           IS DISTINCT FROM '{org.owner,org.admin,agent.delegate}'::text[]
       OR (SELECT handler_names FROM membership.vw_permission WHERE permission_id = 'access.explore')
           IS DISTINCT FROM '{list_access_handlers,list_access_paths,simulate_access}'::text[] THEN
        RAISE EXCEPTION 'TEST FAILED: membership.vw_permission names the roles and handlers of access.explore';
    END IF;

    RAISE NOTICE '  + access explorer: operations, paths and the simulation stay inside the request''s organization';
END $$;
