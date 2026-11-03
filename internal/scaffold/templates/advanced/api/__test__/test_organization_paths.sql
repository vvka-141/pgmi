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
    IF (v_response).status_code IS DISTINCT FROM 409 THEN
        RAISE EXCEPTION 'TEST FAILED: a taken slug expected 409, got %', (v_response).status_code;
    END IF;
    v_response := api.rest_invoke('POST', '/admin/organizations', v_headers,
        '{"name": "Nobody", "slug": "nobody-org", "ownerEmail": "not an address"}'::jsonb);
    IF (v_response).status_code IS DISTINCT FROM 422
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
        v_response := api.rest_invoke('POST', '/organizations', v_bob, '{"name": "Bob Again", "slug": "bob-builds"}'::jsonb);
        IF (v_response).status_code IS DISTINCT FROM 409 THEN
            RAISE EXCEPTION 'TEST FAILED: self-serve with a taken slug expected 409, got %', (v_response).status_code;
        END IF;
        v_response := api.rest_invoke('POST', '/organizations',
            pg_temp.test_session_headers(current_setting('test.bob_subject'), current_setting('test.acme_org_id')::uuid),
            '{"name": "Bob Elsewhere", "slug": "bob-elsewhere"}'::jsonb);
        IF (v_response).status_code IS DISTINCT FROM 409 THEN
            RAISE EXCEPTION 'TEST FAILED: self-serve inside an organization session expected 409, got %', (v_response).status_code;
        END IF;
    ELSIF (v_response).status_code IS DISTINCT FROM 405
          OR v_after IS DISTINCT FROM v_count THEN
        RAISE EXCEPTION 'TEST FAILED: without the self-serve handler POST /organizations must be 405 and create nothing, got %',
            (v_response).status_code;
    END IF;

    RAISE NOTICE '  + organizations: operator-created and invitation-activated; self-serve only where its handler is deployed';
END $$;
