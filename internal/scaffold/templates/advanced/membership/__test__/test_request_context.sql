-- ============================================================================
-- Test: the request context
-- ============================================================================
-- The trust gate, sessions that resolve to nothing, the one organization of a
-- request, MFA on platform permissions, and the once-per-request snapshot.
-- internal.resolve_session_context is what the gateways call with the two
-- session handles of a request.

CREATE FUNCTION pg_temp.context_of(p_headers extensions.hstore)
RETURNS TABLE (principal uuid, organization uuid, permissions text[], operator boolean)
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM internal.resolve_session_context(p_headers->'x-login-session', p_headers->'x-organization-session');
    RETURN QUERY SELECT api.current_principal_id(), api.current_organization_id(),
                        api.current_permissions(), api.is_platform_operator();
END $$;

DO $$
DECLARE
    v_alice   uuid := current_setting('test.alice_id')::uuid;
    v_corp    uuid := current_setting('test.corp_id')::uuid;
    v_role    text := current_user;
    v_trusted record;
    v_forged  record;
BEGIN
    RAISE DEBUG '-> Request context: the trust gate';

    PERFORM pg_temp.test_act_as('test|alice-001', v_corp);
    SELECT api.current_principal_id() AS principal, api.current_organization_id() AS org INTO v_trusted;
    IF v_trusted.principal IS DISTINCT FROM v_alice OR v_trusted.org IS DISTINCT FROM v_corp THEN
        RAISE EXCEPTION 'TEST FAILED: control: the deploy login''s settings must resolve, got %', row_to_json(v_trusted);
    END IF;
    PERFORM pg_temp.test_as_kernel();
    PERFORM membership.fn_snapshot_request_context();
    PERFORM pg_temp.test_as_owner();

    -- SET SESSION AUTHORIZATION, not SET ROLE: the gate asks session_user.
    -- Only a superuser may switch it, so a deploy by a scoped login checks the
    -- control above alone.
    IF NOT (SELECT rolsuper FROM pg_roles WHERE rolname = session_user) THEN
        PERFORM pg_temp.test_act_as(NULL);
        RAISE DEBUG '  + trust gate control only (not a superuser deploy)';
        RETURN;
    END IF;
    RESET ROLE;
    EXECUTE format('SET SESSION AUTHORIZATION %I', pg_temp.deployment_setting('database_owner_role'));
    SELECT membership.fn_is_trusted_context() AS trusted,
           api.current_principal_id() AS principal,
           api.current_organization_id() AS org,
           api.current_permissions() AS permissions,
           api.is_platform_operator() AS operator,
           api.is_authenticated() AS authenticated,
           api.current_login_session_id() AS login
      INTO v_forged;
    RESET SESSION AUTHORIZATION;
    EXECUTE format('SET ROLE %I', v_role);
    PERFORM pg_temp.test_act_as(NULL);

    IF v_forged.trusted IS DISTINCT FROM false OR v_forged.principal IS NOT NULL OR v_forged.org IS NOT NULL
       OR v_forged.permissions IS DISTINCT FROM '{}' OR v_forged.operator IS DISTINCT FROM false
       OR v_forged.authenticated IS DISTINCT FROM false OR v_forged.login IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a login outside the api role stated a context through auth.*: %', row_to_json(v_forged);
    END IF;
    RAISE DEBUG '  + a login outside the api role finds no principal, organization or permission, even with a snapshot';
END $$;

DO $$
DECLARE
    v_alice   uuid := current_setting('test.alice_id')::uuid;
    v_corp    uuid := current_setting('test.corp_id')::uuid;
    v_member  uuid := current_setting('test.alice_member_id')::uuid;
    v_headers extensions.hstore;
    v_other   extensions.hstore;
    v_ctx     record;
BEGIN
    RAISE DEBUG '-> Request context: sessions';

    v_headers := pg_temp.test_session_headers('test|alice-001', v_corp);
    SELECT * INTO v_ctx FROM pg_temp.context_of(v_headers);
    IF v_ctx.principal IS DISTINCT FROM v_alice OR v_ctx.organization IS DISTINCT FROM v_corp
       OR ('task.write' = ANY (v_ctx.permissions)) IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: control: a live login and organization session must resolve, got %', row_to_json(v_ctx);
    END IF;

    SELECT * INTO v_ctx FROM pg_temp.context_of(v_headers - 'x-organization-session'::text);
    IF v_ctx.principal IS DISTINCT FROM v_alice OR v_ctx.organization IS NOT NULL OR v_ctx.permissions IS DISTINCT FROM '{}' THEN
        RAISE EXCEPTION 'TEST FAILED: without an organization session there is no organization, got %', row_to_json(v_ctx);
    END IF;

    v_other := pg_temp.test_session_headers('test|alice-001');
    SELECT * INTO v_ctx FROM pg_temp.context_of(v_other || (v_headers - 'x-login-session'::text));
    IF v_ctx.principal IS DISTINCT FROM v_alice OR v_ctx.organization IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an organization session crossed into another login session of the same person: %', row_to_json(v_ctx);
    END IF;
    v_other := pg_temp.test_session_headers('test|bob-001');
    SELECT * INTO v_ctx FROM pg_temp.context_of(v_other || (v_headers - 'x-login-session'::text));
    IF v_ctx.organization IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an organization session crossed into another person''s login session';
    END IF;
    RAISE DEBUG '  + an organization session holds only within its own login session';

    PERFORM pg_temp.test_as_kernel();
    UPDATE membership.login_session SET idle_expires_at = now() - interval '1 second'
    WHERE token_hash = membership.fn_session_token_hash(v_headers->'x-login-session');
    PERFORM pg_temp.test_as_owner();
    SELECT * INTO v_ctx FROM pg_temp.context_of(v_headers);
    IF v_ctx.principal IS NOT NULL OR v_ctx.organization IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an idle-expired session resolved: %', row_to_json(v_ctx);
    END IF;

    v_headers := pg_temp.test_session_headers('test|alice-001', v_corp);
    PERFORM pg_temp.test_as_kernel();
    UPDATE membership.login_session SET absolute_expires_at = now() - interval '1 second'
    WHERE token_hash = membership.fn_session_token_hash(v_headers->'x-login-session');
    PERFORM pg_temp.test_as_owner();
    IF (SELECT principal FROM pg_temp.context_of(v_headers)) IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an absolutely expired session resolved';
    END IF;

    v_headers := pg_temp.test_session_headers('test|alice-001', v_corp);
    IF (api.close_login_session(v_headers->'x-login-session')) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: logout found no session to revoke';
    END IF;
    SELECT * INTO v_ctx FROM pg_temp.context_of(v_headers);
    IF v_ctx.principal IS NOT NULL OR v_ctx.organization IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a revoked session resolved: %', row_to_json(v_ctx);
    END IF;

    v_headers := pg_temp.test_session_headers('test|alice-001', v_corp);
    PERFORM pg_temp.test_as_kernel();
    PERFORM membership.fn_end_organization_session(
        (SELECT object_id FROM membership.organization_session
          WHERE token_hash = membership.fn_session_token_hash(v_headers->'x-organization-session')));
    PERFORM pg_temp.test_as_owner();
    SELECT * INTO v_ctx FROM pg_temp.context_of(v_headers);
    IF v_ctx.principal IS DISTINCT FROM v_alice OR v_ctx.organization IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: an ended organization session still fixed the organization: %', row_to_json(v_ctx);
    END IF;

    v_headers := pg_temp.test_session_headers('test|alice-001', v_corp);
    PERFORM membership.fn_disable_principal(v_alice, 'test lockout');
    SELECT * INTO v_ctx FROM pg_temp.context_of(v_headers);
    IF v_ctx.principal IS NOT NULL OR v_ctx.organization IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: a disabled person''s session resolved: %', row_to_json(v_ctx);
    END IF;
    PERFORM membership.fn_enable_principal(v_alice);

    PERFORM internal.resolve_session_context(NULL, NULL);
    PERFORM membership.fn_end_membership(v_member, 'removed');
    SELECT * INTO v_ctx FROM pg_temp.context_of(v_headers);
    IF v_ctx.principal IS DISTINCT FROM v_alice OR v_ctx.organization IS NOT NULL OR v_ctx.permissions IS DISTINCT FROM '{}' THEN
        RAISE EXCEPTION 'TEST FAILED: an ended membership kept its organization session: %', row_to_json(v_ctx);
    END IF;
    PERFORM pg_temp.test_as_kernel();
    IF (membership.fn_open_organization_session(
            (SELECT object_id FROM membership.login_session
              WHERE token_hash = membership.fn_session_token_hash(v_headers->'x-login-session')), v_corp)).problem
       IS DISTINCT FROM 'not_a_member' THEN
        RAISE EXCEPTION 'TEST FAILED: a former member opened an organization session';
    END IF;
    PERFORM pg_temp.test_as_owner();

    PERFORM internal.resolve_session_context(NULL, NULL);
    RAISE DEBUG '  + expired, revoked, ended, disabled-person and ended-membership sessions resolve to nothing';
END $$;

DO $$
DECLARE
    v_bob   uuid := current_setting('test.bob_id')::uuid;
    v_token text;
    v_ctx   record;
BEGIN
    RAISE DEBUG '-> Request context: MFA gates platform permissions';

    PERFORM pg_temp.test_operator(v_bob);

    SELECT * INTO v_ctx FROM pg_temp.context_of(pg_temp.test_session_headers('test|bob-001', NULL, '{pwd}'));
    IF pg_temp.deployment_setting('platform_mfa_required') = 'true' THEN
        IF v_ctx.operator IS DISTINCT FROM false OR ('platform.operate' = ANY (v_ctx.permissions)) IS NOT FALSE THEN
            RAISE EXCEPTION 'TEST FAILED: a session without MFA used a platform permission: %', row_to_json(v_ctx);
        END IF;
    ELSIF v_ctx.operator IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: with platform_mfa_required=false an operator acts without MFA';
    END IF;

    SELECT * INTO v_ctx FROM pg_temp.context_of(pg_temp.test_session_headers('test|bob-001', NULL, '{pwd,mfa}'));
    IF v_ctx.operator IS DISTINCT FROM true OR v_ctx.permissions IS DISTINCT FROM ARRAY['platform.operate'] THEN
        RAISE EXCEPTION 'TEST FAILED: a session with MFA must use the platform permission: %', row_to_json(v_ctx);
    END IF;

    IF to_regprocedure('api.open_dev_login_session(text, text, text, text)') IS NOT NULL THEN
        EXECUTE $q$SELECT api.open_dev_login_session('test', 'bob-001', 'bob@example.test', NULL)$q$ INTO v_token;
        SELECT * INTO v_ctx FROM pg_temp.context_of(extensions.hstore('x-login-session', v_token));
        IF v_ctx.operator IS DISTINCT FROM true THEN
            RAISE EXCEPTION 'TEST FAILED: dev impersonation must pass the MFA gate';
        END IF;
    END IF;

    PERFORM internal.resolve_session_context(NULL, NULL);
    RAISE DEBUG '  + platform permissions need amr mfa; dev impersonation has it';
END $$;

DO $$
DECLARE
    v_corp    uuid := current_setting('test.corp_id')::uuid;
    v_owner   uuid := current_setting('test.owner_id')::uuid;
    v_headers extensions.hstore := pg_temp.test_session_headers('test|owner-001', v_corp);
BEGIN
    RAISE DEBUG '-> Request context: resolved once per request';
    IF (v_headers ? 'x-organization-session') IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST SETUP: the owner could not open an organization session';
    END IF;

    PERFORM internal.resolve_session_context(v_headers->'x-login-session', v_headers->'x-organization-session');
    PERFORM pg_temp.test_as_kernel();
    UPDATE membership.role_assignment SET valid_during = tstzrange(lower(valid_during), clock_timestamp(), '[)')
    WHERE organization_object_id = v_corp AND upper_inf(valid_during)
      AND membership_object_id IN (SELECT object_id FROM membership.organization_membership WHERE principal_object_id = v_owner);
    PERFORM pg_temp.test_as_owner();
    IF (api.has_permission('task.write')) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: the readers must return the request''s snapshot, not re-resolve on every call';
    END IF;

    PERFORM internal.resolve_session_context(v_headers->'x-login-session', v_headers->'x-organization-session');
    IF api.has_permission('task.write') THEN
        RAISE EXCEPTION 'TEST FAILED: the next request must see the revoked role';
    END IF;

    PERFORM pg_temp.test_act_as('test|owner-001', v_corp);
    IF api.current_permissions() IS DISTINCT FROM '{}' THEN
        RAISE EXCEPTION 'TEST FAILED: changed inputs must resolve live, not from the old snapshot';
    END IF;

    PERFORM pg_temp.test_act_as(NULL);
    RAISE DEBUG '  + the context is a snapshot per request, re-resolved when its inputs change';
END $$;
