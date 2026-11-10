-- ============================================================================
-- Test: no kernel reaches another organization
-- ============================================================================
-- A kernel runs as the kernel role, which passes every row, so its own reach
-- check is all that keeps a request inside its organization. This guard is
-- driven by the catalog: it calls every routine the kernel role owns and the
-- owner (every handler) may call, from a request in organization A, with
-- organization B's ids in every id parameter, and requires that the call
-- refuses (raises, or returns nothing: NULL, false, no rows, a problem code),
-- leaves every membership row as it was, and that a *_problem function names
-- a problem. A kernel taking no id cannot name B and is skipped; a parameter
-- the guard cannot fill fails it, naming the kernel, so a new kernel is
-- covered the day it is handed to the kernel role.
-- ============================================================================

CREATE FUNCTION pg_temp.reach_state()
RETURNS text LANGUAGE plpgsql AS $$
DECLARE
    v_caller text := current_user;
    v_table  regclass;
    v_part   text;
    v_state  text := '';
BEGIN
    PERFORM pg_temp.test_as_kernel();
    FOR v_table IN SELECT oid FROM pg_class WHERE relnamespace = 'membership'::regnamespace AND relkind = 'r' ORDER BY relname LOOP
        EXECUTE format('SELECT coalesce(md5(string_agg(to_jsonb(t)::text, ''|'' ORDER BY to_jsonb(t)::text)), '''') FROM %s t', v_table)
        INTO v_part;
        v_state := v_state || v_table::text || '=' || v_part || ';';
    END LOOP;
    EXECUTE format('SET ROLE %I', v_caller);
    RETURN md5(v_state);
END $$;

DO $$
DECLARE
    v_a_owner  uuid := pg_temp.test_login('test', 'reach-a-owner', 'reach-a-owner@example.test');
    v_b_owner  uuid := pg_temp.test_login('test', 'reach-b-owner', 'reach-b-owner@example.test');
    v_b_member uuid := pg_temp.test_login('test', 'reach-b-member', 'reach-b-member@example.test');
    v_c_owner  uuid := pg_temp.test_login('test', 'reach-c-owner', 'reach-c-owner@example.test');
    v_a        uuid;
    v_b        uuid;
    v_c        uuid;
    v_headers  extensions.hstore;
    v_id       jsonb;
    v_kernel   record;
    v_args     text;
    v_sql      text;
    v_before   text;
    v_problem  text;
    v_failed   text[] := '{}';
    v_checked  int := 0;
    v_scope    uuid;
    v_scope_label text;
BEGIN
    RAISE DEBUG '-> Kernel reach: a request in one organization gets nothing from another';

    v_a := (membership.fn_create_organization_with_owner('Reach A', 'reach-a', v_a_owner)).object_id;
    v_b := (membership.fn_create_organization_with_owner('Reach B', 'reach-b', v_b_owner)).object_id;
    v_c := (membership.fn_create_organization_with_owner('Reach C', 'reach-c', v_c_owner)).object_id;
    PERFORM membership.fn_add_member(v_b, v_b_member, 'org.member');
    PERFORM membership.fn_invite(v_b, 'reach-invitee@example.test', 'org.viewer');
    PERFORM membership.fn_invite(v_c, 'reach-b-member@example.test', 'org.viewer');
    v_headers := pg_temp.test_session_headers('test|reach-b-member', v_b);
    PERFORM pg_temp.test_mcp_context('test|reach-b-member', v_b);

    PERFORM pg_temp.test_as_kernel();
    INSERT INTO membership.pending_connection (principal_object_id, issuer_id, client_id, pairing_code, expires_at)
    VALUES (v_b_member, 'test', 'reach-client', membership.fn_new_pairing_code(), clock_timestamp() + interval '1 hour');
    INSERT INTO membership.proposal (organization_object_id, tool_name, arguments, proposed_by_object_id, proposer_kind)
    VALUES (v_b, 'reach_probe', '{}', v_b_member, 'human');
    SELECT jsonb_build_object(
        'p_organization_object_id', v_b,
        'p_membership_object_id', (SELECT object_id FROM membership.organization_membership
                                   WHERE organization_object_id = v_b AND principal_object_id = v_b_member),
        'p_invitation_object_id', (SELECT object_id FROM membership.invitation WHERE organization_object_id = v_b),
        'p_principal_object_id', v_b_member,
        'p_owner_principal_object_id', v_b_member,
        'p_new_owner_principal_object_id', v_b_member,
        'p_login_session_object_id', (SELECT object_id FROM membership.login_session
                                      WHERE token_hash = membership.fn_session_token_hash(v_headers->'x-login-session')),
        'p_organization_session_object_id', (SELECT object_id FROM membership.organization_session
                                             WHERE token_hash = membership.fn_session_token_hash(v_headers->'x-organization-session')),
        'p_connection_object_id', (SELECT object_id FROM membership.connection WHERE principal_object_id = v_b_member),
        'p_external_identity_object_id', (SELECT object_id FROM membership.external_identity
                                          WHERE user_principal_object_id = v_b_member),
        'p_role_id', 'org.admin', 'p_reason', 'removed', 'p_status', 'archived', 'p_name', 'Reach probe',
        'p_slug', 'reach-probe', 'p_email', 'reach-b-member@example.test', 'p_display_name', 'Probe',
        'p_issuer_id', 'test', 'p_subject', 'reach-b-member', 'p_is_email_verified', true, 'p_amr', '{mfa}',
        'p_previous_owner_becomes_admin', true, 'p_action', 'membership.reach.probe', 'p_detail', '{}',
        'p_method', 'GET', 'p_url', '/members', 'p_client_name', 'Reach probe', 'p_valid_days', 1,
        'p_operation', 'revoke', 'p_enabled', false,
        'p_proposal_object_id', (SELECT object_id FROM membership.proposal WHERE organization_object_id = v_b),
        'p_pairing_code', (SELECT pairing_code FROM membership.pending_connection WHERE principal_object_id = v_b_member))
    INTO v_id;
    PERFORM pg_temp.test_as_owner();
    IF EXISTS (SELECT FROM jsonb_each(v_id) WHERE value = 'null') THEN
        RAISE EXCEPTION 'TEST SETUP: organization B lacks a row the guard needs: %', v_id;
    END IF;

    FOREACH v_scope IN ARRAY ARRAY[v_a, NULL]::uuid[] LOOP
        v_scope_label := CASE WHEN v_scope IS NULL THEN 'signed in, no organization' ELSE 'in A' END;
        FOR v_kernel IN
            SELECT p.oid::regprocedure AS routine, format('%I.%I', p.pronamespace::regnamespace, p.proname) AS call, p.proname,
                   array_agg(a.name ORDER BY a.ord) AS names,
                   array_agg(format_type(a.type, NULL) ORDER BY a.ord) AS types
            FROM pg_proc p
            CROSS JOIN LATERAL unnest(p.proargnames[1:p.pronargs], p.proargtypes::oid[]) WITH ORDINALITY a(name, type, ord)
            WHERE p.proowner = pg_temp.deployment_setting('database_kernel_role')::regrole
              AND has_function_privilege(pg_temp.deployment_setting('database_owner_role'), p.oid, 'EXECUTE')
              AND p.pronargs > 0
            GROUP BY p.oid
            HAVING bool_or(a.type = 'uuid'::regtype)
            ORDER BY 1::text
        LOOP
            IF EXISTS (SELECT FROM unnest(v_kernel.names) n WHERE NOT v_id ? n) THEN
                v_failed := v_failed || format('%s: the guard cannot fill %s', v_kernel.call,
                    (SELECT string_agg(n, ', ') FROM unnest(v_kernel.names) n WHERE NOT v_id ? n));
                CONTINUE;
            END IF;
            SELECT string_agg(format('%L::%s', v_id->>n, t), ', ' ORDER BY o) INTO v_args
            FROM unnest(v_kernel.names, v_kernel.types) WITH ORDINALITY u(n, t, o);
            v_sql := format('SELECT coalesce(jsonb_agg(to_jsonb(x)), ''[]'') FROM (SELECT * FROM %s(%s) r) x',
                            v_kernel.call, v_args);

            v_before := pg_temp.reach_state();
            BEGIN
                PERFORM pg_temp.test_act_as('test|reach-a-owner', v_scope, true);
                DECLARE
                    v_result jsonb;
                    v_leaked boolean;
                BEGIN
                    EXECUTE v_sql INTO v_result;
                    PERFORM pg_temp.test_act_as(NULL);
                    v_leaked := CASE WHEN v_kernel.proname LIKE '%\_problem'
                                     THEN jsonb_path_exists(v_result, '$[*].r ? (@.type() == "null")')
                                     ELSE jsonb_path_exists(v_result, '$[*].* ? (!(@.type() == "null") && !(@.type() == "boolean" && @ == false))')
                                          AND NOT jsonb_path_exists(v_result, '$[*].problem ? (!(@.type() == "null"))') END;
                    v_problem := concat_ws(' and ',
                        CASE WHEN v_leaked THEN 'returned ' || v_result::text END,
                        CASE WHEN pg_temp.reach_state() IS DISTINCT FROM v_before THEN 'changed membership rows' END);
                END;
                RAISE EXCEPTION USING ERRCODE = 'P0004', MESSAGE = coalesce(v_problem, '');
            EXCEPTION
                WHEN assert_failure THEN
                    IF SQLERRM IS DISTINCT FROM '' THEN
                        v_failed := v_failed || format('%s (%s) %s', v_kernel.call, v_scope_label, SQLERRM);
                    END IF;
                WHEN insufficient_privilege OR undefined_function OR syntax_error OR invalid_text_representation THEN
                    v_failed := v_failed || format('%s (%s) could not be called: %s', v_kernel.call, v_scope_label, SQLERRM);
                WHEN OTHERS THEN
                    -- Any other error is the kernel refusing: nothing reached B.
                    NULL;
            END;
            v_checked := v_checked + 1;
        END LOOP;
    END LOOP;

    IF cardinality(v_failed) > 0 THEN
        RAISE EXCEPTION E'TEST FAILED: kernel(s) reached organization B from a request outside it:\n  %',
            array_to_string(v_failed, E'\n  ');
    END IF;
    IF coalesce(v_checked, 0) < 30 THEN
        RAISE EXCEPTION 'TEST FAILED: the guard checked only % kernels', v_checked;
    END IF;
    RAISE DEBUG '  + % kernels refuse another organization''s ids', v_checked;
END $$;

-- A person signed in without an organization session creates an organization
-- and becomes its owner: the kernel adds the owner to the organization it
-- has just created, which no reach check could name beforehand.
DO $$
DECLARE
    v_founder uuid := pg_temp.test_login('test', 'reach-founder', 'reach-founder@example.test');
    v_org     membership.organization;
BEGIN
    PERFORM pg_temp.test_act_as('test|reach-founder');
    v_org := membership.fn_create_organization_with_owner('Founded', 'reach-founded', v_founder);
    PERFORM pg_temp.test_act_as(NULL);
    PERFORM pg_temp.test_as_kernel();
    IF v_org.object_id IS NULL
       OR NOT EXISTS (SELECT FROM membership.vw_organization_owner ow
                      WHERE ow.organization_object_id = v_org.object_id AND ow.principal_object_id = v_founder) THEN
        RAISE EXCEPTION 'TEST FAILED: a person with no organization session could not found an organization they own: %', row_to_json(v_org);
    END IF;
    PERFORM pg_temp.test_as_owner();
END $$;
