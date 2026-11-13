-- ============================================================================
-- Test: organization.transfer_ownership over JSON-RPC
-- ============================================================================
-- Alice owns Acme Corp, Bob is a member (fixture). Only the owner may move
-- ownership: an admin who is not the owner is refused, a target outside the
-- organization is refused, and neither changes anything.
-- ============================================================================

CREATE FUNCTION pg_temp.transfer(p_subject text, p_new_owner uuid)
RETURNS api.http_response LANGUAGE sql AS $$
    SELECT api.rpc_invoke(api.rpc_resolve('organization.transfer_ownership'),
        pg_temp.test_session_headers(p_subject, current_setting('test.acme_org_id')::uuid),
        convert_to(jsonb_build_object('jsonrpc', '2.0', 'id', 1, 'method', 'organization.transfer_ownership',
            'params', jsonb_build_object('newOwnerId', p_new_owner))::text, 'UTF8'))
$$;

CREATE FUNCTION pg_temp.acme_owner()
RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_owner  uuid;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    SELECT principal_object_id INTO v_owner FROM membership.vw_organization_owner
    WHERE organization_object_id = current_setting('test.acme_org_id')::uuid;
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN v_owner;
END $$;

DO $$
DECLARE
    v_acme       uuid := current_setting('test.acme_org_id')::uuid;
    v_alice      uuid;
    v_bob        uuid;
    v_bob_member uuid;
    v_outsider   uuid := pg_temp.test_login('google', 'outsider-716', 'outsider@example.test', NULL, true);
    v_response   api.http_response;
    v_error      jsonb;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    SELECT principal_object_id INTO v_alice FROM membership.vw_member
    WHERE organization_object_id = v_acme AND email = 'alice@acme.com';
    SELECT principal_object_id, membership_object_id INTO v_bob, v_bob_member FROM membership.vw_member
    WHERE organization_object_id = v_acme AND email = 'bob@acme.com';
    PERFORM pg_temp.test_as_owner();

    v_response := api.rest_invoke('PUT', format('/members/%s/roles/org.admin', v_bob_member),
        pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme), NULL::bytea);
    PERFORM pg_temp.expect_status('Alice must make Bob an admin', v_response, 201);

    v_response := pg_temp.transfer(current_setting('test.bob_subject'), v_bob);
    v_error := api.content_json((v_response).content)->'error';
    IF (v_response).status_code IS DISTINCT FROM 409 OR (v_error->>'code')::int IS DISTINCT FROM -32010
       OR v_error->'data'->>'code' IS DISTINCT FROM 'membership.ownership.not_owner'
       OR pg_temp.acme_owner() IS DISTINCT FROM v_alice THEN
        RAISE EXCEPTION 'TEST FAILED: an admin who is not the owner must be refused and change nothing: % %',
            (v_response).status_code, v_error;
    END IF;

    v_response := pg_temp.transfer(current_setting('test.alice_subject'), v_outsider);
    v_error := api.content_json((v_response).content)->'error';
    IF (v_response).status_code IS DISTINCT FROM 422 OR (v_error->>'code')::int IS DISTINCT FROM -32011
       OR v_error->'data'->'invalid-params' IS DISTINCT FROM '[{"name": "newOwnerId", "reason": "not_found"}]'::jsonb
       OR pg_temp.acme_owner() IS DISTINCT FROM v_alice THEN
        RAISE EXCEPTION 'TEST FAILED: a target outside the organization must be refused and change nothing: % %',
            (v_response).status_code, v_error;
    END IF;

    v_response := pg_temp.transfer(current_setting('test.alice_subject'), v_bob);
    IF (v_response).status_code IS DISTINCT FROM 200
       OR api.content_json((v_response).content)->'result'->>'ownerId' IS DISTINCT FROM v_bob::text
       OR pg_temp.acme_owner() IS DISTINCT FROM v_bob THEN
        RAISE EXCEPTION 'TEST FAILED: the owner''s transfer must move ownership: % %',
            (v_response).status_code, api.content_json((v_response).content);
    END IF;
    PERFORM pg_temp.test_as_kernel();
    IF NOT EXISTS (SELECT FROM membership.vw_member WHERE organization_object_id = v_acme
                   AND principal_object_id = v_alice AND 'org.admin' = ANY (role_ids)) THEN
        RAISE EXCEPTION 'TEST FAILED: the previous owner must keep org.admin';
    END IF;
    PERFORM pg_temp.test_as_owner();

    RAISE NOTICE '  + organization.transfer_ownership: only the owner moves it, to a member, in one step';
END $$;
