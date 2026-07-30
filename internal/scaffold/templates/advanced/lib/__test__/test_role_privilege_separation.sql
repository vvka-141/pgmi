-- ============================================================================
-- Test: Role privilege separation
--
-- The customer role is the least-privileged login role. It must not be able to
-- reach the gateway entrypoints (SECURITY DEFINER, identity taken from an
-- unverified header) nor the registration functions (CREATE FUNCTION from a
-- caller-supplied body, executed as the owner). Either one is a full
-- privilege escalation to database owner.
--
-- The same applies to schema membership, where the credential lifecycle lives:
-- what any role can reach there must be a deliberate grant, never PostgreSQL's
-- default EXECUTE-to-PUBLIC.
-- ============================================================================

DO $$
DECLARE
    v_owner_role text := pg_temp.deployment_setting('database_owner_role');
    v_admin_role text := pg_temp.deployment_setting('database_admin_role');
    v_api_role text := pg_temp.deployment_setting('database_api_role');
    v_customer_role text := pg_temp.deployment_setting('database_customer_role');
    v_registration_fn text;
    v_gateway_fn text;
BEGIN
    RAISE NOTICE '-> Testing role privilege separation';

    -- The escalation vector: customer must not inherit the gateway role.
    IF pg_has_role(v_customer_role, v_api_role, 'USAGE') THEN
        RAISE EXCEPTION 'customer role % inherits api role % — gateway entrypoints and every api-role grant are reachable from the least-privileged login role',
            v_customer_role, v_api_role;
    END IF;

    IF pg_has_role(v_customer_role, v_owner_role, 'USAGE') THEN
        RAISE EXCEPTION 'customer role % inherits owner role %', v_customer_role, v_owner_role;
    END IF;

    RAISE NOTICE '  + customer role inherits neither api nor owner';

    -- Registration is deploy-time DDL: only the admin role may call it.
    FOREACH v_registration_fn IN ARRAY ARRAY[
        'api.create_or_replace_rest_handler(jsonb, text)',
        'api.create_or_replace_rpc_handler(jsonb, text)',
        'api.create_or_replace_mcp_handler(jsonb, text)'
    ] LOOP
        IF has_function_privilege(v_customer_role, v_registration_fn, 'EXECUTE') THEN
            RAISE EXCEPTION 'customer role can EXECUTE % — arbitrary SQL as the database owner', v_registration_fn;
        END IF;

        IF has_function_privilege(v_api_role, v_registration_fn, 'EXECUTE') THEN
            RAISE EXCEPTION 'api role can EXECUTE % — a compromised gateway could rewrite any handler', v_registration_fn;
        END IF;

        IF NOT has_function_privilege(v_admin_role, v_registration_fn, 'EXECUTE') THEN
            RAISE EXCEPTION 'admin role cannot EXECUTE % — deployment would break', v_registration_fn;
        END IF;
    END LOOP;

    RAISE NOTICE '  + registration functions reachable only by the admin role';

    -- Gateway entrypoints: the api role is the gateway and legitimately holds
    -- these; the customer role must not.
    FOREACH v_gateway_fn IN ARRAY ARRAY[
        'api.rest_invoke(text, text, extensions.hstore, bytea)',
        'api.rpc_invoke(uuid, extensions.hstore, bytea)'
    ] LOOP
        IF has_function_privilege(v_customer_role, v_gateway_fn, 'EXECUTE') THEN
            RAISE EXCEPTION 'customer role can EXECUTE % — it could impersonate any user via the identity headers', v_gateway_fn;
        END IF;

        IF NOT has_function_privilege(v_api_role, v_gateway_fn, 'EXECUTE') THEN
            RAISE EXCEPTION 'api role cannot EXECUTE % — the gateway would be unable to serve requests', v_gateway_fn;
        END IF;
    END LOOP;

    RAISE NOTICE '  + gateway entrypoints reachable by the api role, not the customer role';
END $$;

-- Every SECURITY DEFINER routine must pin search_path. Without it the caller
-- chooses which schema an unqualified name resolves in, so a table or function
-- planted in a schema they can create in runs as the definer — the escalation
-- the role separation above exists to prevent, reached from inside instead.
-- Checked against pg_proc, not the source text, so a routine created by any
-- path is covered.
DO $$
DECLARE
    v_unpinned text[];
BEGIN
    SELECT coalesce(array_agg(format('%s.%s', n.nspname, p.proname)
                              ORDER BY n.nspname, p.proname), '{}')
    INTO v_unpinned
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE p.prosecdef
      AND n.nspname IN ('api', 'internal', 'core', 'common', 'membership')
      AND NOT EXISTS (
          SELECT 1 FROM unnest(coalesce(p.proconfig, '{}')) AS cfg
          WHERE cfg LIKE 'search_path=%'
      );

    IF cardinality(v_unpinned) > 0 THEN
        RAISE EXCEPTION 'SECURITY DEFINER without SET search_path: % — the caller decides where unqualified names resolve, and the body runs as the definer',
            array_to_string(v_unpinned, ', ');
    END IF;

    RAISE NOTICE '  + every SECURITY DEFINER routine pins search_path';
END $$;

-- api schema: no routine reachable by PUBLIC. ALL ROUTINES (not ALL FUNCTIONS)
-- is required because api.purge_exchanges is a procedure.
DO $$
DECLARE
    v_public_routines text[];
BEGIN
    SELECT coalesce(array_agg(format('%s(%s)', p.oid::regproc, pg_get_function_arguments(p.oid))
                              ORDER BY p.oid::regproc::text), '{}')
    INTO v_public_routines
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = 'api'
      AND (p.proacl IS NULL
           OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a
                      WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE'));

    IF cardinality(v_public_routines) > 0 THEN
        RAISE EXCEPTION 'api routines are EXECUTE-able by PUBLIC: % — any authenticated session reaches gateway and maintenance surface',
            array_to_string(v_public_routines, ', ');
    END IF;

    RAISE NOTICE '  + no api routine is EXECUTE-able by PUBLIC';
END $$;

-- Live attempt: a customer session registering a handler must be denied.
DO $$
DECLARE
    v_customer_role text := pg_temp.deployment_setting('database_customer_role');
    v_denied boolean := false;
BEGIN
    EXECUTE format('SET ROLE %I', v_customer_role);

    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'a0000000-0000-4000-8000-00000000dead',
                'handlerFunctionName', 'privilege_escalation_probe',
                'method', 'GET',
                'path', '/probe',
                'outputSchema', jsonb_build_object('type', 'object')
            ),
            'BEGIN RETURN api.json_response(200, ''{}''::jsonb); END;'
        );
    EXCEPTION
        WHEN insufficient_privilege THEN
            v_denied := true;
    END;

    RESET ROLE;

    IF v_denied IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'customer session registered a handler — privilege escalation to database owner is live';
    END IF;

    IF EXISTS (SELECT 1 FROM api.handler WHERE handler_function_name = 'privilege_escalation_probe') THEN
        RAISE EXCEPTION 'privilege escalation probe left a registered handler behind';
    END IF;

    RAISE NOTICE '  + customer session denied handler registration';
END $$;

-- The credential surface in schema membership: every reachable routine must be
-- there by an explicit grant. A NULL proacl is the default ACL, which grants
-- EXECUTE to PUBLIC — so "no ACL recorded" is the failure, not the pass.
DO $$
DECLARE
    v_api_role text := pg_temp.deployment_setting('database_api_role');
    v_customer_role text := pg_temp.deployment_setting('database_customer_role');
    v_public_routines text[];
    v_fn text;
BEGIN
    SELECT coalesce(array_agg(format('%s(%s)', p.oid::regproc, pg_get_function_arguments(p.oid))
                              ORDER BY p.oid::regproc::text), '{}')
    INTO v_public_routines
    FROM pg_proc p
    JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = 'membership'
      AND (p.proacl IS NULL
           OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a
                      WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE'));

    IF cardinality(v_public_routines) > 0 THEN
        RAISE EXCEPTION 'membership routines are EXECUTE-able by PUBLIC: % — any role with USAGE on the schema reaches the credential lifecycle',
            array_to_string(v_public_routines, ', ');
    END IF;

    RAISE NOTICE '  + no membership routine is EXECUTE-able by PUBLIC';

    -- Self-service is a feature: a customer session manages its own keys, and
    -- membership.can_create_api_key / can_manage_api_key confine it. Assert the
    -- grant so revoking PUBLIC cannot silently take the feature away.
    FOREACH v_fn IN ARRAY ARRAY[
        'membership.create_api_key(uuid, uuid, text, timestamptz, timestamptz)',
        'membership.disable_api_key(text)',
        'membership.enable_api_key(text)',
        'membership.revoke_api_key(text)'
    ] LOOP
        IF NOT has_function_privilege(v_customer_role, v_fn, 'EXECUTE') THEN
            RAISE EXCEPTION 'customer role cannot EXECUTE % — self-service key management is broken', v_fn;
        END IF;
    END LOOP;

    RAISE NOTICE '  + customer role keeps the four self-service lifecycle functions';

    -- And nothing beyond them. validate_api_key resolves a raw key to its owner
    -- and org; generate_api_key_material mints secret material. Both belong to
    -- the gateway and the admin role, not to the sessions they authenticate.
    FOREACH v_fn IN ARRAY ARRAY[
        'membership.validate_api_key(text)',
        'membership.generate_api_key_material()'
    ] LOOP
        IF has_function_privilege(v_customer_role, v_fn, 'EXECUTE') THEN
            RAISE EXCEPTION 'customer role can EXECUTE % — it is gateway surface, not customer surface', v_fn;
        END IF;
    END LOOP;

    IF has_function_privilege(v_api_role, 'membership.generate_api_key_material()', 'EXECUTE') THEN
        RAISE EXCEPTION 'api role can EXECUTE membership.generate_api_key_material() — key material is minted inside create_api_key, never on request';
    END IF;

    RAISE NOTICE '  + validate_api_key and generate_api_key_material are out of customer reach';
END $$;
