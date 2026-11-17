-- ============================================================================
-- Test: how an organization comes to be
-- ============================================================================
-- Invitation-only by default: a platform operator creates a pending
-- organization for an owner's email, and it becomes active when that person
-- signs in with the verified address. The self-serve handler
-- (api/dev/self-serve-organizations.sql) deploys with env=dev alone; where it
-- is deployed, a signed-in person creates an organization they own, and
-- where it is not, nobody can.
-- ============================================================================

DO $$
DECLARE
    v_operator  uuid;
    v_headers   extensions.hstore;
    v_bob       extensions.hstore;
    v_response  api.http_response;
    v_body      jsonb;
    v_org       uuid;
    v_login     record;
    v_dana      uuid;
    v_count     bigint;
    v_after     bigint;
BEGIN
    RAISE DEBUG '-> Organization paths';

    v_operator := pg_temp.test_login('google', 'org-paths-operator', 'org-paths-operator@example.test', NULL, true);
    PERFORM pg_temp.test_operator(v_operator);
    v_headers := pg_temp.test_session_headers('google|org-paths-operator', NULL, ARRAY['pwd', 'mfa']);

    v_response := api.rest_invoke('POST', '/admin/organizations', v_headers,
        '{"name": "Dana Works", "slug": "dana-works", "ownerEmail": "Dana@Example.test"}'::jsonb);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 201 OR v_body->>'status' IS DISTINCT FROM 'pending' THEN
        RAISE EXCEPTION 'TEST FAILED: operator POST /admin/organizations expected 201 pending, got %: %',
            (v_response).status_code, v_body;
    END IF;
    v_org := (v_body->>'id')::uuid;
    PERFORM pg_temp.test_as_kernel();
    IF NOT EXISTS (SELECT FROM membership.invitation
                   WHERE organization_object_id = v_org AND email = 'dana@example.test' AND role_id = 'org.owner'
                     AND accepted_at IS NULL AND revoked_at IS NULL)
       OR EXISTS (SELECT FROM membership.organization_membership WHERE organization_object_id = v_org) THEN
        RAISE EXCEPTION 'TEST FAILED: the pending organization must have an owner invitation and no member';
    END IF;
    PERFORM pg_temp.test_as_owner();

    v_response := api.rest_invoke('POST', '/admin/organizations', v_headers,
        '{"name": "Acme Again", "slug": "acme-corp", "ownerEmail": "x@example.test"}'::jsonb);
    IF (v_response).status_code IS DISTINCT FROM 422
       OR api.content_json((v_response).content)->'invalid-params' IS DISTINCT FROM '[{"name": "slug", "reason": "duplicate"}]' THEN
        RAISE EXCEPTION 'TEST FAILED: a taken slug expected 422 naming slug, got %: %',
            (v_response).status_code, convert_from((v_response).content, 'UTF8');
    END IF;
    v_response := api.rest_invoke('POST', '/admin/organizations', v_headers,
        '{"name": "Nobody", "slug": "nobody-org", "ownerEmail": "not an address"}'::jsonb);
    IF (v_response).status_code IS DISTINCT FROM 422
       OR api.content_json((v_response).content)->'invalid-params' IS DISTINCT FROM '[{"name": "ownerEmail", "reason": "invalid"}]'
       OR pg_temp.test_organization('nobody-org') IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a bad owner email expected 422 and no organization, got %', (v_response).status_code;
    END IF;

    SELECT * INTO v_login FROM api.open_login_session(
        'https://idp.example.com', 'dana-org-paths', 'dana@example.test', 'Dana', true, ARRAY['pwd']);
    v_dana := pg_temp.test_person('https://idp.example.com|dana-org-paths');
    PERFORM pg_temp.test_as_kernel();
    IF v_login.token IS NULL
       OR (SELECT status FROM membership.organization WHERE object_id = v_org) IS DISTINCT FROM 'active'
       OR NOT EXISTS (SELECT FROM membership.vw_organization_owner
                      WHERE organization_object_id = v_org AND principal_object_id = v_dana) THEN
        RAISE EXCEPTION 'TEST FAILED: signing in with the invited, verified address must activate the organization with its owner (%)',
            v_login.problem;
    END IF;
    PERFORM pg_temp.test_as_owner();

    v_bob := pg_temp.test_session_headers(current_setting('test.bob_subject'));
    PERFORM pg_temp.test_as_kernel();
    SELECT count(*) INTO v_count FROM membership.organization;
    PERFORM pg_temp.test_as_owner();
    v_response := api.rest_invoke('POST', '/organizations', v_bob, '{"name": "Bob Builds", "slug": "bob-builds"}'::jsonb);
    PERFORM pg_temp.test_as_kernel();
    SELECT count(*) INTO v_after FROM membership.organization;
    PERFORM pg_temp.test_as_owner();

    IF EXISTS (SELECT FROM api.handler WHERE handler_type = 'rest' AND handler_function_name = 'create_organization') THEN
        v_body := api.content_json((v_response).content);
        PERFORM pg_temp.test_as_kernel();
        IF (v_response).status_code IS DISTINCT FROM 201 OR v_body->>'status' IS DISTINCT FROM 'active'
           OR NOT EXISTS (SELECT FROM membership.vw_organization_owner oo
                          WHERE oo.organization_object_id = (v_body->>'id')::uuid
                            AND oo.principal_object_id = pg_temp.test_person(current_setting('test.bob_subject'))) THEN
            RAISE EXCEPTION 'TEST FAILED: self-serve POST /organizations must create an active organization the caller owns, got %: %',
                (v_response).status_code, v_body;
        END IF;
        PERFORM pg_temp.test_as_owner();
        IF (v_response).headers->'location' IS DISTINCT FROM rtrim(api.openapi_server_url(), '/') || '/organizations/' || (v_body->>'id') THEN
            RAISE EXCEPTION 'TEST FAILED: self-serve POST /organizations must answer Location /organizations/% under the mount, got %',
                v_body->>'id', (v_response).headers->'location';
        END IF;
        v_response := api.rest_invoke('GET', '/organizations/' || (v_body->>'id'), v_bob, NULL::bytea);
        IF (v_response).status_code IS DISTINCT FROM 200
           OR api.content_json((v_response).content) IS DISTINCT FROM v_body THEN
            RAISE EXCEPTION 'TEST FAILED: GET on the Location of a new organization must answer it, got %: %',
                (v_response).status_code, convert_from((v_response).content, 'UTF8');
        END IF;
        v_response := api.rest_invoke('POST', '/organizations', v_bob, '{"name": "Bob Again", "slug": "bob-builds"}'::jsonb);
        IF (v_response).status_code IS DISTINCT FROM 422
           OR api.content_json((v_response).content)->'invalid-params' IS DISTINCT FROM '[{"name": "slug", "reason": "duplicate"}]' THEN
            RAISE EXCEPTION 'TEST FAILED: self-serve with a taken slug expected 422 naming slug, got %', (v_response).status_code;
        END IF;
        v_response := api.rest_invoke('POST', '/organizations',
            pg_temp.test_session_headers(current_setting('test.bob_subject'), current_setting('test.acme_org_id')::uuid),
            '{"name": "Bob Elsewhere", "slug": "bob-elsewhere"}'::jsonb);
        PERFORM pg_temp.expect_status('self-serve inside an organization session', v_response, 409);
    ELSIF (v_response).status_code IS DISTINCT FROM 405
          OR v_after IS DISTINCT FROM v_count THEN
        RAISE EXCEPTION 'TEST FAILED: without the self-serve handler POST /organizations must be 405 and create nothing, got %',
            (v_response).status_code;
    END IF;

    RAISE NOTICE '  + organizations: operator-created and invitation-activated; self-serve only where its handler is deployed';
END $$;

DO $$
DECLARE
    v_acme     uuid := current_setting('test.acme_org_id')::uuid;
    v_response api.http_response;
    v_body     jsonb;
BEGIN
    v_response := api.rest_invoke('GET', '/organizations/' || v_acme,
        pg_temp.test_session_headers(current_setting('test.alice_subject')), NULL::bytea);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 200 OR (v_body->>'id')::uuid IS DISTINCT FROM v_acme
       OR v_body->>'status' IS DISTINCT FROM 'active' THEN
        RAISE EXCEPTION 'TEST FAILED: a member''s GET /organizations/{id} must answer the organization, got %: %',
            (v_response).status_code, v_body;
    END IF;

    PERFORM pg_temp.test_login('google', 'org-paths-stranger', 'org-paths-stranger@example.test', NULL, true);
    v_response := api.rest_invoke('GET', '/organizations/' || v_acme,
        pg_temp.test_session_headers('google|org-paths-stranger'), NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 404
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'membership.organization.not_found' THEN
        RAISE EXCEPTION 'TEST FAILED: GET /organizations/{id} of an organization one is not a member of must be 404 membership.organization.not_found, got % %',
            (v_response).status_code, convert_from((v_response).content, 'UTF8');
    END IF;

    RAISE DEBUG '  + GET /organizations/{id}: a member reads it, anyone else gets 404';
END $$;
