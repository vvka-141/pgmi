-- ============================================================================
-- Test: the authorization catalogue and the permission hook
-- ============================================================================

DO $$
BEGIN
    RAISE DEBUG '-> Authorization: the hook knows the vocabulary on every deploy';

    -- The framework's re-run file restores its default (knows nothing) on each
    -- deploy; membership must replace it again. This runs after every deploy.
    IF internal.is_platform_permission('project.read') IS DISTINCT FROM false
       OR internal.is_platform_permission('platform.operate') IS DISTINCT FROM true
       OR internal.is_platform_permission('no.such-permission') IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: the permission hook does not read membership.permission';
    END IF;
    IF EXISTS (SELECT FROM membership.role_permission rp JOIN membership.permission p ON p.id = rp.permission_id WHERE p.is_platform) THEN
        RAISE EXCEPTION 'TEST FAILED: a role grants a platform permission';
    END IF;

    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'a7c3e1d0-4b5f-4a61-9e2d-0c8b7f6a5e41', 'path', '/test-authz/projects', 'httpMethod', '^GET$',
        'name', 'test_authz_projects', 'permissions', jsonb_build_array('project.read'),
        'outputSchema', jsonb_build_object('type', 'object')),
        $b$BEGIN RETURN api.json_response(200, '{}'::jsonb); END;$b$);

    BEGIN
        PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
            'id', 'a7c3e1d0-4b5f-4a61-9e2d-0c8b7f6a5e42', 'path', '/test-authz/unknown', 'httpMethod', '^GET$',
            'name', 'test_authz_unknown', 'permissions', jsonb_build_array('project.delete-everything'),
            'outputSchema', jsonb_build_object('type', 'object')),
            $b$BEGIN RETURN api.json_response(200, '{}'::jsonb); END;$b$);
        RAISE EXCEPTION 'TEST FAILED: registration accepted a permission outside the catalogue';
    EXCEPTION WHEN foreign_key_violation THEN
        IF (SQLERRM LIKE '%declares unknown permission(s)%project.delete-everything%') IS NOT TRUE THEN
            RAISE EXCEPTION 'TEST FAILED: an unknown permission must be refused by the vocabulary check, got: %', SQLERRM;
        END IF;
    END;

    RAISE DEBUG '  + hook, registration and catalogue agree';
END $$;

DO $$
DECLARE
    v_case  record;
    v_level text := current_setting('client_min_messages');
BEGIN
    RAISE DEBUG '-> Authorization: catalogue checks';
    PERFORM set_config('client_min_messages', 'warning', true) WHERE v_level = 'notice';

    FOR v_case IN
        SELECT * FROM (VALUES
            ('a grant pattern matching nothing', 'matches no organization permission',
             '{"permissions": [{"id": "project.read", "description": "Read"}],
               "roles": [{"id": "org.owner", "name": "Owner", "sortOrder": 1, "grants": ["*"]},
                         {"id": "org.admin", "name": "Admin", "sortOrder": 2, "grants": ["projekt.*"]}]}'),
            ('a grant of a platform permission', 'matches no organization permission',
             '{"permissions": [{"id": "project.read", "description": "Read"}, {"id": "platform.operate", "description": "Operate", "platform": true}],
               "roles": [{"id": "org.owner", "name": "Owner", "sortOrder": 1, "grants": ["*"]},
                         {"id": "org.admin", "name": "Admin", "sortOrder": 2, "grants": ["platform.operate"]}]}'),
            ('an owner weaker than an admin', 'org.owner must hold every permission of org.admin',
             '{"permissions": [{"id": "project.read", "description": "Read"}, {"id": "project.write", "description": "Write"}],
               "roles": [{"id": "org.owner", "name": "Owner", "sortOrder": 1, "grants": ["project.read"]},
                         {"id": "org.admin", "name": "Admin", "sortOrder": 2, "grants": ["project.*"]}]}'),
            ('no org.admin', 'org.owner and org.admin must exist',
             '{"permissions": [{"id": "project.read", "description": "Read"}],
               "roles": [{"id": "org.owner", "name": "Owner", "sortOrder": 1, "grants": ["*"]}]}')
        ) AS t(label, expected, catalogue)
    LOOP
        BEGIN
            PERFORM pg_temp.sync_authorization(v_case.catalogue::jsonb);
            RAISE EXCEPTION 'TEST FAILED: the catalogue accepted %', v_case.label;
        EXCEPTION WHEN OTHERS THEN
            IF SQLERRM NOT LIKE '%' || v_case.expected || '%' THEN
                RAISE EXCEPTION 'TEST FAILED: % refused with the wrong message: %', v_case.label, SQLERRM;
            END IF;
        END;
    END LOOP;

    BEGIN
        PERFORM pg_temp.sync_authorization(
            '{"permissions": [{"id": "project.read", "description": "Read"}],
              "roles": [{"id": "org.owner", "name": "Owner", "sortOrder": 1, "grants": ["*"]},
                        {"id": "org.admin", "name": "Admin", "sortOrder": 2, "grants": ["*"]}]}'::jsonb);
        RAISE EXCEPTION 'TEST FAILED: the catalogue dropped roles still held by members';
    EXCEPTION WHEN foreign_key_violation THEN
        NULL;
    END;
    PERFORM set_config('client_min_messages', v_level, true);

    RAISE DEBUG '  + bad catalogues refused before anything changes';
END $$;

DO $$
DECLARE
    v_corp  uuid := current_setting('test.corp_id')::uuid;
    v_bob   uuid := current_setting('test.bob_id')::uuid;
    v_member_grants text[];
BEGIN
    RAISE DEBUG '-> Authorization: the readers';

    PERFORM pg_temp.test_act_as('test|alice-001');
    IF api.current_organization_id() IS NOT NULL OR api.current_permissions() IS DISTINCT FROM '{}' THEN
        RAISE EXCEPTION 'TEST FAILED: without an organization a member holds no organization permission';
    END IF;

    -- From the catalogue, so an application that grants org.member a new
    -- permission in data/authorization.json does not break this test.
    v_member_grants := ARRAY(SELECT rp.permission_id FROM membership.role_permission rp
                             WHERE rp.role_id = 'org.member' ORDER BY rp.permission_id);
    PERFORM pg_temp.test_act_as('test|alice-001', v_corp);
    IF api.current_organization_id() IS DISTINCT FROM v_corp
       OR api.current_permissions() IS DISTINCT FROM v_member_grants
       OR (v_member_grants @> ARRAY['members.read']) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: org.member in its organization: %, %', api.current_organization_id(), api.current_permissions();
    END IF;

    PERFORM pg_temp.test_act_as('test|bob-001', v_corp);
    IF api.current_organization_id() IS NOT NULL OR api.current_permissions() IS DISTINCT FROM '{}' THEN
        RAISE EXCEPTION 'TEST FAILED: a non-member named an organization and got it';
    END IF;

    PERFORM pg_temp.test_operator(v_bob);
    PERFORM pg_temp.test_act_as('test|bob-001', NULL, true);
    IF api.current_permissions() IS DISTINCT FROM ARRAY['platform.operate'] OR (api.is_platform_operator()) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: a platform operator holds the platform permissions, got %', api.current_permissions();
    END IF;

    PERFORM pg_temp.test_act_as(NULL);
    RAISE NOTICE '  + authorization: the hook, the catalogue checks and the readers agree';
END $$;
