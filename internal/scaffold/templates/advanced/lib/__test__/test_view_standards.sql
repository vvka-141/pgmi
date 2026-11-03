-- ============================================================================
-- Test: every view runs with its reader's rights
-- ============================================================================
-- A view without security_invoker reads its tables as the view's owner: it
-- skips the reader's own privileges and, on a table that is not forced, its
-- row policies. So every view of an application schema is security_invoker;
-- a view altered by hand to security_invoker = false is named here even when
-- core.ensure_view has not repaired it yet. The second block proves it live:
-- a throwaway reader is refused a view whose tables it cannot read, and with
-- them reads only its own organization's rows through it.
-- ============================================================================

CREATE FUNCTION pg_temp.definer_views()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT format('%I.%I', s.name, c.relname)
    FROM pg_class c
    JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
    WHERE c.relkind = 'v'
      AND NOT coalesce('security_invoker=true' = ANY (c.reloptions), false)
$$;

DO $$
DECLARE
    v_bad text;
BEGIN
    SELECT string_agg(v, ', ' ORDER BY v) INTO v_bad FROM pg_temp.definer_views() v;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: view(s) without security_invoker: %', v_bad
            USING HINT = 'CREATE OR REPLACE VIEW <name> WITH (security_invoker = true) AS ...';
    END IF;

    IF NOT EXISTS (SELECT FROM pg_class c JOIN pg_temp.application_schema() s ON s.oid = c.relnamespace
                   WHERE c.relkind = 'v') THEN
        RAISE EXCEPTION 'TEST FAILED: no view in any application schema; the scan is blind';
    END IF;

    CREATE VIEW core.vw_definer_probe AS SELECT 1 AS one;
    CREATE VIEW core.vw_switched_probe WITH (security_invoker = true) AS SELECT 1 AS one;
    ALTER VIEW core.vw_switched_probe SET (security_invoker = false);
    SELECT string_agg(v, ', ' ORDER BY v) INTO v_bad FROM pg_temp.definer_views() v;
    IF v_bad IS DISTINCT FROM 'core.vw_definer_probe, core.vw_switched_probe' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name a definer view and a view switched to the owner''s rights, named: %', v_bad;
    END IF;
    DROP VIEW core.vw_definer_probe, core.vw_switched_probe;
END $$;

DO $$
DECLARE
    v_owner  text := current_user;
    v_reader text := 'view_reader_probe_' || txid_current();
    v_a      uuid;
    v_b      uuid;
    v_seen   uuid[];
BEGIN
    v_a := (membership.fn_create_organization_with_owner('View A', 'view-standards-a',
              pg_temp.test_login('test', 'view-standards-a', 'view-standards-a@example.test'))).object_id;
    v_b := (membership.fn_create_organization_with_owner('View B', 'view-standards-b',
              pg_temp.test_login('test', 'view-standards-b', 'view-standards-b@example.test'))).object_id;

    RESET ROLE;
    EXECUTE format('CREATE ROLE %I NOLOGIN', v_reader);
    EXECUTE format('GRANT %I TO CURRENT_USER WITH INHERIT FALSE, SET TRUE', v_reader);
    EXECUTE format('SET ROLE %I', v_owner);
    EXECUTE format('GRANT USAGE ON SCHEMA membership, api TO %I', v_reader);
    EXECUTE format('GRANT SELECT ON membership.vw_member TO %I', v_reader);
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.current_principal_id(), api.current_organization_id(), '
                   'api.is_platform_operator(), membership.fn_request_context_key(), membership.fn_is_trusted_context() TO %I', v_reader);
    PERFORM pg_temp.test_as_kernel();
    EXECUTE format('GRANT EXECUTE ON FUNCTION membership.fn_resolve_request_context() TO %I', v_reader);
    PERFORM pg_temp.test_as_owner();
    PERFORM pg_temp.test_act_as('test|view-standards-a', v_a);

    EXECUTE format('SET ROLE %I', v_reader);
    BEGIN
        PERFORM FROM membership.vw_member;
        RAISE EXCEPTION 'TEST FAILED: a reader without SELECT on the membership tables read membership.vw_member';
    EXCEPTION WHEN insufficient_privilege THEN
        NULL;
    END;
    EXECUTE format('SET ROLE %I', v_owner);

    EXECUTE format('GRANT SELECT ON membership.organization_membership, membership.organization, membership.principal, '
                   'membership.user_principal, membership.role_assignment, membership.role TO %I', v_reader);
    EXECUTE format('SET ROLE %I', v_reader);
    SELECT array_agg(DISTINCT organization_object_id) INTO v_seen
    FROM membership.vw_member WHERE organization_object_id IN (v_a, v_b);
    EXECUTE format('SET ROLE %I', v_owner);
    PERFORM pg_temp.test_act_as(NULL);

    IF v_seen IS DISTINCT FROM ARRAY[v_a] THEN
        RAISE EXCEPTION 'TEST FAILED: a reader in organization A must see only A''s members through membership.vw_member, saw %', v_seen;
    END IF;
END $$;
