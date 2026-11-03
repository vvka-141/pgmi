-- ============================================================================
-- Test: provisioning a login (membership.fn_provision_login)
-- ============================================================================
-- Linking a new login to an existing person needs the email verified on BOTH
-- sides. Subjects may hold pipes, never whitespace. Signing in creates no
-- organization.

DO $$
DECLARE
    v_alice  uuid := current_setting('test.alice_id')::uuid;
    v_una    uuid := current_setting('test.una_id')::uuid;
    v_case   record;
    v_result membership.user_principal;
BEGIN
    PERFORM pg_temp.test_as_kernel();
    RAISE DEBUG '-> Provisioning: account linking';

    FOR v_case IN
        SELECT * FROM (VALUES
            ('unverified login onto a verified person',   'attacker-1', 'alice@example.test', false),
            ('verified login onto an unverified person',  'attacker-2', 'una@example.test',   true),
            ('unverified login onto an unverified person', 'attacker-3', 'una@example.test',   false)
        ) AS t(label, subject, email, verified)
    LOOP
        v_result := membership.fn_provision_login('other-idp', v_case.subject, v_case.email, NULL, v_case.verified);
        IF v_result.principal_object_id IS NOT NULL THEN
            RAISE EXCEPTION 'TEST FAILED: linked % (to %)', v_case.label, v_result.principal_object_id;
        END IF;
        IF EXISTS (SELECT FROM membership.external_identity WHERE issuer_id = 'other-idp' AND subject = v_case.subject) THEN
            RAISE EXCEPTION 'TEST FAILED: refused link still stored the login (%)', v_case.label;
        END IF;
    END LOOP;

    IF EXISTS (SELECT FROM membership.user_principal WHERE principal_object_id = v_una AND is_email_verified) THEN
        RAISE EXCEPTION 'TEST FAILED: a refused verified login marked the existing person''s email verified';
    END IF;

    v_result := membership.fn_provision_login('other-idp', 'alice-elsewhere', 'Alice@Example.test', NULL, true);
    IF v_result.principal_object_id IS DISTINCT FROM v_alice THEN
        RAISE EXCEPTION 'TEST FAILED: a verified login onto a verified person must link to %, got %',
            v_alice, v_result.principal_object_id;
    END IF;
    IF (SELECT count(*) FROM membership.external_identity WHERE user_principal_object_id = v_alice) IS DISTINCT FROM 2 THEN
        RAISE EXCEPTION 'TEST FAILED: alice should now have two logins';
    END IF;

    RAISE DEBUG '  + links only an email verified on both sides';
    PERFORM pg_temp.test_as_owner();
END $$;

DO $$
DECLARE
    v_person uuid;
BEGIN
    RAISE DEBUG '-> Provisioning: subjects';

    v_person := pg_temp.test_login('test', 'samlp|acme-sso|carol', 'carol@example.test', NULL, true);
    IF v_person IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a multi-pipe subject must be accepted';
    END IF;
    IF pg_temp.test_login('test', 'samlp|acme-sso|dave', 'dave@example.test', NULL, true)
       IS NOT DISTINCT FROM v_person THEN
        RAISE EXCEPTION 'TEST FAILED: two users of one connection collapsed into one person';
    END IF;

    IF api.content_json((api.rest_invoke('GET', '/me', pg_temp.test_session_headers('test|samlp|acme-sso|carol'),
                                         NULL::bytea)).content)->>'principalId' IS DISTINCT FROM v_person::text THEN
        RAISE EXCEPTION 'TEST FAILED: a session of a multi-pipe login must resolve to its person';
    END IF;

    IF pg_temp.test_login('test', 'erin smith', 'erin@example.test', NULL, true) IS NOT NULL
       OR pg_temp.test_login('test', E'erin\n', 'erin@example.test', NULL, true) IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a subject with whitespace must be refused';
    END IF;
    PERFORM pg_temp.test_as_kernel();
    IF EXISTS (SELECT FROM membership.user_principal WHERE email = 'erin@example.test') THEN
        RAISE EXCEPTION 'TEST FAILED: a refused subject still created a person';
    END IF;
    PERFORM pg_temp.test_as_owner();

    IF pg_temp.test_login('nowhere', 'frank-1', 'frank@example.test', NULL, true) IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a login from an unknown issuer must be refused';
    END IF;

    RAISE DEBUG '  + multi-pipe subjects accepted, whitespace and unknown issuers refused';
END $$;

DO $$
DECLARE
    v_orgs     bigint;
    v_members  bigint;
    v_person   membership.user_principal;
    v_response api.http_response;
    v_token    text;
BEGIN
    RAISE DEBUG '-> Provisioning: sign-in creates no organization';

    PERFORM pg_temp.test_as_kernel();
    SELECT count(*) INTO v_orgs FROM membership.organization;
    SELECT count(*) INTO v_members FROM membership.organization_membership;
    v_person := membership.fn_provision_login('test', 'newcomer-1', 'newcomer@example.test', 'Newcomer', true);
    IF v_person.principal_object_id IS NULL OR (v_person.is_email_verified) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: a new verified login must create a verified person';
    END IF;
    PERFORM pg_temp.test_as_owner();

    v_response := api.rest_invoke('GET', '/me',
        extensions.hstore(ARRAY['x-user-id', 'test|newcomer-2', 'x-user-email', 'newcomer2@example.test']), NULL::bytea);
    PERFORM pg_temp.test_as_kernel();
    IF (v_response).status_code IS DISTINCT FROM 401
       OR EXISTS (SELECT FROM membership.user_principal WHERE email = 'newcomer2@example.test') THEN
        RAISE EXCEPTION 'TEST FAILED: identity headers must provision no one, got %', (v_response).status_code;
    END IF;
    PERFORM pg_temp.test_as_owner();

    v_token := membership.fn_open_login_session('test', 'newcomer-2', 'newcomer2@example.test', NULL, true, '{}');
    v_response := api.rest_invoke('GET', '/me', extensions.hstore('x-login-session', v_token), NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'TEST FAILED: opening a login session must provision the person, got %', (v_response).status_code;
    END IF;

    IF to_regprocedure('api.open_dev_login_session(text, text, text, text)') IS NOT NULL THEN
        EXECUTE $q$SELECT api.open_dev_login_session('test', 'newcomer-3', 'newcomer3@example.test', NULL)$q$ INTO v_token;
        PERFORM pg_temp.test_as_kernel();
        IF NOT EXISTS (SELECT FROM membership.login_session l
                       WHERE l.token_hash = membership.fn_session_token_hash(v_token)
                         AND l.is_mfa AND l.authentication_methods = '{mfa}') THEN
            RAISE EXCEPTION 'TEST FAILED: dev impersonation must open a multi-factor session (amr [mfa])';
        END IF;
        PERFORM pg_temp.test_as_owner();
        EXECUTE $q$SELECT api.open_dev_login_session('https://idp.example.com', 'x', 'x@example.test', NULL)$q$ INTO v_token;
        IF v_token IS NOT NULL THEN
            RAISE EXCEPTION 'TEST FAILED: dev impersonation signed in through a non-development issuer';
        END IF;
    END IF;

    PERFORM pg_temp.test_as_kernel();
    IF (SELECT count(*) FROM membership.organization) IS DISTINCT FROM v_orgs
       OR (SELECT count(*) FROM membership.organization_membership) IS DISTINCT FROM v_members THEN
        RAISE EXCEPTION 'TEST FAILED: signing in created an organization or a membership';
    END IF;

    v_person := membership.fn_provision_login('test', 'newcomer-1', 'renamed@example.test', 'Newcomer Renamed', false);
    IF v_person.email IS DISTINCT FROM 'renamed@example.test' OR v_person.is_email_verified THEN
        RAISE EXCEPTION 'TEST FAILED: a known login must refresh the email, and an unverified new address is unverified: %', v_person;
    END IF;
    PERFORM pg_temp.test_as_owner();

    RAISE NOTICE '  + provisioning: verified linking, multi-pipe subjects, no organization at sign-in';
END $$;
