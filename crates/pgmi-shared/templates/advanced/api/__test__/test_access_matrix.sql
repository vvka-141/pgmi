-- ============================================================================
-- Test: the access matrix
-- ============================================================================
-- Every permission-guarded REST handler is called by a person holding each
-- shipped member role (agent.* roles are never a member's), and by a
-- platform operator. The expectation below is written
-- out by hand, not derived from data/authorization.json: a role that gains
-- or loses a permission, or a handler that changes what it declares, fails
-- here until this table is changed with it. A handler added with
-- "permissions" fails here until it has a row.
--
-- Each handler's function is replaced, for this test only, by a probe that
-- records that it ran. An allowed call must reach it; a refused call answers
-- 403 with the gateway's fixed body, is logged in api.access_denial, and must
-- not reach it. membership.fn_simulate_access must predict every one of
-- these answers: allowed, or refused for the same reason.
-- ============================================================================

DO $$
DECLARE
    v_nil      text := '00000000-0000-4000-8000-000000000000';
    v_owner    uuid;
    v_org      uuid;
    v_operator uuid;
    v_persona  record;
    v_call     record;
    v_handler  record;
    v_response api.http_response;
    v_before   bigint;
    v_after    bigint;
    v_allowed  boolean;
    v_sim      record;
    v_missing  text;
BEGIN
    RAISE DEBUG '-> Access matrix';
    PERFORM pg_temp.test_act_as(NULL);

    CREATE TEMP TABLE matrix_expectation (handler text PRIMARY KEY, method text, url text, body jsonb, allowed text[])
        ON COMMIT DROP;
    INSERT INTO matrix_expectation VALUES
        ('list_members',              'GET',    '/members',                                     NULL,
            '{org.owner,org.admin,org.member,org.viewer}'),
        ('remove_member',             'DELETE', '/members/' || v_nil,                           NULL,
            '{org.owner,org.admin}'),
        ('grant_member_role',         'PUT',    '/members/' || v_nil || '/roles/org.viewer',    NULL,
            '{org.owner,org.admin}'),
        ('revoke_member_role',        'DELETE', '/members/' || v_nil || '/roles/org.viewer',    NULL,
            '{org.owner,org.admin}'),
        ('invite_member',             'POST',   '/invitations',
            '{"email": "matrix-invitee@example.test", "roleId": "org.viewer"}',
            '{org.owner,org.admin}'),
        ('revoke_invitation',         'DELETE', '/invitations/' || v_nil,                       NULL,
            '{org.owner,org.admin}'),
        ('list_access_handlers',      'GET',    '/access/handlers',                             NULL,
            '{org.owner,org.admin}'),
        ('list_access_paths',         'GET',    '/access/paths',                                NULL,
            '{org.owner,org.admin}'),
        ('simulate_access',           'GET',    '/access/simulate?principalId=' || v_nil || '&method=GET&path=/members',
            NULL, '{org.owner,org.admin}'),
        ('list_agents',               'GET',    '/agents',                                      NULL,
            '{org.owner,org.admin}'),
        ('pause_agent',               'POST',   '/agents/' || v_nil || '/pause',                NULL,
            '{org.owner,org.admin}'),
        ('resume_agent',              'POST',   '/agents/' || v_nil || '/resume',               NULL,
            '{org.owner,org.admin}'),
        ('revoke_agent',              'DELETE', '/agents/' || v_nil,                            NULL,
            '{org.owner,org.admin}'),
        ('disable_agents',            'POST',   '/agents/disable',                              NULL,
            '{org.owner,org.admin}'),
        ('enable_agents',             'POST',   '/agents/enable',                               NULL,
            '{org.owner,org.admin}'),
        ('list_proposals',            'GET',    '/proposals',                                   NULL,
            '{org.owner,org.admin}'),
        ('confirm_proposal',          'POST',   '/proposals/' || v_nil || '/confirm',           NULL,
            '{org.owner,org.admin}'),
        ('reject_proposal',           'POST',   '/proposals/' || v_nil || '/reject',            NULL,
            '{org.owner,org.admin}'),
        ('admin_dashboard',           'GET',    '/admin/dashboard',                             NULL, '{operator}'),
        ('admin_handlers',            'GET',    '/admin/handlers',                              NULL, '{operator}'),
        ('admin_handler_stats',       'GET',    '/admin/handlers/stats',                        NULL, '{operator}'),
        ('admin_routes',              'GET',    '/admin/routes',                                NULL, '{operator}'),
        ('admin_exchanges',           'GET',    '/admin/exchanges',                             NULL, '{operator}'),
        ('admin_exchange_stats',      'GET',    '/admin/exchanges/stats',                       NULL, '{operator}'),
        ('admin_exchange_replay',     'GET',    '/admin/exchanges/' || v_nil || '/replay',      NULL, '{operator}'),
        ('admin_replay_exchange',     'POST',   '/admin/exchanges/' || v_nil || '/replay',      NULL, '{operator}'),
        ('admin_purge_exchanges',     'POST',   '/admin/maintenance/purge-exchanges',           NULL, '{operator}'),
        ('admin_create_organization', 'POST',   '/admin/organizations',
            '{"name": "Matrix New", "slug": "matrix-new", "ownerEmail": "matrix-new@example.test"}',
            '{operator}');

    SELECT string_agg(h.handler_function_name, ', ' ORDER BY h.handler_function_name) INTO v_missing
    FROM api.handler h
    WHERE h.handler_type = 'rest' AND h.access = 'permission'
      AND NOT EXISTS (SELECT FROM matrix_expectation e WHERE e.handler = h.handler_function_name);
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: permission-guarded REST handlers missing from the access matrix: %', v_missing;
    END IF;
    SELECT string_agg(e.handler, ', ' ORDER BY e.handler) INTO v_missing
    FROM matrix_expectation e
    WHERE NOT EXISTS (SELECT FROM api.handler h
                      WHERE h.handler_type = 'rest' AND h.access = 'permission' AND h.handler_function_name = e.handler);
    IF v_missing IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the access matrix names handlers that are not permission-guarded REST handlers: %', v_missing;
    END IF;

    v_owner := pg_temp.test_login('google', 'matrix-org.owner', 'matrix-owner@example.test', NULL, true);
    v_org := (membership.fn_create_organization_with_owner('Matrix', 'matrix-org', v_owner)).object_id;
    PERFORM membership.fn_add_member(v_org,
                pg_temp.test_login('google', 'matrix-' || r.id, 'matrix-' || replace(r.id, '.', '-') || '@example.test',
                                               NULL, true),
                r.id)
    FROM membership.role r
    WHERE r.id IS DISTINCT FROM 'org.owner' AND r.id NOT LIKE 'agent.%';
    v_operator := pg_temp.test_login('google', 'matrix-operator', 'matrix-operator@example.test', NULL, true);
    PERFORM pg_temp.test_operator(v_operator);

    CREATE TEMP TABLE matrix_persona ON COMMIT DROP AS
    SELECT r.id AS persona, pg_temp.test_person('google|matrix-' || r.id) AS principal,
           pg_temp.test_session_headers('google|matrix-' || r.id, v_org, ARRAY['pwd', 'mfa']) AS headers
    FROM membership.role r
    WHERE r.id NOT LIKE 'agent.%'
    UNION ALL
    SELECT 'operator', v_operator, pg_temp.test_session_headers('google|matrix-operator', NULL, ARRAY['pwd', 'mfa']);

    IF (SELECT count(*) FROM matrix_persona) IS DISTINCT FROM (SELECT count(*) + 1 FROM membership.role WHERE id NOT LIKE 'agent.%')
       OR EXISTS (SELECT FROM matrix_persona WHERE NOT headers ? 'x-login-session') THEN
        RAISE EXCEPTION 'TEST FAILED: every role and the operator need a signed-in persona';
    END IF;

    FOR v_handler IN
        SELECT n.nspname, p.proname
        FROM api.handler h
        JOIN pg_proc p ON p.oid = h.handler_func
        JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE h.handler_function_name IN (SELECT handler FROM matrix_expectation)
    LOOP
        EXECUTE format($f$CREATE OR REPLACE FUNCTION %I.%I(request api.rest_request) RETURNS api.http_response
                          LANGUAGE plpgsql AS $p$BEGIN
                              PERFORM set_config('test.body_ran', 'yes', true);
                              RETURN api.json_response(200, '{}'::jsonb);
                          END $p$ $f$, v_handler.nspname, v_handler.proname);
    END LOOP;

    FOR v_persona IN SELECT * FROM matrix_persona ORDER BY persona LOOP
        FOR v_call IN SELECT e.*, h.object_id FROM matrix_expectation e
                      JOIN api.handler h ON h.handler_type = 'rest' AND h.handler_function_name = e.handler
                      ORDER BY e.handler LOOP
            v_allowed := v_persona.persona = ANY (v_call.allowed);
            SELECT coalesce(sum(attempts), 0) INTO v_before FROM api.access_denial
            WHERE handler_object_id = v_call.object_id AND effective_principal_object_id = v_persona.principal;

            PERFORM set_config('test.body_ran', 'no', true);
            v_response := CASE WHEN v_call.body IS NULL
                THEN api.rest_invoke(v_call.method, v_call.url, v_persona.headers, NULL::bytea)
                ELSE api.rest_invoke(v_call.method, v_call.url, v_persona.headers, v_call.body) END;

            SELECT coalesce(sum(attempts), 0) INTO v_after FROM api.access_denial
            WHERE handler_object_id = v_call.object_id AND effective_principal_object_id = v_persona.principal;

            IF v_allowed AND ((v_response).status_code IS DISTINCT FROM 200
                              OR current_setting('test.body_ran') IS DISTINCT FROM 'yes'
                              OR v_after IS DISTINCT FROM v_before) THEN
                RAISE EXCEPTION 'TEST FAILED: % % % must be allowed: status %, body ran %, refusals logged %',
                    v_persona.persona, v_call.method, v_call.url, (v_response).status_code,
                    current_setting('test.body_ran'), v_after - v_before;
            END IF;
            IF (v_allowed) IS NOT TRUE AND ((v_response).status_code IS DISTINCT FROM 403
                                  OR current_setting('test.body_ran') IS DISTINCT FROM 'no'
                                  OR v_after IS DISTINCT FROM v_before + 1
                                  OR coalesce(api.content_json((v_response).content)->>'code', '')
                                         NOT IN ('pgmi.forbidden', 'pgmi.organization_required')) THEN
                RAISE EXCEPTION 'TEST FAILED: % % % must be refused: status %, body ran %, refusals logged %, body %',
                    v_persona.persona, v_call.method, v_call.url, (v_response).status_code,
                    current_setting('test.body_ran'), v_after - v_before, api.content_json((v_response).content);
            END IF;

            PERFORM pg_temp.test_act_as(NULL);
            SELECT * INTO v_sim FROM membership.fn_simulate_access(v_persona.principal, v_org, v_call.method, v_call.url);
            IF v_sim.allowed IS DISTINCT FROM v_allowed
               OR v_sim.handler_name IS DISTINCT FROM v_call.handler
               OR (v_allowed IS NOT TRUE AND CASE v_sim.reason WHEN 'organization_required' THEN 'pgmi.organization_required'
                                                       WHEN 'permission_missing' THEN 'pgmi.forbidden' END
                                     IS DISTINCT FROM api.content_json((v_response).content)->>'code') THEN
                RAISE EXCEPTION 'TEST FAILED: simulation disagrees with the gateway for % % %: simulated % (%) on %, gateway %',
                    v_persona.persona, v_call.method, v_call.url, v_sim.allowed, v_sim.reason, v_sim.handler_name,
                    (v_response).status_code;
            END IF;
        END LOOP;
    END LOOP;

    RAISE NOTICE '  + access matrix: % handlers x % personas as declared; refusals answer 403, are logged, never run the body; the simulation agrees',
        (SELECT count(*) FROM matrix_expectation), (SELECT count(*) FROM matrix_persona);
END $$;
