-- ============================================================================
-- Test: signing in through api.open_login_session
-- ============================================================================
-- The transport's only door. It provisions the person behind a login, links a
-- new login to an existing person only when both sides verified the email,
-- keeps every user of one enterprise connection distinct, takes MFA from the
-- amr claim, accepts only OIDC issuers this deployment lists, and creates no
-- organization.
-- ============================================================================

DO $$
DECLARE
    v_login    record;
    v_response api.http_response;
    v_alice    text;
    v_bob      text;
    v_orgs     bigint;
    v_members  bigint;
BEGIN
    RAISE DEBUG '→ Testing that a matching email does not hand over an account';

    PERFORM pg_temp.test_as_kernel();
    SELECT count(*) INTO v_orgs FROM membership.organization;
    SELECT count(*) INTO v_members FROM membership.organization_membership;
    PERFORM pg_temp.test_as_owner();

    PERFORM pg_temp.test_login('https://idp.example.com', 'victim-1', 'victim@example.test', 'Victim', true);

    SELECT * INTO v_login FROM api.open_login_session(
        'https://other-idp.example.com', 'attacker-9', 'victim@example.test', NULL, false, '{}');
    IF v_login.token IS NOT NULL OR v_login.problem IS DISTINCT FROM 'membership.login.email_unverified' THEN
        RAISE EXCEPTION 'TEST FAILED: an unverified login presenting another account''s email opened a session: %', row_to_json(v_login);
    END IF;
    IF pg_temp.test_person('https://other-idp.example.com|attacker-9') IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the attacker login was linked to an account';
    END IF;

    SELECT * INTO v_login FROM api.open_login_session(
        'https://other-idp.example.com', 'victim-2', 'victim@example.test', NULL, true, '{}');
    v_response := api.rest_invoke('GET', '/me', extensions.hstore('x-login-session', v_login.token), NULL::bytea);
    IF api.content_json((v_response).content)->>'email' IS DISTINCT FROM 'victim@example.test' THEN
        RAISE EXCEPTION 'TEST FAILED: a verified login onto a verified person did not link, got % %',
            row_to_json(v_login), convert_from((v_response).content, 'UTF8');
    END IF;
    RAISE DEBUG '  ✓ unverified collision refused, verified onto verified links';

    RAISE DEBUG '→ Testing that users of one enterprise connection stay distinct';

    SELECT * INTO v_login FROM api.open_login_session(
        'https://idp.example.com', 'samlp|acme-sso|alice', 'alice@acme-sso.example', NULL, true, '{}');
    v_response := api.rest_invoke('GET', '/me', extensions.hstore('x-login-session', v_login.token), NULL::bytea);
    PERFORM pg_temp.expect_status('samlp alice GET /me', v_response, 200);
    v_alice := api.content_json((v_response).content)->>'principalId';

    SELECT * INTO v_login FROM api.open_login_session(
        'https://idp.example.com', 'samlp|acme-sso|bob', 'bob@acme-sso.example', NULL, true, '{}');
    v_response := api.rest_invoke('GET', '/me', extensions.hstore('x-login-session', v_login.token), NULL::bytea);
    v_bob := api.content_json((v_response).content)->>'principalId';
    IF v_alice IS NULL OR v_bob IS NULL OR (v_alice = v_bob) IS NOT FALSE THEN
        RAISE EXCEPTION 'TEST FAILED: two users of one connection resolved to the same account (% / %)', v_alice, v_bob;
    END IF;
    RAISE DEBUG '  ✓ samlp|acme-sso|alice and samlp|acme-sso|bob are distinct people';

    RAISE DEBUG '→ Testing the issuer, the amr claim and the organizations';

    SELECT * INTO v_login FROM api.open_login_session('google', 'dev-door', 'dev-door@example.test', NULL, true, '{mfa}');
    IF v_login.token IS NOT NULL OR v_login.problem IS DISTINCT FROM 'membership.login.issuer_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: a development issuer signed in through api.open_login_session: %', row_to_json(v_login);
    END IF;
    SELECT * INTO v_login FROM api.open_login_session('https://nowhere.example.com', 'x', 'x@example.test', NULL, true, '{}');
    IF v_login.problem IS DISTINCT FROM 'membership.login.issuer_unknown' THEN
        RAISE EXCEPTION 'TEST FAILED: an unlisted issuer signed in: %', row_to_json(v_login);
    END IF;

    SELECT * INTO v_login FROM api.open_login_session(
        'https://idp.example.com', 'mfa-user', 'mfa-user@example.test', NULL, true, '{pwd,mfa}');
    PERFORM pg_temp.test_as_kernel();
    IF (SELECT l.is_mfa FROM membership.login_session l
            WHERE l.token_hash = membership.fn_session_token_hash(v_login.token)) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: amr [pwd, mfa] did not count as multi-factor';
    END IF;
    IF (SELECT l.is_mfa FROM membership.login_session l
        JOIN membership.external_identity ei ON ei.object_id = l.external_identity_object_id
        WHERE ei.subject = 'samlp|acme-sso|bob') THEN
        RAISE EXCEPTION 'TEST FAILED: an empty amr counted as multi-factor';
    END IF;

    IF (SELECT count(*) FROM membership.organization) IS DISTINCT FROM v_orgs
       OR (SELECT count(*) FROM membership.organization_membership) IS DISTINCT FROM v_members THEN
        RAISE EXCEPTION 'TEST FAILED: signing in created an organization or a membership';
    END IF;
    PERFORM pg_temp.test_as_owner();
    v_response := api.rest_invoke('GET', '/organizations', extensions.hstore('x-login-session', v_login.token), NULL::bytea);
    IF api.content_json((v_response).content)->'organizations' IS DISTINCT FROM '[]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: a new person has organizations: %', convert_from((v_response).content, 'UTF8');
    END IF;
    RAISE DEBUG '  ✓ OIDC issuers only, MFA from amr, sign-in creates no organization';
END $$;
